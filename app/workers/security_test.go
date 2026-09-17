package workers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

func TestSetupCreatesOneAdministratorUnderConcurrency(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	const attempts = 6
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rec := do(t, h, http.MethodPost, "/web/setup", map[string]string{"username": "admin" + itoa(int64(i)), "password": "password123"}, nil)
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	created, refused := 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusForbidden:
			refused++
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	if created != 1 || refused != attempts-1 {
		t.Errorf("created %d, refused %d (want 1 / %d): %v", created, refused, attempts-1, codes)
	}
	if n, _ := models.CountUsers(c.db); n != 1 {
		t.Errorf("%d users after the concurrent setup, want 1", n)
	}
}

func TestStateChangingRequestsNeedJSONAndSameOrigin(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	createUser(t, c.db, "alice", "", "correct-horse", modules.RoleUser)
	creds := map[string]string{"username": "alice", "password": "correct-horse"}
	loginWith := func(mutate func(r *http.Request)) int {
		req := newRequest(t, http.MethodPost, "/web/login", creds)
		mutate(req)
		return serve(h, req).Code
	}
	// Content-Type: application/json is required (parameters are fine);
	// anything an HTML form can send is refused before the handler runs.
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonx"} {
		if code := loginWith(func(r *http.Request) { r.Header.Set("Content-Type", ct) }); code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: %d, want 415", ct, code)
		}
	}
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		if code := loginWith(func(r *http.Request) { r.Header.Set("Content-Type", ct) }); code != http.StatusOK {
			t.Errorf("Content-Type %q: %d, want 200", ct, code)
		}
	}
	// Origin must name the request host; the scheme and a default port do
	// not matter (a reverse proxy may terminate TLS).
	for _, origin := range []string{"http://evil.example", "http://example.com.evil.example", "null", "http://example.com:8443", "file://"} {
		if code := loginWith(func(r *http.Request) { r.Host = "example.com"; r.Header.Set("Origin", origin) }); code != http.StatusForbidden {
			t.Errorf("Origin %q: %d, want 403", origin, code)
		}
	}
	for host, origins := range map[string][]string{
		"example.com":      {"http://example.com", "https://example.com", "https://EXAMPLE.com:443", "http://example.com:80"},
		"example.com:9790": {"http://example.com:9790", "https://example.com:9790"},
		"[::1]:9790":       {"http://[::1]:9790"},
	} {
		for _, origin := range origins {
			if code := loginWith(func(r *http.Request) { r.Host = host; r.Header.Set("Origin", origin) }); code != http.StatusOK {
				t.Errorf("Host %q Origin %q: %d, want 200", host, origin, code)
			}
		}
	}
	// Without Origin the Sec-Fetch-Site header decides.
	for _, site := range []string{"cross-site", "same-site", "Cross-Site"} {
		if code := loginWith(func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", site) }); code != http.StatusForbidden {
			t.Errorf("Sec-Fetch-Site %q: %d, want 403", site, code)
		}
	}
	for _, site := range []string{"same-origin", "none", ""} {
		if code := loginWith(func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", site) }); code != http.StatusOK {
			t.Errorf("Sec-Fetch-Site %q: %d, want 200", site, code)
		}
	}
	// When Origin is present it alone decides (a browser never pairs a
	// matching Origin with a cross-site fetch).
	if code := loginWith(func(r *http.Request) {
		r.Host = "example.com"
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	}); code != http.StatusOK {
		t.Errorf("matching Origin with Sec-Fetch-Site: %d, want 200", code)
	}
	if code := loginWith(func(r *http.Request) {
		r.Host = "example.com"
		r.Header.Set("Origin", "http://evil.example")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}); code != http.StatusForbidden {
		t.Errorf("mismatching Origin with same-origin: %d, want 403", code)
	}
	// The same rules cover every write on the API and the control plane,
	// including bodiless ones; reads are untouched.
	cookie := login(t, h, "alice", "correct-horse")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/web/logout"}, {http.MethodPut, "/api/v1/me/profile"}, {http.MethodDelete, "/api/v1/jobs/1"},
		{http.MethodPost, "/api/v1/jobs"}, {http.MethodPost, "/control/shutdown"},
	} {
		req := newRequest(t, tc.method, tc.path, nil)
		req.Header.Del("Content-Type")
		req.AddCookie(cookie)
		if tc.path == "/control/shutdown" {
			req.RemoteAddr = "127.0.0.1:40000"
		}
		if rec := serve(h, req); rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s %s without a content type: %d, want 415", tc.method, tc.path, rec.Code)
		}
		req = newRequest(t, tc.method, tc.path, nil)
		req.Header.Set("Origin", "http://evil.example")
		req.AddCookie(cookie)
		if rec := serve(h, req); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s from another origin: %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	req := newRequest(t, http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(cookie)
	if rec := serve(h, req); rec.Code != http.StatusOK {
		t.Errorf("GET is not subject to the write checks: %d", rec.Code)
	}
	// Static files and the SPA shell are not subject to the checks either.
	req = newRequest(t, http.MethodPost, "/alerts/1", nil)
	req.Header.Del("Content-Type")
	if rec := serve(h, req); rec.Code == http.StatusUnsupportedMediaType {
		t.Errorf("SPA path subject to the content type check: %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := newSeededCore(t)
	for _, tc := range []struct {
		method, path string
		cookie       *http.Cookie
		wantFrame    string
	}{
		{http.MethodGet, "/", nil, "DENY"},
		{http.MethodGet, "/alerts/1", nil, "DENY"},
		{http.MethodGet, "/assets/index-abc.css", nil, "DENY"},
		{http.MethodGet, "/api/v1/health", nil, "DENY"},
		{http.MethodGet, "/api/v1/me", nil, "DENY"},
		{http.MethodGet, "/api/v1/me", s.user, "DENY"},
		{http.MethodPost, "/api/v1/jobs", s.user, "DENY"},
		{http.MethodGet, "/control/status", nil, "DENY"},
		{http.MethodGet, s.path("/messages/" + s.keys[0] + "/raw"), s.user, "DENY"},
		{http.MethodGet, s.path("/messages/" + s.keys[0] + "/html"), s.user, "SAMEORIGIN"},
	} {
		rec := do(t, s.h, tc.method, tc.path, nil, tc.cookie)
		h := rec.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != tc.wantFrame || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s %s (%d): headers %v", tc.method, tc.path, rec.Code, h)
		}
	}
	// The message HTML keeps its own policy headers next to the common ones.
	rec := do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html"), nil, s.user)
	if rec.Code == http.StatusOK && rec.Header().Get("Content-Security-Policy") != htmlCSP {
		t.Errorf("message HTML lost its CSP: %v", rec.Header())
	}
}

func TestLoginThrottle(t *testing.T) {
	c := newTestCore(t)
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	c.logins = modules.NewLoginLimiterWithClock(func() time.Time { return now })
	h := c.webHandler()
	createUser(t, c.db, "alice", "", "correct-horse", modules.RoleUser)
	attempt := func(username, password, addr string) *httptest.ResponseRecorder {
		req := newRequest(t, http.MethodPost, "/web/login", map[string]string{"username": username, "password": password})
		req.RemoteAddr = addr
		return serve(h, req)
	}
	other := "192.168.1.6:40000"
	// The first LoginFailuresBeforeLock failures are ordinary 401s.
	for i := 0; i < modules.LoginFailuresBeforeLock; i++ {
		if rec := attempt("alice", "wrong", testRemoteAddr); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// Now the pair must wait, even with the right password; other users of
	// the same client and the same user from another client are not held.
	rec := attempt("alice", "correct-horse", testRemoteAddr)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" || !strings.Contains(rec.Body.String(), "try again in 30 seconds") {
		t.Fatalf("during the wait: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a refused attempt must not issue a cookie")
	}
	if rec := attempt("bob", "wrong", testRemoteAddr); rec.Code != http.StatusUnauthorized {
		t.Errorf("another username from the same client: %d", rec.Code)
	}
	if rec := attempt("alice", "correct-horse", other); rec.Code != http.StatusOK {
		t.Errorf("the same user from another client: %d", rec.Code)
	}
	if rec := attempt("ALICE ", "wrong", testRemoteAddr); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the username is matched case-insensitively and trimmed: %d", rec.Code)
	}
	// After the wait a failure doubles it (30s -> 60s); a success clears it.
	now = now.Add(31 * time.Second)
	if rec := attempt("alice", "wrong", testRemoteAddr); rec.Code != http.StatusUnauthorized {
		t.Errorf("after the wait: %d", rec.Code)
	}
	rec = attempt("alice", "correct-horse", testRemoteAddr)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Errorf("doubled wait: %d %v", rec.Code, rec.Header())
	}
	now = now.Add(61 * time.Second)
	if rec := attempt("alice", "correct-horse", testRemoteAddr); rec.Code != http.StatusOK {
		t.Fatalf("login after the wait: %d %s", rec.Code, rec.Body.String())
	}
	for i := 0; i < modules.LoginFailuresBeforeLock-1; i++ {
		if rec := attempt("alice", "wrong", testRemoteAddr); rec.Code != http.StatusUnauthorized {
			t.Errorf("failure %d after a success: %d", i+1, rec.Code)
		}
	}
	if rec := attempt("alice", "correct-horse", testRemoteAddr); rec.Code != http.StatusOK {
		t.Errorf("a success resets the count: %d", rec.Code)
	}
}

