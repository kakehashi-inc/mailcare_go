package workers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

func TestProbeEndpoints(t *testing.T) {
	s := newSeededCore(t)
	// Settings and actions: administrators only.
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/settings/probe"}, {http.MethodPut, "/api/v1/settings/probe"},
		{http.MethodPost, "/api/v1/settings/probe/test"}, {http.MethodPost, "/api/v1/probes/send"},
		{http.MethodPost, "/api/v1/probes/check"},
		{http.MethodPost, "/api/v1/mailboxes/probe-test"},
	} {
		if rec := do(t, s.h, req.method, req.path, map[string]any{}, s.user); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as user: %d", req.method, req.path, rec.Code)
		}
		if rec := do(t, s.h, req.method, req.path, map[string]any{}, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous: %d", req.method, req.path, rec.Code)
		}
	}

	// Defaults.
	rec := do(t, s.h, http.MethodGet, "/api/v1/settings/probe", nil, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var dto ProbeSettingsDTO
	decode(t, rec.Body.Bytes(), &dto)
	if dto.Enabled || strings.Join(dto.SendTimes, ",") != modules.DefaultProbeSendTimes ||
		dto.DelayMinutes != modules.DefaultProbeDelayMinutes || dto.FailureMinutes != modules.DefaultProbeFailureMinutes ||
		dto.IMAPPort != modules.DefaultIMAPPort || dto.IMAPFolder != "INBOX" || dto.Mailboxes != 0 {
		t.Errorf("defaults %+v", dto)
	}
	// Enabling needs the probe server.
	if rec := do(t, s.h, http.MethodPut, "/api/v1/settings/probe", map[string]any{"enabled": true}, s.admin); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "result.probe.notConfigured") {
		t.Errorf("enable unconfigured: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPut, "/api/v1/settings/probe", map[string]any{
		"enabled": true, "address": "probe@example.test", "imap_host": "imap.example.test", "imap_username": "probe",
		"imap_password": "secret", "send_times": []string{"08:00"}, "delay_minutes": 20, "failure_minutes": 90,
	}, s.admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec.Body.Bytes(), &dto)
	if !dto.Enabled || !dto.IMAPPasswordSet || dto.DelayMinutes != 20 || strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("saved %+v / %s", dto, rec.Body.String())
	}

	// The SMTP connection of a mailbox is shown to administrators
	// only; the password never.
	host := "smtp.example.test"
	in := modules.MailboxInputFrom(s.mb)
	on := true
	in.SmtpHost, in.ProbeEnabled, in.SmtpPort, in.SmtpUsername, in.SmtpPassword = &host, &on, 587, "ops", "smtp-pass"
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", in, s.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"smtp_host":"smtp.example.test"`) ||
		!strings.Contains(rec.Body.String(), `"smtp_password_set":true`) || strings.Contains(rec.Body.String(), "smtp-pass") {
		t.Fatalf("update mailbox: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/mailboxes/1", nil, s.user)
	// Whether the send check is on is shown to members; the connection is not.
	if strings.Contains(rec.Body.String(), "smtp.example.test") || !strings.Contains(rec.Body.String(), `"smtp_password_set":false`) ||
		!strings.Contains(rec.Body.String(), `"probe_enabled":true`) || strings.Contains(rec.Body.String(), `"smtp_username":"ops"`) ||
		!strings.Contains(rec.Body.String(), `"smtp_port":0`) {
		t.Errorf("SMTP connection shown to a member: %s", rec.Body.String())
	}

	// The error text of a probe mail that could not be sent names the SMTP
	// server: administrators only.
	sendFailed := &models.ProbeMail{MailboxID: s.mb.ID, Address: s.mb.Address, Token: "0000000000004567", Slot: "2026-10-15 07:00",
		Subject: "s", SentAt: time.Now().Add(-time.Hour), Status: models.ProbeStatusSendFailed, Level: models.ProbeLevelFailure,
		ErrorMessage: "connect to smtp.example.test:587: refused"}
	if err := models.InsertProbeMail(s.db, sendFailed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/probes", "/api/v1/dashboard"} {
		if rec := do(t, s.h, http.MethodGet, path, nil, s.user); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "smtp.example.test") {
			t.Errorf("%s: error text shown to a member: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/probes", nil, s.admin); !strings.Contains(rec.Body.String(), "smtp.example.test:587") {
		t.Errorf("error text not shown to an administrator: %s", rec.Body.String())
	}

	// A failure is listed for every user and can be resolved by any user,
	// like the state of an alert.
	p := &models.ProbeMail{MailboxID: s.mb.ID, Address: s.mb.Address, Token: "0000000000000123", Slot: "2026-10-15 07:00",
		Subject: "s", SentAt: time.Now().Add(-3 * time.Hour), Status: models.ProbeStatusFailure, Level: models.ProbeLevelFailure}
	if err := models.InsertProbeMail(s.db, p); err != nil {
		t.Fatal(err)
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/probes", nil, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"failure"`) ||
		!strings.Contains(rec.Body.String(), `"failure":1`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/probes?filter=nope", nil, s.user); rec.Code != http.StatusBadRequest {
		t.Errorf("bad filter: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/dashboard", nil, s.user)
	if !strings.Contains(rec.Body.String(), `"probe":{"enabled":true`) {
		t.Errorf("dashboard: %s", rec.Body.String())
	}
	path := "/api/v1/probes/" + itoa(p.ID) + "/resolve"
	if rec := do(t, s.h, http.MethodPut, path, map[string]any{"reason": ""}, s.admin); rec.Code != http.StatusBadRequest {
		t.Errorf("no reason: %d", rec.Code)
	}
	if rec := do(t, s.h, http.MethodPut, path, map[string]any{"reason": "maintenance"}, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("resolve anonymous: %d", rec.Code)
	}
	rec = do(t, s.h, http.MethodPut, path, map[string]any{"reason": "maintenance", "note": "planned"}, s.user)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"resolved"`) ||
		!strings.Contains(rec.Body.String(), `"resolved_by":"bob"`) {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodPut, path, map[string]any{"reason": "maintenance"}, s.admin); rec.Code != http.StatusConflict {
		t.Errorf("resolve twice: %d", rec.Code)
	}

	// The actions queue the probe jobs.
	rec = do(t, s.h, http.MethodPost, "/api/v1/probes/send", nil, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"kind":"probe_send"`) {
		t.Errorf("send: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/probes/check", nil, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"kind":"probe_check"`) {
		t.Errorf("check: %d %s", rec.Code, rec.Body.String())
	}
}

// A mailbox is saved without any SMTP setting, and with a host name alone
// (send check off); the send check needs the port, not the authentication.
func TestMailboxSavesWithoutSMTP(t *testing.T) {
	s := newSeededCore(t)
	rec := do(t, s.h, http.MethodPost, "/api/v1/mailboxes", map[string]any{
		"address": "nosmtp@example.test", "imap_host": "imap.example.test", "imap_username": "u", "imap_password": "p",
	}, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"smtp_host":""`) ||
		!strings.Contains(rec.Body.String(), `"probe_enabled":false`) {
		t.Fatalf("create without SMTP: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodPost, "/api/v1/mailboxes", map[string]any{
		"address": "hostonly@example.test", "imap_host": "imap.example.test", "imap_username": "u", "imap_password": "p",
		"smtp_host": "mail.example.test",
	}, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"smtp_host":"mail.example.test"`) ||
		!strings.Contains(rec.Body.String(), `"probe_enabled":false`) {
		t.Fatalf("create with a host only: %d %s", rec.Code, rec.Body.String())
	}
	in := modules.MailboxInputFrom(s.mb)
	host := "smtp.example.test"
	on := true
	in.SmtpHost, in.ProbeEnabled = &host, &on
	if rec := do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", in, s.admin); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "validation.mailbox.smtpPortRequired") {
		t.Fatalf("send check without port: %d %s", rec.Code, rec.Body.String())
	}
	// No authentication is needed.
	in.SmtpPort = 25
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", in, s.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"probe_enabled":true`) ||
		!strings.Contains(rec.Body.String(), `"smtp_port":25`) {
		t.Fatalf("send check without authentication: %d %s", rec.Code, rec.Body.String())
	}
}

// Members see the probe mails without the probe ID (also cut from the
// subject); a pending mail cannot be resolved; no mail is sent by hand while
// the probe is off; a partial update keeps the SMTP fields it leaves out.
func TestProbeMemberViewAndGuards(t *testing.T) {
	s := newSeededCore(t)
	tok := "0123456789abcdef"
	p := &models.ProbeMail{MailboxID: s.mb.ID, Address: s.mb.Address, Token: tok, Slot: "2026-10-15 07:00",
		Subject: "[MC:" + tok + "] Probe 2026-10-15 07:00 " + s.mb.Address, SentAt: time.Now(), Status: models.ProbeStatusPending}
	if err := models.InsertProbeMail(s.db, p); err != nil {
		t.Fatal(err)
	}
	// The probe ID is cut wherever it appears in the subject.
	tok2 := "fedcba9876543210"
	if err := models.InsertProbeMail(s.db, &models.ProbeMail{MailboxID: s.mb.ID, Address: s.mb.Address, Token: tok2,
		Slot: "2026-10-15 11:00", Subject: "[MC:" + tok2 + "] Probe " + tok2, SentAt: time.Now(), Status: models.ProbeStatusPending}); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s.h, http.MethodGet, "/api/v1/probes?filter=all", nil, s.user)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), tok) || strings.Contains(rec.Body.String(), tok2) ||
		!strings.Contains(rec.Body.String(), `"subject":"[MC] Probe 2026-10-15 07:00 `+s.mb.Address+`"`) {
		t.Errorf("member list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodGet, "/api/v1/probes?filter=all", nil, s.admin); !strings.Contains(rec.Body.String(), `"token":"`+tok+`"`) {
		t.Errorf("admin list lacks the probe ID: %s", rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodPut, "/api/v1/probes/"+itoa(p.ID)+"/resolve", map[string]any{"reason": "maintenance"}, s.user); rec.Code != http.StatusConflict {
		t.Errorf("pending resolved: %d %s", rec.Code, rec.Body.String())
	}
	// Off (the default): no send now.
	if rec := do(t, s.h, http.MethodPost, "/api/v1/probes/send", nil, s.admin); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "result.probe.disabled") {
		t.Errorf("send while off: %d %s", rec.Code, rec.Body.String())
	}
	// A partial update keeps the port, the username and the password.
	host := "smtp.example.test"
	in := modules.MailboxInputFrom(s.mb)
	on := true
	in.SmtpHost, in.ProbeEnabled, in.SmtpPort, in.SmtpUsername, in.SmtpPassword = &host, &on, 587, "ops", "smtp-pass"
	if rec := do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", in, s.admin); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	partial := map[string]any{"address": s.mb.Address, "imap_host": s.mb.ImapHost, "imap_username": s.mb.ImapUsername,
		"probe_enabled": true, "smtp_security": "ssl", "smtp_password": "new-pass"}
	if rec := do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", partial, s.admin); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"smtp_port":587`) || !strings.Contains(rec.Body.String(), `"smtp_username":"ops"`) ||
		!strings.Contains(rec.Body.String(), `"smtp_password_set":true`) || !strings.Contains(rec.Body.String(), `"smtp_security":"ssl"`) {
		t.Errorf("partial update: %d %s", rec.Code, rec.Body.String())
	}
}

