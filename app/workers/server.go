package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"develop_app/app/models"
	"develop_app/app/modules"
)

func init() {
	// Register the server entry point so the CLI (modules) can start it.
	modules.StartServer = startServer
}

// ctxKey is the type for request-context keys.
type ctxKey int

const tokenCtxKey ctxKey = 0

// core holds the shared state for both the API and Web servers. The data
// handlers on *core contain NO transport-auth logic, keeping the two servers
// fully independent at the auth layer.
type core struct {
	db        *sql.DB
	spaFS     fs.FS
	startTime time.Time
	apiListen string
	webListen string
	apiPort   int
	webPort   int

	apiSrv       *http.Server
	webSrv       *http.Server
	shutdownOnce sync.Once
}

// needsLoopbackListener reports whether addr is a specific interface IP that
// does not itself cover loopback, and therefore needs a separate 127.0.0.1
// listener added alongside it. Addresses that already reach loopback --
// "" / 0.0.0.0 / 127.0.0.1 / localhost -- do not (a second 127.0.0.1 bind would
// collide, since 0.0.0.0 already includes it).
func needsLoopbackListener(addr string) bool {
	switch addr {
	case "", "0.0.0.0", "127.0.0.1", "localhost":
		return false
	default:
		return true
	}
}

// listenWithLoopback opens the listener for addr:port and, when addr is a
// specific interface IP (e.g. 192.168.1.5), an additional listener on
// 127.0.0.1:port bound to the same port (different IP, so no conflict). The
// extra loopback listener keeps localhost-only endpoints -- the control plane,
// and the reachability that service stop/status rely on -- available even when
// the server is bound to a single external interface.
func listenWithLoopback(addr string, port int) ([]net.Listener, error) {
	main, err := net.Listen("tcp", fmt.Sprintf("%s:%d", addr, port))
	if err != nil {
		return nil, err
	}
	if !needsLoopbackListener(addr) {
		return []net.Listener{main}, nil
	}
	loop, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		main.Close()
		return nil, err
	}
	return []net.Listener{main, loop}, nil
}

