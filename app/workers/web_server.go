package workers

import (
	"errors"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"mailcare/app/modules"
)

func init() {
	// Extensions the Go built-in MIME table does not know. The OS table is not
	// always available (e.g. minimal containers), so register them explicitly.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	_ = mime.AddExtensionType(".ico", "image/x-icon")
	_ = mime.AddExtensionType(".woff2", "font/woff2")
}

// webHandler builds the server's handler: the cookie-authenticated internal
// API, the login endpoints, the loopback-only control endpoints and the SPA.
// The user role is read-only: every write except the caller's own password
// and profile needs an administrator.
func (c *core) webHandler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated.
	mux.HandleFunc("GET /api/v1/health", c.handleHealth)
	mux.HandleFunc("GET /api/v1/setup", c.handleSetupStatus)
	mux.HandleFunc("POST /web/setup", c.handleSetup)
	mux.HandleFunc("POST /web/login", c.handleLogin)
	mux.HandleFunc("POST /web/logout", c.handleLogout)

	// Account.
	mux.HandleFunc("GET /api/v1/me", c.handleMe)
	mux.HandleFunc("PUT /api/v1/me/password", c.handleChangeMyPassword)
	mux.HandleFunc("PUT /api/v1/me/profile", c.handleChangeMyProfile)

	// Dashboard.
	mux.HandleFunc("GET /api/v1/dashboard", c.handleDashboard)

	// Mailboxes.
	mux.HandleFunc("GET /api/v1/mailboxes", c.handleListMailboxes)
	mux.HandleFunc("POST /api/v1/mailboxes", c.requireAdmin(c.handleCreateMailbox))
	mux.HandleFunc("POST /api/v1/mailboxes/test", c.requireAdmin(c.handleTestMailbox))
	mux.HandleFunc("GET /api/v1/mailboxes/{id}", c.handleGetMailbox)
	mux.HandleFunc("PUT /api/v1/mailboxes/{id}", c.requireAdmin(c.handleUpdateMailbox))
	mux.HandleFunc("DELETE /api/v1/mailboxes/{id}", c.requireAdmin(c.handleDeleteMailbox))
	mux.HandleFunc("POST /api/v1/mailboxes/{id}/sync", c.requireAdmin(c.handleSyncMailbox))

	// Bounce groups (alerts).
	mux.HandleFunc("GET /api/v1/mailboxes/{id}/groups", c.handleListGroups)
	mux.HandleFunc("GET /api/v1/mailboxes/{id}/groups/{key}", c.handleGetGroup)
	mux.HandleFunc("PUT /api/v1/mailboxes/{id}/groups/{key}/state", c.requireAdmin(c.handleSetGroupState))
	mux.HandleFunc("POST /api/v1/mailboxes/{id}/groups/{key}/analyze", c.requireAdmin(c.handleAnalyzeGroup))

	// Messages.
	mux.HandleFunc("GET /api/v1/mailboxes/{id}/messages", c.handleListMessages)
	mux.HandleFunc("GET /api/v1/mailboxes/{id}/messages/{key}", c.handleGetMessage)
	mux.HandleFunc("GET /api/v1/mailboxes/{id}/messages/{key}/html", c.handleMessageHTML)
	mux.HandleFunc("GET /api/v1/mailboxes/{id}/messages/{key}/raw", c.handleMessageRaw)

	// Jobs.
	mux.HandleFunc("GET /api/v1/jobs", c.handleListJobs)
	mux.HandleFunc("POST /api/v1/jobs", c.requireAdmin(c.handleCreateJob))
	mux.HandleFunc("GET /api/v1/jobs/{id}", c.handleGetJob)
	mux.HandleFunc("DELETE /api/v1/jobs/{id}", c.requireAdmin(c.handleCancelJob))

	// Settings.
	mux.HandleFunc("GET /api/v1/settings", c.handleGetSettings)
	mux.HandleFunc("PUT /api/v1/settings", c.requireAdmin(c.handleUpdateSettings))
	mux.HandleFunc("GET /api/v1/settings/notifications", c.requireAdmin(c.handleGetNotificationSettings))
	mux.HandleFunc("PUT /api/v1/settings/notifications", c.requireAdmin(c.handleUpdateNotificationSettings))
	mux.HandleFunc("POST /api/v1/notifications/test", c.requireAdmin(c.handleTestNotification))
	mux.HandleFunc("POST /api/v1/notifications/send", c.requireAdmin(c.handleSendNotification))

	// Users (admin only).
	mux.HandleFunc("GET /api/v1/users", c.requireAdmin(c.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", c.requireAdmin(c.handleCreateUser))
	mux.HandleFunc("PUT /api/v1/users/{id}", c.requireAdmin(c.handleUpdateUser))
	mux.HandleFunc("PUT /api/v1/users/{id}/password", c.requireAdmin(c.handleSetUserPassword))
	mux.HandleFunc("DELETE /api/v1/users/{id}", c.requireAdmin(c.handleDeleteUser))

	// Tokens (admin only; reserved for the future API, not used for login).
	mux.HandleFunc("GET /api/v1/tokens", c.requireAdmin(c.handleListTokens))
	mux.HandleFunc("POST /api/v1/tokens", c.requireAdmin(c.handleCreateToken))
	mux.HandleFunc("DELETE /api/v1/tokens/{identifier}", c.requireAdmin(c.handleDeleteToken))

	// Control plane (loopback only, no cookie).
	mux.HandleFunc("POST /control/shutdown", c.handleControlShutdown)
	mux.HandleFunc("GET /control/status", c.handleControlStatus)
	mux.HandleFunc("POST /control/jobs", c.handleControlCreateJob)
	mux.HandleFunc("GET /control/jobs/{id}", c.handleControlGetJob)

	// Unknown API paths answer JSON, not the SPA shell.
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.HandleFunc("/web/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.HandleFunc("/control/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	// SPA + static assets fallback.
	mux.HandleFunc("/", c.serveSPA)

	return c.webMiddleware(mux)
}

// isLoopbackRequest reports whether the client address is 127.0.0.1 or ::1.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isPublicAPIPath lists the /api/v1 paths that need no session.
func isPublicAPIPath(p string) bool {
	return p == "/api/v1/health" || p == "/api/v1/setup"
}

// webMiddleware restricts /control/* to loopback clients and enforces cookie
// authentication on /api/v1/* (except health and setup). Other paths (SPA,
// login, logout, setup) pass through unauthenticated.
func (c *core) webMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/control/"):
			if !isLoopbackRequest(r) {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/v1/") && !isPublicAPIPath(p):
			sess, err := modules.ValidateSessionCookie(c.db, c.key, cookieValue(r))
			if err != nil {
				// 401 strictly means "this session is invalid; log in again".
				// An internal failure (e.g. a transient DB error) says nothing
				// about the session, so answer 503 instead: the SPA keeps the
				// session and retries rather than showing the login screen.
				if errors.Is(err, modules.ErrSessionInvalid) {
					writeError(w, http.StatusUnauthorized, "unauthorized")
				} else {
					log.Printf("session validation failed transiently: %v", err)
					writeError(w, http.StatusServiceUnavailable, "temporarily unavailable")
				}
				return
			}
			// Sliding expiry for remembered sessions only, throttled: re-issue
			// the cookie at most once per SessionRefreshInterval. It was
			// issued at expiry - cookie TTL. A browser-session cookie keeps
			// its fixed expiry.
			issued := sess.Expiry.Add(-time.Duration(c.cookieTTLHours) * time.Hour)
			if sess.Remember && time.Since(issued) >= modules.SessionRefreshInterval {
				c.setSessionCookie(w, sess.User, true)
			}
			next.ServeHTTP(w, withUser(r, sess.User))
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// requireAdmin wraps a handler so that only administrators reach it.
func (c *core) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if u.Role != modules.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}

// cookieValue reads the raw session cookie value.
func cookieValue(r *http.Request) string {
	if ck, err := r.Cookie(modules.CookieName); err == nil {
		return ck.Value
	}
	return ""
}

// serveSPA serves the embedded SPA, falling back to index.html for unknown
// client-side routes.
func (c *core) serveSPA(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	data, err := fs.ReadFile(c.spaFS, p)
	if err != nil {
		p = "index.html"
		data, err = fs.ReadFile(c.spaFS, p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	ctype := mime.TypeByExtension(path.Ext(p))
	switch path.Ext(p) {
	case ".webmanifest":
		ctype = "application/manifest+json"
	case ".ico":
		ctype = "image/x-icon"
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	http.ServeContent(w, r, p, time.Time{}, strings.NewReader(string(data)))
}
