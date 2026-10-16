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
	in.SmtpHost, in.SmtpUsername, in.SmtpPassword = &host, "ops", "smtp-pass"
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", in, s.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"smtp_host":"smtp.example.test"`) ||
		!strings.Contains(rec.Body.String(), `"smtp_password_set":true`) || strings.Contains(rec.Body.String(), "smtp-pass") {
		t.Fatalf("update mailbox: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, s.h, http.MethodGet, "/api/v1/mailboxes/1", nil, s.user)
	if strings.Contains(rec.Body.String(), "smtp.example.test") {
		t.Errorf("SMTP host shown to a member: %s", rec.Body.String())
	}

	// A failure is listed for every user and can be resolved by any user,
	// like the state of an alert.
	p := &models.ProbeMail{MailboxID: s.mb.ID, Address: s.mb.Address, Token: "MCPROBE-0123", Slot: "2026-10-15 07:00",
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

// A mailbox is saved without any SMTP setting, and clearing the host clears
// the rest of the SMTP connection.
func TestMailboxSavesWithoutSMTP(t *testing.T) {
	s := newSeededCore(t)
	rec := do(t, s.h, http.MethodPost, "/api/v1/mailboxes", map[string]any{
		"address": "nosmtp@example.test", "imap_host": "imap.example.test", "imap_username": "u", "imap_password": "p",
	}, s.admin)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"smtp_host":""`) {
		t.Fatalf("create without SMTP: %d %s", rec.Code, rec.Body.String())
	}
	in := modules.MailboxInputFrom(s.mb)
	host := "smtp.example.test"
	in.SmtpHost, in.SmtpUsername, in.SmtpPassword = &host, "ops", "pw"
	if rec := do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", in, s.admin); rec.Code != http.StatusOK {
		t.Fatalf("set SMTP: %d %s", rec.Code, rec.Body.String())
	}
	// Clearing the host needs no password and drops the rest, whatever
	// the other SMTP fields carry.
	empty := ""
	in.SmtpHost, in.SmtpPort, in.SmtpUsername, in.SmtpPassword = &empty, 2525, "ops", ""
	rec = do(t, s.h, http.MethodPut, "/api/v1/mailboxes/1", in, s.admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"smtp_host":""`) ||
		!strings.Contains(rec.Body.String(), `"smtp_username":""`) || !strings.Contains(rec.Body.String(), `"smtp_password_set":false`) {
		t.Fatalf("clear SMTP: %d %s", rec.Code, rec.Body.String())
	}
}
