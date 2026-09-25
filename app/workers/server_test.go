package workers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// newTestCore builds a core backed by an in-memory SPA build and a temporary
// data directory (database migrated from the repository's app/migrations,
// fresh master key). No listener, job worker or scheduler is started.
func newTestCore(t *testing.T) *core {
	t.Helper()
	modules.MigrationsFS = os.DirFS("../..")
	dataDir := t.TempDir()
	modules.SetDataDir(dataDir)
	db, err := modules.OpenDB("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := modules.LoadSecretKey(db)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	var spaFS fs.FS = fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>shell</title>")},
		"assets/index-abc.css": {Data: []byte("body{}")},
		"manifest.webmanifest": {Data: []byte("{}")},
	}
	return newCore(db, key, spaFS, dataDir, "127.0.0.1", modules.DefaultWebPort)
}

// createUser inserts a user with the default preferences and no address.
func createUser(t *testing.T, db *sql.DB, username, displayName, password, role string) *models.User {
	t.Helper()
	u, err := modules.CreateUserFrom(db, modules.NewUser{Username: username, DisplayName: displayName, Password: password, Role: role})
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

// testRemoteAddr is the client address of every request made by do.
const testRemoteAddr = "192.168.1.5:40000"

// newRequest builds a request the way the SPA sends it: a JSON body when
// given, and the JSON content type on every state-changing method (the
// server refuses such requests without it).
func newRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = testRemoteAddr
	if body != nil || (method != http.MethodGet && method != http.MethodHead) {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// serve runs one request through the handler.
func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// do performs a request against the handler, optionally with a JSON body
// and a session cookie.
func do(t *testing.T, h http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(t, method, path, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return serve(h, req)
}

// sessionCookie extracts the session cookie set by a response.
func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == modules.CookieName && ck.Value != "" {
			return ck
		}
	}
	t.Fatalf("no session cookie in response (status %d, body %s)", rec.Code, rec.Body.String())
	return nil
}

// login signs in with a password and returns the session cookie.
func login(t *testing.T, h http.Handler, username, password string) *http.Cookie {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/web/login", map[string]string{"username": username, "password": password}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: status %d, body %s", username, rec.Code, rec.Body.String())
	}
	return sessionCookie(t, rec)
}

func TestSPAAndPublicEndpoints(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	cases := []struct {
		path       string
		wantStatus int
		wantBody   string // substring
		wantCType  string // prefix
	}{
		{"/", http.StatusOK, "shell", "text/html"},
		{"/alerts/1", http.StatusOK, "shell", "text/html"},
		{"/assets/index-abc.css", http.StatusOK, "body{}", "text/css"},
		{"/manifest.webmanifest", http.StatusOK, "{}", "application/manifest+json"},
		{"/api/v1/health", http.StatusOK, `"version"`, "application/json"},
		{"/api/v1/setup", http.StatusOK, `"needs_setup":true`, "application/json"},
		{"/api/v1/me", http.StatusUnauthorized, `"unauthorized"`, "application/json"},
		{"/api/v1/nope", http.StatusUnauthorized, `"unauthorized"`, "application/json"},
	}
	for _, tc := range cases {
		rec := do(t, h, http.MethodGet, tc.path, nil, nil)
		if rec.Code != tc.wantStatus {
			t.Errorf("GET %s: status %d, want %d", tc.path, rec.Code, tc.wantStatus)
		}
		if !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("GET %s: body %q does not contain %q", tc.path, rec.Body.String(), tc.wantBody)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.wantCType) {
			t.Errorf("GET %s: Content-Type %q, want prefix %q", tc.path, got, tc.wantCType)
		}
	}
}

