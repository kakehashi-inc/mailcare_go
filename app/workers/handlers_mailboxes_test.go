package workers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// TestMailboxBusyAnswersConflict checks that deleting or renaming a mailbox
// while one of its jobs runs answers 409, that other updates still work,
// and that the deletion afterwards cancels the mailbox's queued jobs.
func TestMailboxBusyAnswersConflict(t *testing.T) {
	s := newSeededCore(t)
	running, _, err := modules.EnqueueJob(s.db, modules.JobKindGroup, s.mb.ID, "", "cli", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := models.ClaimJobByID(s.db, running.ID); err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	waiting, _, err := modules.EnqueueJob(s.db, modules.JobKindFetch, s.mb.ID, "", "cli", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, s.h, http.MethodDelete, "/api/v1/mailboxes/"+itoa(s.mb.ID), nil, s.admin)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"key":"result.mailbox.busy"`) {
		t.Errorf("delete while running: %d %s", rec.Code, rec.Body.String())
	}
	body := map[string]any{"address": "renamed@example.test", "imap_host": "imap.example.test", "imap_username": "u"}
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/"+itoa(s.mb.ID), body, s.admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("rename while running: %d %s", rec.Code, rec.Body.String())
	}
	body["address"] = s.mb.Address
	body["display_name"] = "Ops"
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/"+itoa(s.mb.ID), body, s.admin)
	if rec.Code != http.StatusOK {
		t.Errorf("update without rename while running: %d %s", rec.Code, rec.Body.String())
	}
	if err := models.FinishJob(s.db, running.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	rec = do(t, s.h, http.MethodDelete, "/api/v1/mailboxes/"+itoa(s.mb.ID), nil, s.admin)
	if rec.Code != http.StatusOK {
		t.Errorf("delete after the job finished: %d %s", rec.Code, rec.Body.String())
	}
	if j, err := models.GetJobByID(s.db, waiting.ID); err != nil || j.Status != modules.JobStatusCanceled {
		t.Errorf("queued job after the delete = %+v (err %v), want canceled", j, err)
	}
}

// TestMessageEndpointsAfterAudit checks the search and HTML endpoint
// details: a search string of any length is accepted, LIKE wildcards in it
// match literally, and a message whose HTML section files are all missing
// answers 404.
func TestMessageEndpointsAfterAudit(t *testing.T) {
	s := newSeededCore(t)
	long := strings.Repeat("x", 500)
	rec := do(t, s.h, http.MethodGet, s.path("/messages?q="+long), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("long query: %d %s", rec.Code, rec.Body.String())
	}
	// "%" and "_" are literal characters of the search, not wildcards.
	for _, q := range []string{"%25", "_", "%25%25"} {
		rec = do(t, s.h, http.MethodGet, s.path("/messages?q="+q), nil, s.user)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
			t.Errorf("query %s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	rec = do(t, s.h, http.MethodGet, s.path("/messages?q=Undelivered"), nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Errorf("plain query: %d %s", rec.Code, rec.Body.String())
	}
	// The index announces an HTML section whose file does not exist.
	idx, err := s.openIndex(httptest.NewRequest(http.MethodGet, "/", nil), s.mb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Exec(`UPDATE messages SET html_count = 1 WHERE message_key = ?`, s.keys[0]); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	rec = do(t, s.h, http.MethodGet, s.path("/messages/"+s.keys[0]+"/html/1"), nil, s.user)
	if rec.Code != http.StatusNotFound {
		t.Errorf("html with missing section file: %d %s", rec.Code, rec.Body.String())
	}
}

// TestMailboxSmtpHostThroughTheAPI: the SMTP host is created and updated
// normalized, refused with validation.mailbox.smtpHostInvalid (nothing
// saved), kept by an update without the field, cleared by "", changed
// without the IMAP password (which stays stored), shown to every user and
// changed by administrators only. The settings list the SMTP hosts of the
// mailboxes as sending_source_mailbox_hosts, which a PUT cannot set.
func TestMailboxSmtpHostThroughTheAPI(t *testing.T) {
	s := newSeededCore(t)
	path := "/api/v1/mailboxes/" + itoa(s.mb.ID)
	type mailboxReply struct {
		Mailbox struct {
			SmtpHost string `json:"smtp_host"`
		} `json:"mailbox"`
	}
	type message struct {
		Key    string         `json:"key"`
		Params map[string]any `json:"params"`
	}
	smtpHost := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var r mailboxReply
		decode(t, rec.Body.Bytes(), &r)
		return r.Mailbox.SmtpHost
	}
	stored := func() *models.Mailbox {
		t.Helper()
		mb, err := models.GetMailboxByID(s.db, s.mb.ID)
		if err != nil {
			t.Fatal(err)
		}
		return mb
	}
	mailboxHosts := func() []string {
		t.Helper()
		var st struct {
			Hosts []string `json:"sending_source_mailbox_hosts"`
		}
		rec := do(t, s.h, http.MethodGet, "/api/v1/settings", nil, s.admin)
		decode(t, rec.Body.Bytes(), &st)
		if st.Hosts == nil {
			t.Fatalf("sending_source_mailbox_hosts missing: %s", rec.Body.String())
		}
		return st.Hosts
	}
	if got := mailboxHosts(); len(got) != 0 {
		t.Errorf("sending_source_mailbox_hosts before any SMTP host: %v", got)
	}

	// Creation: normalized.
	rec := do(t, s.h, http.MethodPost, "/api/v1/mailboxes", map[string]any{"address": "info@example.test", "imap_host": "imap.example.test",
		"imap_username": "u", "imap_password": "p", "smtp_host": " Mail.Example.JP. "}, s.admin)
	if rec.Code != http.StatusCreated || smtpHost(rec) != "mail.example.jp" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	// Update of the SMTP host alone, without the password.
	body := map[string]any{"address": s.mb.Address, "imap_host": "imap.example.test", "imap_username": "u", "smtp_host": "SMTP.example.jp"}
	rec = do(t, s.h, http.MethodPut, path, body, s.admin)
	if rec.Code != http.StatusOK || smtpHost(rec) != "smtp.example.jp" || stored().SmtpHost != "smtp.example.jp" {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if pw, err := modules.MailboxPassword(s.key, stored()); err != nil || pw != "p" {
		t.Errorf("password after the update: %q, %v", pw, err)
	}
	if got := mailboxHosts(); !reflect.DeepEqual(got, []string{"mail.example.jp", "smtp.example.jp"}) {
		t.Errorf("sending_source_mailbox_hosts: %v", got)
	}
	// Invalid: refused, nothing saved.
	for _, bad := range []string{"203.0.113.5", "mail_1.example.jp", "localhost", "mail.example.jp:25"} {
		body["smtp_host"] = bad
		rec = do(t, s.h, http.MethodPut, path, body, s.admin)
		var msg message
		decode(t, rec.Body.Bytes(), &msg)
		if rec.Code != http.StatusBadRequest || msg.Key != "validation.mailbox.smtpHostInvalid" || msg.Params["value"] != bad {
			t.Errorf("%q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if stored().SmtpHost != "smtp.example.jp" {
		t.Errorf("a refused update changed it to %q", stored().SmtpHost)
	}
	// Without the field: kept.
	delete(body, "smtp_host")
	body["display_name"] = "Ops"
	if rec = do(t, s.h, http.MethodPut, path, body, s.admin); rec.Code != http.StatusOK || stored().SmtpHost != "smtp.example.jp" {
		t.Errorf("update without the field: %d %s", rec.Code, rec.Body.String())
	}
	// An administrator sees it, a member gets it blanked like the IMAP
	// connection; only an administrator changes it.
	for _, p := range []string{path, "/api/v1/mailboxes"} {
		if rec := do(t, s.h, http.MethodGet, p, nil, s.admin); !strings.Contains(rec.Body.String(), `"smtp_host":"smtp.example.jp"`) {
			t.Errorf("GET %s as admin: %s", p, rec.Body.String())
		}
		if rec := do(t, s.h, http.MethodGet, p, nil, s.user); !strings.Contains(rec.Body.String(), `"smtp_host":""`) ||
			strings.Contains(rec.Body.String(), "smtp.example.jp") {
			t.Errorf("GET %s as user: %s", p, rec.Body.String())
		}
	}
	body["smtp_host"] = "other.example.jp"
	if rec := do(t, s.h, http.MethodPut, path, body, s.user); rec.Code != http.StatusForbidden || stored().SmtpHost != "smtp.example.jp" {
		t.Errorf("update by a user: %d", rec.Code)
	}
	// "" clears it.
	body["smtp_host"] = ""
	if rec = do(t, s.h, http.MethodPut, path, body, s.admin); rec.Code != http.StatusOK || smtpHost(rec) != "" || stored().SmtpHost != "" {
		t.Errorf("clearing: %d %s", rec.Code, rec.Body.String())
	}
	if got := mailboxHosts(); !reflect.DeepEqual(got, []string{"mail.example.jp"}) {
		t.Errorf("sending_source_mailbox_hosts after clearing: %v", got)
	}
	// The settings cannot set it: the field is ignored like any unknown one.
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings", map[string]any{"sending_source_mailbox_hosts": []string{"x.example.jp"}}, s.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sending_source_mailbox_hosts":["mail.example.jp"]`) {
		t.Errorf("PUT sending_source_mailbox_hosts: %d %s", rec.Code, rec.Body.String())
	}
	if v, found, err := models.GetSettingStrict(s.db, "sending_source_mailbox_hosts"); found || err != nil {
		t.Errorf("stored %q (%v)", v, err)
	}
}

// The error of the last fetch names the IMAP server: members read only that
// there was one.
func TestLastFetchErrorRedactedForMembers(t *testing.T) {
	s := newSeededCore(t)
	if err := models.UpdateMailboxFetchResult(s.db, s.mb.ID, "dial tcp imap.example.test:993: connection refused"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/mailboxes/1", "/api/v1/mailboxes", "/api/v1/dashboard"} {
		rec := do(t, s.h, http.MethodGet, path, nil, s.user)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "imap.example.test") || !strings.Contains(rec.Body.String(), `"last_fetch_error":"error"`) {
			t.Errorf("%s as a member: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/mailboxes/1", nil, s.admin); !strings.Contains(rec.Body.String(), "imap.example.test:993") {
		t.Errorf("admin: %s", rec.Body.String())
	}
}