// A probe send or check and an analysis asked for by hand are refused with
// the reason when they could do nothing (the generic job endpoint too);
// administrators see why an analysis cannot run.
func TestManualJobsRefusedWhenNothingToDo(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no agent CLI
	s := newSeededCore(t)
	str := func(v string) *string { return &v }
	on := true
	if err := modules.SaveProbeSettings(s.db, s.key, &modules.ProbeInput{Address: str("probe@example.test"),
		IMAPHost: str("imap.example.test"), IMAPUsername: str("probe"), IMAPPassword: str("x"), Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, body, key string }{
		{"/api/v1/probes/send", "", "result.probe.noMailbox"},
		{"/api/v1/probes/check", "", "result.probe.nothingOpen"},
		{"/api/v1/jobs", "probe_check", "result.probe.nothingOpen"},
		{"/api/v1/jobs", "analyze", "result.agent.unavailable"},
	} {
		var body any
		if c.body != "" {
			body = map[string]any{"kind": c.body}
		}
		if rec := do(t, s.h, http.MethodPost, c.path, body, s.admin); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.key) {
			t.Errorf("%s %s: %d %s, want 400 %s", c.path, c.body, rec.Code, rec.Body.String(), c.key)
		}
	}
	settings := func(u *http.Cookie) string {
		return do(t, s.h, http.MethodGet, "/api/v1/settings", nil, u).Body.String()
	}
	if body := settings(s.admin); !strings.Contains(body, `"analyze_refusal":"result.agent.unavailable"`) {
		t.Errorf("settings without the agent CLI: %s", body)
	}
	if body := settings(s.user); strings.Contains(body, "analyze_refusal") {
		t.Errorf("members see the analysis state: %s", body)
	}
	// With the CLI but within a recorded usage limit; then free.
	fakeAgentCLI(t)
	if err := modules.SetAgentLimitedUntil(s.db, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rec := do(t, s.h, http.MethodPost, s.path("/groups/0123456789abcdef/analyze"), nil, s.admin)
	if rec.Code == http.StatusCreated {
		t.Errorf("analysis queued within the usage limit: %s", rec.Body.String())
	}
	if rec := do(t, s.h, http.MethodPost, "/api/v1/jobs", map[string]any{"kind": "analyze", "target": "*"}, s.admin); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "result.agent.usageLimited") {
		t.Errorf("analysis within the usage limit: %d %s", rec.Code, rec.Body.String())
	}
	if body := settings(s.admin); !strings.Contains(body, `"analyze_refusal":"result.agent.usageLimited"`) {
		t.Errorf("settings within the usage limit: %s", body)
	}
	if err := modules.SetAgentLimitedUntil(s.db, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if body := settings(s.admin); !strings.Contains(body, `"analyze_refusal":""`) {
		t.Errorf("settings with the agent available: %s", body)
	}
	active, _ := models.ListActiveJobs(s.db)
	for _, j := range active {
		if j.Kind == modules.JobKindProbeSend || j.Kind == modules.JobKindProbeCheck || j.Kind == modules.JobKindAnalyze {
			t.Errorf("a refused job was queued: %+v", j)
		}
	}
}
