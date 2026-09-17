package workers

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/smtptest"
)

func TestMeEmailAndUserEmailEndpoints(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	createUser(t, c.db, "admin", "", "password123", modules.RoleAdmin)
	admin := login(t, h, "admin", "password123")
	// Own address: set (normalized), reject invalid, clear.
	rec := do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"email": " Admin@Example.test "}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"admin@example.test"`) {
		t.Errorf("set own email: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"email": "nope"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid own email: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, admin)
	if !strings.Contains(rec.Body.String(), `"email":"admin@example.test"`) {
		t.Errorf("me lacks the email: %s", rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"email": ""}, admin); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":""`) {
		t.Errorf("clear own email: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"email": "x@example.test"}, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	// Users: create with an address, update it, reject invalid ones.
	rec = do(t, h, http.MethodPost, "/api/v1/users", map[string]string{"username": "carol", "password": "password123", "role": "user", "email": "Carol@Example.test"}, admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"email":"carol@example.test"`) {
		t.Fatalf("create user with email: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		User UserDTO `json:"user"`
	}
	decode(t, rec.Body.Bytes(), &env)
	if rec := do(t, h, http.MethodPost, "/api/v1/users", map[string]string{"username": "dave", "password": "password123", "email": "bad"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("create user with a bad email: %d", rec.Code)
	}
	rec = do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"display_name": "Carol"}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"carol@example.test"`) {
		t.Errorf("update without email must keep it: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"email": ""}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":""`) {
		t.Errorf("clear user email: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"email": "no"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("bad user email: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/users", nil, admin)
	if !strings.Contains(rec.Body.String(), `"email":`) {
		t.Errorf("user list lacks emails: %s", rec.Body.String())
	}
	// Timezone: default, own change, invalid, per-user update, settings zone.
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, admin)
	if !strings.Contains(rec.Body.String(), `"timezone":"`+models.DefaultTimezone+`"`) {
		t.Errorf("me lacks the default timezone: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"timezone": " UTC "}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"timezone":"UTC"`) {
		t.Errorf("set own timezone: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"Mars/Olympus", "Local", "JST+9"} {
		if rec := do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"timezone": bad}, admin); rec.Code != http.StatusBadRequest {
			t.Errorf("timezone %q: %d, want 400", bad, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"timezone": ""}, admin); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"timezone":"`+models.DefaultTimezone+`"`) {
		t.Errorf("empty timezone restores the default: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"timezone": "Europe/Berlin"}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"timezone":"Europe/Berlin"`) {
		t.Errorf("update user timezone: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"display_name": "Carol"}, admin)
	if !strings.Contains(rec.Body.String(), `"timezone":"Europe/Berlin"`) {
		t.Errorf("update without timezone must keep it: %s", rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"timezone": "Nowhere"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("bad user timezone: %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/users", map[string]string{"username": "dave", "password": "password123", "timezone": "America/New_York"}, admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"timezone":"America/New_York"`) {
		t.Errorf("create user with timezone: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/users", map[string]string{"username": "erin", "password": "password123", "timezone": "Local"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("create user with a bad timezone: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/settings", nil, admin)
	var settings struct {
		ServerTimezone string `json:"server_timezone"`
	}
	decode(t, rec.Body.Bytes(), &settings)
	if settings.ServerTimezone == "" || settings.ServerTimezone == "Local" || settings.ServerTimezone != modules.ServerTimezone() {
		t.Errorf("server_timezone = %q", settings.ServerTimezone)
	}
	// Language and theme, display name, and several fields at once.
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, admin)
	if !strings.Contains(rec.Body.String(), `"language":"`+models.DefaultLanguage+`"`) || !strings.Contains(rec.Body.String(), `"theme":"`+models.DefaultTheme+`"`) {
		t.Errorf("me lacks the default language/theme: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"language": "EN", "theme": "Dark", "display_name": " Root "}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"language":"en"`) || !strings.Contains(rec.Body.String(), `"theme":"dark"`) ||
		!strings.Contains(rec.Body.String(), `"display_name":"Root"`) {
		t.Errorf("profile language/theme/name: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []map[string]string{{"language": "fr"}, {"theme": "blue"}, {"display_name": strings.Repeat("x", 129)}} {
		if rec := do(t, h, http.MethodPut, "/api/v1/me/profile", bad, admin); rec.Code != http.StatusBadRequest {
			t.Errorf("profile %v: %d, want 400", bad, rec.Code)
		}
	}
	rec = do(t, h, http.MethodPut, "/api/v1/me/profile", map[string]string{"language": "", "theme": "", "display_name": ""}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"language":"ja"`) || !strings.Contains(rec.Body.String(), `"theme":"auto"`) ||
		!strings.Contains(rec.Body.String(), `"display_name":"Root"`) {
		t.Errorf("empty preferences restore defaults, empty name keeps it: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"language": "en", "theme": "light"}, admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"language":"en"`) || !strings.Contains(rec.Body.String(), `"theme":"light"`) ||
		!strings.Contains(rec.Body.String(), `"timezone":"Europe/Berlin"`) {
		t.Errorf("update user language/theme: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/users/"+itoa(env.User.ID), map[string]any{"theme": "neon"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("bad user theme: %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/users", map[string]string{"username": "frank", "password": "password123", "language": "en", "theme": "dark"}, admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"language":"en"`) || !strings.Contains(rec.Body.String(), `"theme":"dark"`) {
		t.Errorf("create user with language/theme: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/users", map[string]string{"username": "gina", "password": "password123", "language": "de"}, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("create user with a bad language: %d", rec.Code)
	}
}

func TestSetupAcceptsProfileFields(t *testing.T) {
	c := newTestCore(t)
	h := c.webHandler()
	if rec := do(t, h, http.MethodPost, "/web/setup", map[string]string{"username": "admin", "password": "password123", "timezone": "Nowhere/City"}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("setup with a bad timezone: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/web/setup", map[string]string{"username": "admin", "display_name": "Admin", "password": "password123",
		"email": "Admin@Example.test", "language": "en", "timezone": "UTC", "theme": "light"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"email":"admin@example.test"`, `"language":"en"`, `"timezone":"UTC"`, `"theme":"light"`, `"display_name":"Admin"`, `"role":"admin"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("setup response lacks %s: %s", want, rec.Body.String())
		}
	}
	rec = do(t, h, http.MethodGet, "/api/v1/me", nil, sessionCookie(t, rec))
	if !strings.Contains(rec.Body.String(), `"timezone":"UTC"`) {
		t.Errorf("me after setup: %s", rec.Body.String())
	}
}

func TestNotificationSettingsEndpoints(t *testing.T) {
	modules.TemplatesFS = os.DirFS("../..")
	s := newSeededCore(t)
	// Admin only.
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/settings/notifications"}, {http.MethodPut, "/api/v1/settings/notifications"},
		{http.MethodPost, "/api/v1/notifications/test"}, {http.MethodPost, "/api/v1/notifications/send"},
	} {
		if rec := do(t, s.h, req.method, req.path, map[string]any{}, s.user); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as user: %d", req.method, req.path, rec.Code)
		}
		if rec := do(t, s.h, req.method, req.path, map[string]any{}, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous: %d", req.method, req.path, rec.Code)
		}
	}
	// Defaults and the recipient picker (every user, with their address).
	rec := do(t, s.h, http.MethodGet, "/api/v1/settings/notifications", nil, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var dto NotificationSettingsDTO
	decode(t, rec.Body.Bytes(), &dto)
	if dto.SMTPPort != modules.DefaultSMTPPort || dto.SMTPSecurity != modules.DefaultSMTPSecurity || dto.NotifyEnabled ||
		dto.NotifyTime != modules.DefaultNotifyTime || dto.NotifyIntervalDays != 1 || len(dto.NotifyUserIDs) != 0 ||
		dto.SMTPPasswordSet || dto.LastSentAt != nil || dto.NextSendAt != nil || len(dto.Recipients) != 2 ||
		dto.EffectiveBaseURL != "http://127.0.0.1:9790" {
		t.Errorf("defaults %+v", dto)
	}
	if strings.Contains(rec.Body.String(), "smtp_password\"") {
		t.Errorf("password field in the response: %s", rec.Body.String())
	}
	// Update: the password is stored, reported as set and never returned.
	var adminID, bobID int64
	for _, r := range dto.Recipients {
		switch r.Username {
		case "admin":
			adminID = r.ID
		case "bob":
			bobID = r.ID
		}
	}
	srv, err := smtptest.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings/notifications", map[string]any{
		"smtp_host": srv.Host(), "smtp_port": srv.Port(), "smtp_security": "none", "smtp_username": "u", "smtp_password": "p",
		"smtp_from": "MailCare@Example.test", "public_base_url": "https://mailcare.example.test/", "notify_enabled": true,
		"notify_time": "8:15", "notify_interval_days": 2, "notify_user_ids": []int64{bobID, adminID},
	}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &dto)
	if dto.SMTPHost != srv.Host() || dto.SMTPPort != srv.Port() || dto.SMTPSecurity != "none" || dto.SMTPUsername != "u" ||
		!dto.SMTPPasswordSet || dto.SMTPFrom != "mailcare@example.test" || dto.PublicBaseURL != "https://mailcare.example.test" ||
		dto.EffectiveBaseURL != "https://mailcare.example.test" || !dto.NotifyEnabled || dto.NotifyTime != "08:15" ||
		dto.NotifyIntervalDays != 2 || len(dto.NotifyUserIDs) != 2 || dto.NextSendAt == nil {
		t.Errorf("after put %+v", dto)
	}
	if strings.Contains(rec.Body.String(), `"p"`) || strings.Contains(rec.Body.String(), "smtp_password\"") {
		t.Errorf("password leaked: %s", rec.Body.String())
	}
	// Partial update keeps the rest; an empty password keeps the stored one.
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings/notifications", map[string]any{"smtp_password": "", "notify_interval_days": 3}, s.admin)
	decode(t, rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusOK || !dto.SMTPPasswordSet || dto.NotifyIntervalDays != 3 || dto.SMTPHost != srv.Host() {
		t.Errorf("partial put: %d %+v", rec.Code, dto)
	}
	// While a password is stored, changing the host, port, security mode or
	// username needs the password in the same request; a refused update
	// saves nothing (the other field of the request included).
	for _, change := range []map[string]any{
		{"smtp_host": "other.example.test"}, {"smtp_port": srv.Port() + 1}, {"smtp_security": "starttls"}, {"smtp_username": "other"},
		{"smtp_username": "other", "smtp_password": ""},
	} {
		change["notify_interval_days"] = 5
		rec := do(t, s.h, http.MethodPut, "/api/v1/settings/notifications", change, s.admin)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "smtp_password is required when the connection settings change") {
			t.Errorf("put %v without the password: %d %s", change, rec.Code, rec.Body.String())
		}
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/settings/notifications", nil, s.admin)
	decode(t, rec.Body.Bytes(), &dto)
	if dto.SMTPHost != srv.Host() || dto.SMTPPort != srv.Port() || dto.SMTPSecurity != "none" || dto.SMTPUsername != "u" || dto.NotifyIntervalDays != 3 {
		t.Errorf("refused put changed the settings: %+v", dto)
	}
	// Repeating the saved values is not a change; with the password the
	// change goes through (and back).
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings/notifications", map[string]any{"smtp_host": srv.Host(), "smtp_port": srv.Port(), "smtp_security": "none", "smtp_username": "u"}, s.admin)
	if rec.Code != http.StatusOK {
		t.Errorf("put with the saved connection values: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings/notifications", map[string]any{"smtp_username": "other", "smtp_password": "p2"}, s.admin)
	decode(t, rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusOK || dto.SMTPUsername != "other" || !dto.SMTPPasswordSet {
		t.Errorf("put with the password: %d %+v", rec.Code, dto)
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings/notifications", map[string]any{"smtp_username": "u", "smtp_password": "p"}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("put back: %d %s", rec.Code, rec.Body.String())
	}
	// Validation errors.
	for _, bad := range []map[string]any{
		{"notify_interval_days": 8}, {"notify_time": "24:00"}, {"smtp_security": "tls"}, {"smtp_port": 0},
		{"smtp_from": "nope"}, {"public_base_url": "ftp://x"}, {"notify_user_ids": []int64{999}},
	} {
		if rec := do(t, s.h, http.MethodPut, "/api/v1/settings/notifications", bad, s.admin); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: %d, want 400", bad, rec.Code)
		}
	}
	// Test mail: to the given address, else the caller's own; SMTP errors are 400 with the text.
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/test", map[string]string{"to": "Ops@Example.test"}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("test mail: %d %s", rec.Code, rec.Body.String())
	}
	if msgs := srv.Messages(); len(msgs) != 1 || msgs[0].To[0] != "ops@example.test" || msgs[0].AuthUser != "u" {
		t.Errorf("test mail received: %+v", msgs)
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/test", nil, s.admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no recipient") {
		t.Errorf("test without an own address: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodPut, "/api/v1/me/profile", map[string]string{"email": "admin@example.test"}, s.admin); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/test", map[string]any{}, s.admin)
	if rec.Code != http.StatusOK || len(srv.Messages()) != 2 || srv.Messages()[1].To[0] != "admin@example.test" {
		t.Errorf("test to own address: %d %s (%d mails)", rec.Code, rec.Body.String(), len(srv.Messages()))
	}
	srv.Username, srv.Password = "u", "other"
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/test", map[string]string{"to": "ops@example.test"}, s.admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "authentication failed") {
		t.Errorf("test with a wrong password: %d %s", rec.Code, rec.Body.String())
	}
	srv.Username, srv.Password = "u", "p"
	// The stored password serves the saved server only: a test that changes
	// the host, port, security mode or username must carry its own password,
	// so the stored one is never relayed elsewhere.
	sent := len(srv.Messages())
	for _, change := range []map[string]any{
		{"smtp_host": "other.example.test"}, {"smtp_port": srv.Port() + 1}, {"smtp_security": "starttls"}, {"smtp_username": "other"},
		{"smtp_username": "other", "smtp_password": ""},
	} {
		body := map[string]any{"to": "ops@example.test"}
		for k, v := range change {
			body[k] = v
		}
		rec := do(t, s.h, http.MethodPost, "/api/v1/notifications/test", body, s.admin)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "smtp_password is required when the connection settings change") {
			t.Errorf("test with %v and no password: %d %s", change, rec.Code, rec.Body.String())
		}
	}
	if len(srv.Messages()) != sent {
		t.Errorf("a refused test must not send anything")
	}
	// With a password of its own the changed settings are tried with that
	// password (here it is wrong for the server, which proves the stored one
	// was not used).
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/test", map[string]any{"to": "ops@example.test", "smtp_username": "u", "smtp_password": "not-p", "smtp_from": "Other@Example.test"}, s.admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "authentication failed") {
		t.Errorf("test with an own password: %d %s", rec.Code, rec.Body.String())
	}
	// Repeating the saved values is the saved server: the stored password
	// applies, and a different sender is fine.
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/test", map[string]any{"to": "ops@example.test", "smtp_host": srv.Host(), "smtp_port": srv.Port(),
		"smtp_security": "none", "smtp_username": "u", "smtp_from": "Other@Example.test"}, s.admin)
	if rec.Code != http.StatusOK || len(srv.Messages()) != sent+1 || srv.Messages()[sent].From != "other@example.test" {
		t.Errorf("test with the saved server repeated: %d %s (%d mails)", rec.Code, rec.Body.String(), len(srv.Messages()))
	}
	for _, bad := range []map[string]any{{"smtp_port": 0}, {"smtp_security": "tls"}, {"smtp_from": "nope"}} {
		bad["to"] = "ops@example.test"
		bad["smtp_password"] = "p"
		if rec := do(t, s.h, http.MethodPost, "/api/v1/notifications/test", bad, s.admin); rec.Code != http.StatusBadRequest {
			t.Errorf("test with %v: %d, want 400", bad, rec.Code)
		}
	}
	// Send now queues one notify job (admin only), a duplicate answers 200.
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/send", nil, s.admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Job     JobDTO `json:"job"`
		Created bool   `json:"created"`
	}
	decode(t, rec.Body.Bytes(), &env)
	if !env.Created || env.Job.Kind != modules.JobKindNotify || env.Job.MailboxID != nil || env.Job.Target != "" || env.Job.RequestedBy != "web:admin" {
		t.Errorf("send job %+v", env)
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/notifications/send", nil, s.admin)
	decode(t, rec.Body.Bytes(), &env)
	if rec.Code != http.StatusOK || env.Created {
		t.Errorf("duplicate send: %d %+v", rec.Code, env)
	}
	// The generic job endpoint: notify is admin only, without a mailbox.
	if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "notify"}, s.user); rec.Code != http.StatusForbidden {
		t.Errorf("notify job as user: %d", rec.Code)
	}
	if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "notify", "mailbox_id": s.mb.ID}, s.admin); rec.Code != http.StatusBadRequest {
		t.Errorf("notify job with a mailbox: %d", rec.Code)
	}
	if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "notify", "target": "test:nope"}, s.admin); rec.Code != http.StatusBadRequest {
		t.Errorf("notify job with a bad target: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "notify", "target": "test:Ops@Example.test"}, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"target":"test:ops@example.test"`) {
		t.Errorf("notify test job: %d %s", rec.Code, rec.Body.String())
	}
	// Running the queued notification records the outcome on the job.
	job, err := models.ClaimJobByID(s.db, env.Job.ID)
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	result, err := s.jm.RunJob(t.Context(), job, nil)
	if err != nil || !strings.HasPrefix(result, "skipped: ") {
		t.Errorf("notify job result %q, %v", result, err)
	}
}
