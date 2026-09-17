package workers

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

func init() {
	// Extensions the Go built-in MIME table does not know. The OS table is not
	// always available (e.g. minimal containers), so register them explicitly.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	_ = mime.AddExtensionType(".ico", "image/x-icon")
	_ = mime.AddExtensionType(".woff2", "font/woff2")
}

// webHandler builds the Web server's handler. This server serves the SPA and
// the data endpoints the SPA calls on the same origin. Web authentication (e.g.
// a session cookie) belongs in a middleware here; the API server has none.
func (c *core) webHandler() http.Handler {
	mux := http.NewServeMux()

	// Data endpoints — shared core handlers.
	mux.HandleFunc("GET /api/v1/health", c.handleHealth)

	// SPA + static assets fallback.
	mux.HandleFunc("/", c.serveSPA)

	return mux
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
