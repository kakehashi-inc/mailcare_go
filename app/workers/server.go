package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/mailengine"
)

func init() {
	// Register the server entry point so the CLI (modules) can start it.
	modules.StartServer = startServer
}

// ctxKey is the type for request-context keys.
type ctxKey int

const userCtxKey ctxKey = 0

// core holds the shared state of the Web server.
type core struct {
	db             *sql.DB
	spaFS          fs.FS
	key            []byte
	startTime      time.Time
	webListen      string
	webPort        int
	cookieTTLHours int
	dataDir        string
	mailsRoot      string
	agentRoot      string

	jm    *modules.JobManager
	sched *modules.Scheduler

	srv          *http.Server
	shutdownOnce sync.Once
}

// newCore assembles a core over an open database. The job manager is created
// but not started; startServer starts it together with the scheduler.
func newCore(db *sql.DB, key []byte, spaFS fs.FS, dataDir, webListen string, webPort int) *core {
	c := &core{
		db: db, key: key, spaFS: spaFS, startTime: time.Now(),
		webListen: webListen, webPort: webPort,
		cookieTTLHours: modules.ResolveCookieTTLHours(db),
		dataDir:        dataDir,
	}
	c.mailsRoot = dataDir + string(os.PathSeparator) + modules.MailsDirName
	c.agentRoot = dataDir + string(os.PathSeparator) + modules.AgentDirName
	c.jm = modules.NewJobManager(db, key, c.mailsRoot, c.agentRoot, modules.TemplatesFS)
	c.sched = modules.NewScheduler(db, c.jm)
	return c
}

// needsLoopbackListener reports whether addr is a specific interface IP that
// does not itself cover loopback, and therefore needs a separate 127.0.0.1
// listener added alongside it. Addresses that already reach loopback --
// "" / 0.0.0.0 / 127.0.0.1 / localhost -- do not (a second 127.0.0.1 bind would
// collide, since 0.0.0.0 already includes it).
func needsLoopbackListener(addr string) bool {
	switch addr {
	case "", "0.0.0.0", "127.0.0.1", "localhost", "::", "::1", "[::]", "[::1]":
		return false
	default:
		return true
	}
}

// listenWithLoopback opens the listener for addr:port and, when addr is a
// specific interface IP (e.g. 192.168.1.5), an additional listener on
// 127.0.0.1:port (different IP, so no conflict). The extra loopback listener
// keeps the control endpoints -- which service stop / status and the CLI job
// submission rely on -- reachable even when the server is bound to a single
// external interface.
func listenWithLoopback(addr string, port int) ([]net.Listener, error) {
	main, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
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

func startServer(webListen string, webPort int, checkTimes []string) error {
	dataDir, err := modules.EnsureDataDir()
	if err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	db, err := modules.OpenDB("")
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()
	key, err := modules.LoadSecretKey()
	if err != nil {
		return err
	}

	// Resolve settings for this run: explicit arg > saved setting > default,
	// then persist only the values that differ from the code default.
	webListen = modules.ResolveWebListen(db, webListen)
	webPort = modules.ResolveWebPort(db, webPort)
	modules.SaveServerSettings(db, webListen, webPort)
	if checkTimes != nil {
		if err := modules.SaveCheckTimes(db, checkTimes); err != nil {
			log.Printf("failed to persist check_times: %v", err)
		}
	}

	spaFS, err := fs.Sub(modules.FrontendFS, "frontend/dist")
	if err != nil {
		return fmt.Errorf("failed to load embedded frontend: %w", err)
	}
	c := newCore(db, key, spaFS, dataDir, webListen, webPort)
	if err := os.MkdirAll(c.mailsRoot, 0o700); err != nil {
		return fmt.Errorf("failed to create the mails directory: %w", err)
	}
	if err := os.MkdirAll(c.agentRoot, 0o700); err != nil {
		return fmt.Errorf("failed to create the agent directory: %w", err)
	}
	modules.ResetStaleJobs(db, c.mailsRoot)

	c.srv = &http.Server{Handler: c.webHandler(), ReadHeaderTimeout: modules.ReadHeaderTimeout}
	listeners, err := listenWithLoopback(webListen, webPort)
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", webPort, err)
	}

	c.jm.Start()
	c.sched.Start()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		c.shutdown()
	}()

	if n, _ := models.CountUsers(db); n == 0 {
		log.Printf("No users yet: open the Web UI to create the first administrator, or run: %s user create --username <name> --role admin", modules.AppName)
	}
	log.Printf("%s %s listening on %s:%d (data: %s)", modules.AppName, modules.AppVersion, webListen, webPort, dataDir)
	if len(listeners) > 1 {
		log.Printf("also listening on 127.0.0.1:%d for local control", webPort)
	}
	if times := modules.ResolveCheckTimes(db); len(times) > 0 {
		log.Printf("check times: %s", modules.FormatCheckTimes(times))
	} else {
		log.Printf("check times: none (automatic checks are disabled)")
	}

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for _, ln := range listeners {
		wg.Add(1)
		go func(ln net.Listener) {
			defer wg.Done()
			if serr := c.srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
				select {
				case errCh <- fmt.Errorf("server error: %w", serr):
				default:
				}
				c.shutdown()
			}
		}(ln)
	}
	wg.Wait()
	c.sched.Stop()
	c.jm.Stop()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

// shutdown gracefully stops the HTTP server. Safe to call multiple times
// (signal handler and control endpoint may both trigger it). The job worker
// and the scheduler are stopped by startServer once the listeners are closed.
func (c *core) shutdown() {
	c.shutdownOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), modules.ShutdownGraceTimeout)
		defer cancel()
		if c.srv != nil {
			_ = c.srv.Shutdown(ctx)
		}
	})
}

// userFrom returns the authenticated user attached by the auth middleware.
func userFrom(r *http.Request) *models.User {
	if u, ok := r.Context().Value(userCtxKey).(*models.User); ok {
		return u
	}
	return nil
}

// withUser returns a copy of r with the user attached to its context.
func withUser(r *http.Request, u *models.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
}

// --- Response / request helpers ---

// maxJSONBody bounds the size of a JSON request body.
const maxJSONBody = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the JSON error shape {"error": msg}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeInternalError logs the cause and answers 500 with a generic message.
func writeInternalError(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	writeError(w, http.StatusInternalServerError, what)
}

// decodeJSON reads a JSON body into v. It returns false (after answering
// 400) when the body is not valid JSON.
func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// pathID parses the {name} path value as a positive integer.
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return id, true
}

// mailboxFromPath loads the mailbox named by {id}, answering 404 when absent.
func (c *core) mailboxFromPath(w http.ResponseWriter, r *http.Request) (*models.Mailbox, bool) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return nil, false
	}
	mb, err := models.GetMailboxByID(c.db, id)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "mailbox not found")
		return nil, false
	}
	if err != nil {
		writeInternalError(w, "failed to load mailbox", err)
		return nil, false
	}
	return mb, true
}

// openIndex opens the per-mailbox index of a mailbox for one request.
func (c *core) openIndex(r *http.Request, mb *models.Mailbox) (*sql.DB, error) {
	return mailengine.OpenIndex(r.Context(), c.mailsRoot, mb.Address, nil)
}

// mailboxAddresses maps mailbox ids to addresses (for job DTOs).
func (c *core) mailboxAddresses() map[int64]string {
	out := map[int64]string{}
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		log.Printf("failed to list mailboxes: %v", err)
		return out
	}
	for _, mb := range mailboxes {
		out[mb.ID] = mb.Address
	}
	return out
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	return fmt.Sprintf("%dh %dm %ds", h, m, sec)
}