func TestSetupFlow(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()

	rec := do(t, h, http.MethodPost, "/web/setup", map[string]string{"username": "admin", "password": "short"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("setup with a short password: status %d, want 400", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/web/setup", map[string]string{"username": "admin", "display_name": "Admin", "password": "password123"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: status %d, body %s", rec.Code, rec.Body.String())
	}
	ck := sessionCookie(t, rec)

	rec = do(t, h, http.MethodGet, "/api/v1/setup", nil, nil)
	if !strings.Contains(rec.Body.String(), `"needs_setup":false`) {
		t.Errorf("setup status after setup: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/web/setup", map[string]string{"username": "again", "password": "password123"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("second setup: status %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, ck)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"admin"`) {
		t.Errorf("GET /api/v1/me with the setup cookie: status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"display_name":"Admin"`) {
		t.Errorf("display name not stored: %s", rec.Body.String())
	}
}

func TestLoginWithPasswordAndRememberMe(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	createUser(t, c.db, "alice", "Alice", "correct-horse", modules.RoleUser)

	rec := do(t, h, http.MethodPost, "/web/login", map[string]string{"username": "alice", "password": "wrong-horse"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/web/login", map[string]string{"username": "nobody", "password": "correct-horse"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown user: status %d, want 401", rec.Code)
	}
	// There is no token login: a token value is not a credential.
	tok, err := modules.CreateToken(c.db, "laptop", "", sql.NullTime{}, false)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	rec = do(t, h, http.MethodPost, "/web/login", map[string]string{"token": tok.Token}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("token login: status %d, want 400", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/web/login", map[string]string{"username": "alice", "password": tok.Token}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("token as password: status %d, want 401", rec.Code)
	}

	// Default login: a browser-session cookie (no Max-Age) valid for
	// SessionCookieTTLHours, not refreshed.
	rec = do(t, h, http.MethodPost, "/web/login", map[string]string{"username": "alice", "password": "correct-horse"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d, body %s", rec.Code, rec.Body.String())
	}
	ck := sessionCookie(t, rec)
	if ck.MaxAge != 0 || !ck.Expires.IsZero() {
		t.Errorf("session cookie is persistent: max-age %d expires %v", ck.MaxAge, ck.Expires)
	}
	sess, err := modules.ValidateSessionCookie(c.db, c.key, ck.Value)
	if err != nil || sess.Remember {
		t.Fatalf("session: %v %+v", err, sess)
	}
	if until := time.Until(sess.Expiry); until > time.Duration(modules.SessionCookieTTLHours)*time.Hour || until < 23*time.Hour {
		t.Errorf("browser-session expiry %v away", until)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, ck)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Errorf("GET /api/v1/me: status %d, body %s", rec.Code, rec.Body.String())
	}

	// Remember me: a persistent cookie with the configured TTL.
	rec = do(t, h, http.MethodPost, "/web/login", map[string]any{"username": "alice", "password": "correct-horse", "remember": true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remembered login: status %d", rec.Code)
	}
	ck2 := sessionCookie(t, rec)
	if ck2.MaxAge != c.cookieTTLHours*3600 {
		t.Errorf("remembered cookie max-age %d, want %d", ck2.MaxAge, c.cookieTTLHours*3600)
	}
	sess, err = modules.ValidateSessionCookie(c.db, c.key, ck2.Value)
	if err != nil || !sess.Remember {
		t.Fatalf("remembered session: %v %+v", err, sess)
	}
	if until := time.Until(sess.Expiry); until < time.Duration(c.cookieTTLHours)*time.Hour-time.Hour {
		t.Errorf("remembered expiry only %v away", until)
	}
	// Only JSON is accepted: a form-encoded login (what a cross-site HTML
	// form could send) is refused before the credentials are looked at.
	form := httptest.NewRequest(http.MethodPost, "/web/login", strings.NewReader("username=alice&password=correct-horse&remember=1"))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form.RemoteAddr = testRemoteAddr
	formRec := httptest.NewRecorder()
	h.ServeHTTP(formRec, form)
	if formRec.Code != http.StatusUnsupportedMediaType || len(formRec.Result().Cookies()) != 0 {
		t.Errorf("form login: status %d, want 415 and no cookie", formRec.Code)
	}

	// Logout clears the cookie; a password change invalidates old sessions
	// and re-issues the cookie in the mode of the current session.
	rec = do(t, h, http.MethodPost, "/web/logout", nil, ck)
	if rec.Code != http.StatusOK {
		t.Errorf("logout: status %d", rec.Code)
	}
	rec = do(t, h, http.MethodPut, "/api/v1/me/password", map[string]string{"current_password": "correct-horse", "new_password": "battery-staple"}, ck)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: status %d, body %s", rec.Code, rec.Body.String())
	}
	if fresh := sessionCookie(t, rec); fresh.MaxAge != 0 {
		t.Errorf("re-issued cookie became persistent: max-age %d", fresh.MaxAge)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, ck2)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("old session after password change: status %d, want 401", rec.Code)
	}
	login(t, h, "alice", "battery-staple")
}

func TestAdminOnlyEndpoints(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	createUser(t, c.db, "admin", "", "password123", modules.RoleAdmin)
	createUser(t, c.db, "bob", "", "password123", modules.RoleUser)
	admin := login(t, h, "admin", "password123")
	user := login(t, h, "bob", "password123")

	rec := do(t, h, http.MethodGet, "/api/v1/users", nil, user)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"forbidden"`) {
		t.Errorf("GET /api/v1/users as user: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v1/users", nil, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"bob"`) {
		t.Errorf("GET /api/v1/users as admin: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/settings", map[string]any{"check_times": []string{"6:00"}}, user)
	if rec.Code != http.StatusForbidden {
		t.Errorf("PUT /api/v1/settings as user: status %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodPut, "/api/v1/settings", map[string]any{"check_times": []string{"6:00", "18:30", "06:00"}}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"check_times":["06:00","18:30"]`) {
		t.Errorf("PUT /api/v1/settings as admin: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v1/settings", nil, user)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/settings as user: status %d", rec.Code)
	}
	// The user role is read-only: every job kind needs an administrator.
	for _, kind := range []string{"reindex", "sync", "fetch", "group", "analyze", "notify"} {
		rec = do(t, h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": kind}, user)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s job as user: status %d, want 403", kind, rec.Code)
		}
	}
	rec = do(t, h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "sync"}, admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"requested_by":"web:admin"`) {
		t.Errorf("POST sync job as admin: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v1/jobs", nil, user)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/jobs as user: status %d", rec.Code)
	}
	// The last administrator can neither be demoted nor deleted.
	var users struct {
		Users []UserDTO `json:"users"`
	}
	rec = do(t, h, http.MethodGet, "/api/v1/users", nil, admin)
	_ = json.Unmarshal(rec.Body.Bytes(), &users)
	var adminID int64
	for _, u := range users.Users {
		if u.Username == "admin" {
			adminID = u.ID
		}
	}
	rec = do(t, h, http.MethodPut, "/api/v1/users/"+itoa(adminID), map[string]string{"role": "user"}, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("demote last admin: status %d, want 400", rec.Code)
	}
	rec = do(t, h, http.MethodDelete, "/api/v1/users/"+itoa(adminID), nil, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("delete self: status %d, want 400", rec.Code)
	}
}

func TestTokensAreAdminOnly(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	createUser(t, c.db, "admin", "", "password123", modules.RoleAdmin)
	createUser(t, c.db, "bob", "", "password123", modules.RoleUser)
	admin := login(t, h, "admin", "password123")
	user := login(t, h, "bob", "password123")

	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/tokens"}, {http.MethodPost, "/api/v1/tokens"}, {http.MethodDelete, "/api/v1/tokens/default"},
	} {
		if rec := do(t, h, req.method, req.path, map[string]any{"name": "phone"}, user); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as user: status %d, want 403", req.method, req.path, rec.Code)
		}
	}
	rec := do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "phone"}, admin)
	// The first token into an empty table is the default one.
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"value":"mlc_`) || !strings.Contains(rec.Body.String(), `"is_default":true`) {
		t.Fatalf("create token: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Token TokenDTO `json:"token"`
		Value string   `json:"value"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if createdAt, err := time.Parse(time.RFC3339, created.Token.CreatedAt); err != nil || time.Since(createdAt) > time.Minute || createdAt.IsZero() {
		t.Errorf("created_at of a new token = %q", created.Token.CreatedAt)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/tokens", nil, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"identifier":"phone"`) || strings.Contains(rec.Body.String(), created.Value) ||
		strings.Contains(rec.Body.String(), `"user`) {
		t.Errorf("list tokens: status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/tokens/nope", nil, admin); rec.Code != http.StatusNotFound {
		t.Errorf("delete unknown token: status %d", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/tokens/"+created.Token.Identifier, nil, admin); rec.Code != http.StatusOK {
		t.Errorf("delete token: status %d", rec.Code)
	}
}

func TestDefaultToken(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	// Setup does not create a token; only the server start does.
	rec := do(t, h, http.MethodPost, "/web/setup", map[string]string{"username": "admin", "password": "password123"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	if n, _ := models.CountTokens(c.db); n != 0 {
		t.Fatalf("tokens after setup: %d", n)
	}
	// No default token: "default" is created (what the server start does).
	tok, promoted, err := modules.EnsureDefaultToken(c.db)
	if err != nil || promoted || tok == nil || !tok.IsDefault || tok.Identifier != "default" || tok.Name != "default" ||
		tok.ExpiresAt.Valid || !strings.HasPrefix(tok.Token, modules.TokenPrefix) {
		t.Fatalf("default token: %+v %v %v", tok, promoted, err)
	}
	if stored, err := models.GetDefaultToken(c.db); err != nil || stored.ID != tok.ID {
		t.Errorf("stored default: %+v %v", stored, err)
	}
	// With a default present nothing happens.
	if again, promoted, err := modules.EnsureDefaultToken(c.db); err != nil || promoted || again != nil {
		t.Errorf("touched with a default present: %+v %v %v", again, promoted, err)
	}
	// Deleting the default never promotes another token; the next start
	// creates "default" again even though other tokens exist.
	admin := login(t, h, "admin", "password123")
	if rec := do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "other"}, admin); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/tokens/default", nil, admin); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, err := models.GetDefaultToken(c.db); err != sql.ErrNoRows {
		t.Errorf("another token was promoted to default: %v", err)
	}
	if fresh, promoted, err := modules.EnsureDefaultToken(c.db); err != nil || promoted || fresh == nil || !fresh.IsDefault {
		t.Errorf("recreated next to other tokens: %+v %v %v", fresh, promoted, err)
	}
	if n, _ := models.CountTokens(c.db); n != 2 {
		t.Errorf("tokens: %d, want 2", n)
	}
	// A token "default" that lost the flag is promoted instead of duplicated.
	if err := models.SetDefaultToken(c.db, "other"); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/tokens/other", nil, admin); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if fresh, promoted, err := modules.EnsureDefaultToken(c.db); err != nil || !promoted || fresh != nil {
		t.Errorf("promotion: %+v %v %v", fresh, promoted, err)
	}
	if def, err := models.GetDefaultToken(c.db); err != nil || def.Identifier != "default" {
		t.Errorf("\"default\" not promoted: %+v %v", def, err)
	}
	// The first token created into an empty table becomes the default.
	if rec := do(t, h, http.MethodDelete, "/api/v1/tokens/default", nil, admin); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{"identifier": "ci", "expires": "720h"}, admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"is_default":true`) || !strings.Contains(rec.Body.String(), `"name":"ci"`) {
		t.Errorf("first token into an empty table: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{"identifier": "ci"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate identifier: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{"identifier": "bad id"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid identifier: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("no name and no identifier: %d", rec.Code)
	}
}

func TestMailboxesRequireAdminForWrites(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	createUser(t, c.db, "admin", "", "password123", modules.RoleAdmin)
	createUser(t, c.db, "bob", "", "password123", modules.RoleUser)
	admin := login(t, h, "admin", "password123")
	user := login(t, h, "bob", "password123")
	input := map[string]any{"address": "Bounce@Example.com", "imap_host": "imap.example.com", "imap_username": "bounce", "imap_password": "secret"}

	rec := do(t, h, http.MethodPost, "/api/v1/mailboxes", input, user)
	if rec.Code != http.StatusForbidden {
		t.Errorf("create mailbox as user: status %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/mailboxes", input, admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"address":"bounce@example.com"`) {
		t.Fatalf("create mailbox as admin: status %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("password leaked in the response: %s", rec.Body.String())
	}
	mb, err := models.GetMailboxByAddress(c.db, "bounce@example.com")
	if err != nil {
		t.Fatalf("mailbox not stored: %v", err)
	}
	if pw, err := modules.MailboxPassword(c.key, mb); err != nil || pw != "secret" {
		t.Errorf("stored password does not round-trip: %q, %v", pw, err)
	}
	if mb.ImapPort != modules.DefaultIMAPPort || mb.Folder != "INBOX" || mb.InitialDays != modules.DefaultInitialDays {
		t.Errorf("defaults not applied: port %d folder %s initial %d", mb.ImapPort, mb.Folder, mb.InitialDays)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/mailboxes", input, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate mailbox: status %d, want 400", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/mailboxes", nil, user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"address":"bounce@example.com"`) || strings.Contains(rec.Body.String(), `"imap_username":"bounce"`) {
		t.Errorf("list mailboxes as user (connection settings are admin only): status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/v1/mailboxes", nil, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"imap_username":"bounce"`) {
		t.Errorf("list mailboxes as admin: status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/mailboxes/"+itoa(mb.ID)+"/sync", nil, user); rec.Code != http.StatusForbidden {
		t.Errorf("queue sync as user: status %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/mailboxes/"+itoa(mb.ID)+"/sync", nil, admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"mailbox_address":"bounce@example.com"`) ||
		!strings.Contains(rec.Body.String(), `"kind":"sync"`) {
		t.Errorf("queue sync as admin: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/mailboxes/"+itoa(mb.ID)+"/sync", nil, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"created":false`) {
		t.Errorf("duplicate sync suppressed: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodDelete, "/api/v1/mailboxes/"+itoa(mb.ID)+"?keep_data=1", nil, admin)
	if rec.Code != http.StatusOK {
		t.Errorf("delete mailbox: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestControlEndpointsAreLoopbackOnly(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()

	req := httptest.NewRequest(http.MethodGet, "/control/status", nil)
	req.RemoteAddr = "192.168.1.5:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("remote client: status %d, want %d", rec.Code, http.StatusForbidden)
	}
	for _, addr := range []string{"127.0.0.1:40000", "[::1]:40000"} {
		req = httptest.NewRequest(http.MethodGet, "/control/status", nil)
		req.RemoteAddr = addr
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("loopback client %s: status %d, want %d", addr, rec.Code, http.StatusOK)
		}
		for _, want := range []string{`"name":"mailcare"`, `"web_listen":"127.0.0.1:9790"`, `"users":0`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("loopback client: body %q does not contain %s", rec.Body.String(), want)
			}
		}
		// The status names the data directory served, so a CLI of another
		// data directory can tell this server is not its own.
		var st modules.ServerStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatalf("status body: %v", err)
		}
		if st.DataDir != c.dataDir || !filepath.IsAbs(st.DataDir) {
			t.Errorf("status data_dir = %q, want the absolute %q", st.DataDir, c.dataDir)
		}
		if !st.ServesDataDir(c.dataDir) || st.ServesDataDir(filepath.Join(c.dataDir, "other")) {
			t.Errorf("ServesDataDir does not single out %q", c.dataDir)
		}
	}

	// Jobs can be queued and read back over the control endpoints.
	req = httptest.NewRequest(http.MethodPost, "/control/jobs", strings.NewReader(`{"kind":"sync"}`))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"requested_by":"cli"`) {
		t.Fatalf("control job: status %d, body %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Job JobDTO `json:"job"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	req = httptest.NewRequest(http.MethodGet, "/control/jobs/"+itoa(env.Job.ID), nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
		t.Errorf("control job read: status %d, body %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/control/jobs", strings.NewReader(`{"kind":"analyze","target":"0123456789abcdef"}`))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("analyze of one group without a mailbox: status %d, want 400", rec.Code)
	}
	// The control POSTs need the JSON content type like every other write
	// (the CLI sends it).
	req = httptest.NewRequest(http.MethodPost, "/control/jobs", strings.NewReader(`{"kind":"sync"}`))
	req.RemoteAddr = "127.0.0.1:40000"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("control job without a content type: status %d, want 415", rec.Code)
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