func TestMembersDoNotSeeServerInternals(t *testing.T) {
	s := newSeededCore(t)
	// Settings: server internals for administrators only.
	rec := do(t, s.h, http.MethodGet, "/api/v1/settings", nil, s.user)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings as user: %d", rec.Code)
	}
	for _, key := range []string{`"data_dir"`, `"web_listen"`, `"web_port"`, `"workers"`} {
		if strings.Contains(rec.Body.String(), key) {
			t.Errorf("settings for a user contain %s: %s", key, rec.Body.String())
		}
	}
	for _, key := range []string{`"check_times"`, `"agent_provider"`, `"mail_keep_days"`, `"server_timezone"`, `"providers"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("settings for a user lack %s: %s", key, rec.Body.String())
		}
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/settings", nil, s.admin)
	for _, want := range []string{`"data_dir":"`, `"web_listen":"127.0.0.1"`, `"web_port":`, `"workers":`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("settings for an admin lack %s: %s", want, rec.Body.String())
		}
	}
	// Mailboxes: the IMAP connection settings and fetch ranges are blanked
	// for users on the list, the single mailbox and the dashboard.
	redacted := func(body string) bool {
		return strings.Contains(body, `"imap_host":""`) && strings.Contains(body, `"imap_port":0`) && strings.Contains(body, `"imap_security":""`) &&
			strings.Contains(body, `"imap_username":""`) && strings.Contains(body, `"folder":""`) && strings.Contains(body, `"initial_days":0`) &&
			strings.Contains(body, `"recent_days":0`) && !strings.Contains(body, "imap.example.test")
	}
	full := func(body string) bool {
		return strings.Contains(body, `"imap_host":"imap.example.test"`) && strings.Contains(body, `"imap_username":"u"`) &&
			strings.Contains(body, `"folder":"INBOX"`) && strings.Contains(body, `"initial_days":`+itoa(int64(modules.DefaultInitialDays)))
	}
	for _, path := range []string{"/api/v1/mailboxes", "/api/v1/mailboxes?stats=1", "/api/v1/mailboxes/" + itoa(s.mb.ID), "/api/v1/dashboard"} {
		rec := do(t, s.h, http.MethodGet, path, nil, s.user)
		if rec.Code != http.StatusOK || !redacted(rec.Body.String()) {
			t.Errorf("%s as user: %d %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"address":"ops@example.test"`) || !strings.Contains(rec.Body.String(), `"enabled":true`) {
			t.Errorf("%s as user lacks the public fields: %s", path, rec.Body.String())
		}
		rec = do(t, s.h, http.MethodGet, path, nil, s.admin)
		if rec.Code != http.StatusOK || !full(rec.Body.String()) {
			t.Errorf("%s as admin: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