func startServer(apiListen, webListen string, apiPort, webPort int) error {
	if _, err := modules.EnsureDataDir(); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	db, err := modules.OpenDB("")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Resolve settings for use this run: explicit arg > saved setting > default. A
	// setting with no saved row resolves from the current code default — that is what
	// lets a changed default take effect.
	apiListen = resolveStr(db, modules.SettingApiListen, apiListen, modules.DefaultApiListenAddr)
	webListen = resolveStr(db, modules.SettingWebListen, webListen, modules.DefaultWebListenAddr)
	apiPort = resolveInt(db, modules.SettingApiPort, apiPort, modules.DefaultApiPort)
	webPort = resolveInt(db, modules.SettingWebPort, webPort, modules.DefaultWebPort)

	if apiPort == webPort {
		return fmt.Errorf("API port and Web port must differ (both are %d)", apiPort)
	}

	// Persist ONLY values that differ from the code default; when a value equals the
	// default, delete any stale row instead of writing it. This keeps settings holding
	// only real overrides, so a later change to a default takes effect (a baked-in
	// default would otherwise win).
	for _, s := range []struct {
		key       string
		val       string
		isDefault bool
	}{
		{modules.SettingApiListen, apiListen, apiListen == modules.DefaultApiListenAddr},
		{modules.SettingWebListen, webListen, webListen == modules.DefaultWebListenAddr},
		{modules.SettingApiPort, strconv.Itoa(apiPort), apiPort == modules.DefaultApiPort},
		{modules.SettingWebPort, strconv.Itoa(webPort), webPort == modules.DefaultWebPort},
	} {
		var err error
		if s.isDefault {
			err = models.DeleteSetting(db, s.key)
		} else {
			err = models.SetSetting(db, s.key, s.val)
		}
		if err != nil {
			log.Printf("failed to persist setting %s: %v", s.key, err)
		}
	}

	// Ensure a default token exists. Deleting the default never promotes another
	// token: on the next start an existing "default" token is made the default,
	// otherwise a new one is created.
	if _, err := models.GetDefaultToken(db); err == sql.ErrNoRows {
		if _, gerr := models.GetTokenByIdentifier(db, "default"); gerr == nil {
			if serr := models.SetDefaultToken(db, "default"); serr != nil {
				return fmt.Errorf("failed to set default token: %w", serr)
			}
			log.Printf("Default token set to %q", "default")
		} else if gerr == sql.ErrNoRows {
			tok, cerr := modules.CreateToken(db, "default", "default", sql.NullTime{}, true)
			if cerr != nil {
				return fmt.Errorf("failed to create default token: %w", cerr)
			}
			log.Printf("Created default token: %s", tok.Token)
		} else {
			return gerr
		}
	} else if err != nil {
		return err
	}

	spaFS, err := fs.Sub(modules.FrontendFS, "frontend/dist")
	if err != nil {
		return fmt.Errorf("failed to load embedded frontend: %w", err)
	}

	c := &core{
		db: db, spaFS: spaFS, startTime: time.Now(),
		apiListen: apiListen, webListen: webListen, apiPort: apiPort, webPort: webPort,
	}

	c.apiSrv = &http.Server{Handler: c.apiHandler(), ReadHeaderTimeout: modules.ReadHeaderTimeout}
	c.webSrv = &http.Server{Handler: c.webHandler(), ReadHeaderTimeout: modules.ReadHeaderTimeout}

	apiLns, err := listenWithLoopback(apiListen, apiPort)
	if err != nil {
		return fmt.Errorf("failed to listen on API port %d: %w", apiPort, err)
	}
	webLn, err := net.Listen("tcp", fmt.Sprintf("%s:%d", webListen, webPort))
	if err != nil {
		for _, ln := range apiLns {
			ln.Close()
		}
		return fmt.Errorf("failed to listen on Web port %d: %w", webPort, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		c.shutdown()
	}()

	log.Printf("API server listening on %s:%d", apiListen, apiPort)
	if len(apiLns) > 1 {
		log.Printf("API server also listening on 127.0.0.1:%d for local control", apiPort)
	}
	log.Printf("Web server listening on %s:%d", webListen, webPort)

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	serve := func(srv *http.Server, ln net.Listener, name string) {
		defer wg.Done()
		if serr := srv.Serve(ln); serr != nil && serr != http.ErrServerClosed {
			select {
			case errCh <- fmt.Errorf("%s server error: %w", name, serr):
			default:
			}
			// Stop the rest so wg.Wait() below can return.
			c.shutdown()
		}
	}
	wg.Add(1 + len(apiLns))
	go serve(c.webSrv, webLn, "web")
	for _, ln := range apiLns {
		go serve(c.apiSrv, ln, "api")
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

// shutdown gracefully stops both servers. Safe to call multiple times (signal
// handler and control endpoint may both trigger it).
func (c *core) shutdown() {
	c.shutdownOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), modules.ShutdownGraceTimeout)
		defer cancel()
		if c.apiSrv != nil {
			_ = c.apiSrv.Shutdown(ctx)
		}
		if c.webSrv != nil {
			_ = c.webSrv.Shutdown(ctx)
		}
	})
}

// tokenFrom returns the authenticated token attached by an auth middleware.
//
//lint:ignore U1000 accessor for API handlers (none are registered yet)
func tokenFrom(r *http.Request) *models.Token {
	if t, ok := r.Context().Value(tokenCtxKey).(*models.Token); ok {
		return t
	}
	return nil
}

// withToken returns a copy of r with the token attached to its context.
func withToken(r *http.Request, tok *models.Token) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), tokenCtxKey, tok))
}

// =============================================================================
// Shared data handlers (auth-agnostic; read the token from the context).
// These contain NO cookie/bearer logic — that lives in the per-server middleware.
// =============================================================================

// handleHealth is the liveness endpoint; it also exposes the build version so
// the Web UI can show it.
func (c *core) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": modules.AppVersion,
	})
}

func resolveStr(db *sql.DB, key, arg, fallback string) string {
	if arg != "" {
		return arg
	}
	if saved := models.GetSetting(db, key); saved != "" {
		return saved
	}
	return fallback
}

func resolveInt(db *sql.DB, key string, arg, fallback int) int {
	if arg != 0 {
		return arg
	}
	if saved := models.GetSetting(db, key); saved != "" {
		if v, err := strconv.Atoi(saved); err == nil && v != 0 {
			return v
		}
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	return fmt.Sprintf("%dh %dm %ds", h, m, sec)
}
