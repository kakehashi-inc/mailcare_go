package workers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

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
	key, err := modules.LoadSecretKey()
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

// do performs a request against the handler, optionally with a JSON body
// and a session cookie.
func do(t *testing.T, h http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
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
	req.RemoteAddr = "192.168.1.5:40000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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

func TestLoginWithPasswordAndToken(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	u, err := modules.CreateUser(c.db, "alice", "Alice", "correct-horse", modules.RoleUser)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/web/login", map[string]string{"username": "alice", "password": "wrong-horse"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d, want 401", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/web/login", map[string]string{"username": "nobody", "password": "correct-horse"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown user: status %d, want 401", rec.Code)
	}
	ck := login(t, h, "alice", "correct-horse")
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, ck)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Errorf("GET /api/v1/me: status %d, body %s", rec.Code, rec.Body.String())
	}

	// Token login.
	tok, err := modules.CreateLoginToken(c.db, u.ID, "laptop", "", sql.NullTime{})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	rec = do(t, h, http.MethodPost, "/web/login", map[string]string{"token": "mlc_0000"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token: status %d, want 401", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/web/login", map[string]string{"token": tok.Token}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("token login: status %d, body %s", rec.Code, rec.Body.String())
	}
	ck2 := sessionCookie(t, rec)
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, ck2)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/me with the token session: status %d", rec.Code)
	}
	stored, err := models.GetTokenByIdentifier(c.db, tok.Identifier)
	if err != nil || !stored.LastUsedAt.Valid {
		t.Errorf("token last_used_at not recorded (err %v)", err)
	}

	// Logout clears the cookie; a password change invalidates old sessions.
	rec = do(t, h, http.MethodPost, "/web/logout", nil, ck)
	if rec.Code != http.StatusOK {
		t.Errorf("logout: status %d", rec.Code)
	}
	rec = do(t, h, http.MethodPut, "/api/v1/me/password", map[string]string{"current_password": "correct-horse", "new_password": "battery-staple"}, ck)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: status %d, body %s", rec.Code, rec.Body.String())
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
	if _, err := modules.CreateUser(c.db, "admin", "", "password123", modules.RoleAdmin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := modules.CreateUser(c.db, "bob", "", "password123", modules.RoleUser); err != nil {
		t.Fatalf("create user: %v", err)
	}
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
	// The rebuild jobs need an administrator; a sync job does not.
	rec = do(t, h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "reindex"}, user)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST reindex job as user: status %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "sync"}, user)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"requested_by":"web:bob"`) {
		t.Errorf("POST sync job as user: status %d, body %s", rec.Code, rec.Body.String())
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

func TestTokensAreScopedToTheUser(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	adminUser, _ := modules.CreateUser(c.db, "admin", "", "password123", modules.RoleAdmin)
	if _, err := modules.CreateUser(c.db, "bob", "", "password123", modules.RoleUser); err != nil {
		t.Fatalf("create user: %v", err)
	}
	admin := login(t, h, "admin", "password123")
	user := login(t, h, "bob", "password123")

	rec := do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "phone"}, user)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"value":"mlc_`) {
		t.Fatalf("create own token: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "sneaky", "user_id": adminUser.ID}, user)
	if rec.Code != http.StatusForbidden {
		t.Errorf("create token for another user: status %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/tokens?user_id="+itoa(adminUser.ID), nil, user)
	if rec.Code != http.StatusForbidden {
		t.Errorf("list another user's tokens: status %d, want 403", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/tokens", nil, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"bob"`) {
		t.Errorf("admin lists every token: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestMailboxesRequireAdminForWrites(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	if _, err := modules.CreateUser(c.db, "admin", "", "password123", modules.RoleAdmin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := modules.CreateUser(c.db, "bob", "", "password123", modules.RoleUser); err != nil {
		t.Fatalf("create user: %v", err)
	}
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
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"imap_username":"bounce"`) {
		t.Errorf("list mailboxes as user: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/mailboxes/"+itoa(mb.ID)+"/sync", nil, user)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"mailbox_address":"bounce@example.com"`) ||
		!strings.Contains(rec.Body.String(), `"kind":"sync"`) {
		t.Errorf("queue sync as user: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/mailboxes/"+itoa(mb.ID)+"/sync", nil, user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"created":false`) {
		t.Errorf("duplicate sync suppressed: status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/mailboxes/"+itoa(mb.ID)+"/check", nil, user); rec.Code != http.StatusNotFound {
		t.Errorf("the old check route should be gone: status %d", rec.Code)
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
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("analyze of one group without a mailbox: status %d, want 400", rec.Code)
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
