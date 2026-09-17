package workers

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"develop_app/app/models"
	"develop_app/app/modules"
)

// apiHandler builds the API server's handler. This server authenticates ONLY
// via the "Authorization: Bearer <token>" header. It contains no cookie logic
// and serves no Web UI. Control endpoints are restricted to localhost.
func (c *core) apiHandler() http.Handler {
	mux := http.NewServeMux()

	// REST API (bearer-token authenticated). Handlers can read the authenticated
	// token with tokenFrom(r).
	mux.HandleFunc("GET /api/v1/health", c.handleHealth)

	// Control plane (localhost only, no token).
	mux.HandleFunc("POST /control/shutdown", c.handleControlShutdown)
	mux.HandleFunc("GET /control/status", c.handleControlStatus)

	return c.apiMiddleware(mux)
}

// apiMiddleware enforces localhost-only access on /control/ and bearer-token
// authentication on /api/v1/. No other paths are served.
func (c *core) apiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/control/"):
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil || (host != "127.0.0.1" && host != "::1") {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		case strings.HasPrefix(path, "/api/v1/"):
			tok, err := modules.ValidateBearer(c.db, bearerHeader(r))
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, withToken(r, tok))
		default:
			http.NotFound(w, r)
		}
	})
}

// bearerHeader extracts the token from the Authorization header. The API server
// never inspects cookies.
func bearerHeader(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}

// --- control handlers (served only on the API server, localhost) ---

func (c *core) handleControlShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "shutting down"})
	go c.shutdown()
}

func (c *core) handleControlStatus(w http.ResponseWriter, r *http.Request) {
	tokens, _ := models.CountTokens(c.db)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "running",
		"name":       modules.AppName,
		"version":    modules.AppVersion,
		"api_listen": fmt.Sprintf("%s:%d", c.apiListen, c.apiPort),
		"web_listen": fmt.Sprintf("%s:%d", c.webListen, c.webPort),
		"uptime":     formatDuration(time.Since(c.startTime)),
		"tokens":     tokens,
	})
}
