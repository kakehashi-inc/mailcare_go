package workers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"develop_app/app/models"
	"develop_app/app/modules"
)

// newTestCore builds a core backed by an in-memory SPA build and a temporary
// SQLite database (migrated from the repository's app/migrations) holding one
// token.
func newTestCore(t *testing.T) (*core, *models.Token) {
	t.Helper()
	modules.MigrationsFS = os.DirFS("../..")
	modules.SetDataDir(t.TempDir())
	db, err := modules.OpenDB("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	tok, err := modules.CreateToken(db, "test", "test", sql.NullTime{}, true)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return &core{
		db: db,
		spaFS: fstest.MapFS{
			"index.html":           {Data: []byte("<!doctype html><title>shell</title>")},
			"assets/index-abc.css": {Data: []byte("body{}")},
			"manifest.webmanifest": {Data: []byte("{}")},
		},
		startTime: time.Now(),
		apiListen: "127.0.0.1",
		webListen: "127.0.0.1",
		apiPort:   8081,
		webPort:   8080,
	}, tok
}

func TestWebHandler(t *testing.T) {
	c, _ := newTestCore(t)
	h := c.webHandler()
	cases := []struct {
		path       string
		wantStatus int
		wantBody   string // substring
		wantCType  string // prefix
	}{
		{"/", http.StatusOK, "shell", "text/html"},
		{"/assets/index-abc.css", http.StatusOK, "body{}", "text/css"},
		{"/manifest.webmanifest", http.StatusOK, "{}", "application/manifest+json"},
		{"/api/v1/health", http.StatusOK, `"version"`, "application/json"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.wantStatus {
			t.Errorf("GET %s: status %d, want %d", c.path, rec.Code, c.wantStatus)
		}
		if !strings.Contains(rec.Body.String(), c.wantBody) {
			t.Errorf("GET %s: body %q does not contain %q", c.path, rec.Body.String(), c.wantBody)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, c.wantCType) {
			t.Errorf("GET %s: Content-Type %q, want prefix %q", c.path, got, c.wantCType)
		}
	}
}

func TestApiHandler(t *testing.T) {
	c, tok := newTestCore(t)
	h := c.apiHandler()

	// The API server serves no Web UI.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /: status %d, want %d", rec.Code, http.StatusNotFound)
	}

	// /api/v1/ requires a bearer token.
	for _, auth := range []string{"", "Bearer nope"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/health with %q: status %d, want %d", auth, rec.Code, http.StatusUnauthorized)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("GET /api/v1/health with a valid token: status %d, body %q", rec.Code, rec.Body.String())
	}

	// Control endpoints answer loopback clients only and need no token.
	req = httptest.NewRequest(http.MethodGet, "/control/status", nil)
	req.RemoteAddr = "192.168.1.5:40000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("remote client: status %d, want %d", rec.Code, http.StatusForbidden)
	}
	req = httptest.NewRequest(http.MethodGet, "/control/status", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("loopback client: status %d, want %d", rec.Code, http.StatusOK)
	}
	for _, want := range []string{`"api_listen":"127.0.0.1:8081"`, `"tokens":1`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("loopback client: body %q does not contain %s", rec.Body.String(), want)
		}
	}
}
