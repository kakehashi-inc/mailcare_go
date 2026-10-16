package modules

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/testkit"
	"mailcare/app/modules/wording"
)

func wantWordingKey(t *testing.T, err error, key string) {
	t.Helper()
	m, ok := wording.As(err)
	if !ok || m.Key != key {
		t.Errorf("error = %v, want %s", err, key)
	}
}

// setupProbe configures the probe against an in-memory probe server and the
// notification mails against a fake SMTP server (an administrator and a
// member are the recipients).
func setupProbe(t *testing.T) (*sql.DB, []byte, *probeIMAP, *testkit.Server) {
	t.Helper()
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := startProbeIMAP(t)
	smtpSrv := startFakeSMTP(t)
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	on := true
	if err := SaveProbeSettings(db, key, &ProbeInput{Address: str("probe@example.test"), IMAPHost: str(srv.host),
		IMAPPort: num(srv.port), IMAPSecurity: str(IMAPSecurityNone), IMAPUsername: str("probe"), IMAPPassword: str("probe-secret"),
		DelayMinutes: num(30), FailureMinutes: num(120), Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	admin, _ := CreateUserFrom(db, NewUser{Username: "admin", Email: "admin@example.test", Password: "password123", Role: RoleAdmin})
	member, _ := CreateUserFrom(db, NewUser{Username: "bob", Email: "bob@example.test", Password: "password123", Role: RoleUser})
	ids := []int64{admin.ID, member.ID}
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str(smtpSrv.Host()), SMTPPort: num(smtpSrv.Port()),
		SMTPSecurity: str(IMAPSecurityNone), SMTPFrom: str("mailcare@example.test"), UserIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	return db, key, srv, smtpSrv
}

// addProbeMail records a probe mail of the mailbox address (registered on
// first use, with the send check on).
func addProbeMail(t *testing.T, db *sql.DB, key []byte, address string, p *models.ProbeMail) *models.ProbeMail {
	t.Helper()
	mb, err := models.GetMailboxByAddress(db, address)
	if err != nil {
		if mb, err = CreateMailbox(db, key, &MailboxInput{Address: address, ImapHost: "imap.example.test", ImapUsername: "u", ImapPassword: "p"}); err != nil {
			t.Fatal(err)
		}
		mb.SmtpHost, mb.ProbeEnabled, mb.SmtpPort, mb.SmtpSecurity = "smtp.example.test", true, 25, IMAPSecurityNone
		if err := models.UpdateMailbox(db, mb); err != nil {
			t.Fatal(err)
		}
	}
	p.MailboxID, p.Address = mb.ID, mb.Address
	if p.Slot == "" {
		p.Slot = "2026-10-15 07:00"
	}
	if p.Subject == "" {
		p.Subject = probeSubject(DefaultProbeSubjectTag, p.Slot, mb.Address, p.Token)
	}
	if err := models.InsertProbeMail(db, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// probeRaw builds a probe mail as sendOneProbe does (identifier MC).
func probeRaw(from, fromName, token string) string {
	return probeRawTag(DefaultProbeSubjectTag, from, fromName, token)
}

// probeRawTag is probeRaw with another identifier.
func probeRawTag(tag, from, fromName, token string) string {
	return buildMessage(from, []string{"probe@example.test"}, OutgoingMail{FromName: fromName,
		Subject: probeSubject(tag, "2026-10-15 07:00", from, token), Body: "x",
		Headers: []MailHeader{{Name: ProbeHeader, Value: token}}}, time.Now())
}

func probeToken(n int) string {
	return fmt.Sprintf("00112233445566%02x", 0x10+n)
}

// A resolution made while an arrival check runs wins: the check does not
// overwrite it, does not count the mail and mails no failure for it.
func TestProbeResolveDuringCheck(t *testing.T) {
	db, key, _, smtpSrv := setupProbe(t)
	p := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1),
		SentAt: time.Now().Add(-3 * time.Hour), Status: models.ProbeStatusDelayed, Level: models.ProbeLevelDelayed})
	waiting, _ := models.ListWaitingProbeMails(db)
	if _, err := ResolveProbe(db, p.ID, "bob", models.ProbeReasonMaintenance, "planned"); err != nil {
		t.Fatal(err)
	}
	s, _ := ResolveProbeSettings(db, nil)
	res := &ProbeCheckResult{Checked: len(waiting)}
	if err := recordProbeArrivals(db, waiting, nil, probeSort{}, time.Now(), s, res, func(string) {}); err != nil {
		t.Fatal(err)
	}
	got, _ := models.GetProbeMail(db, p.ID)
	if got.Status != models.ProbeStatusResolved || got.ResolveReason != models.ProbeReasonMaintenance {
		t.Errorf("the resolution was overwritten: %+v", got)
	}
	if res.Checked != 0 || res.Failed != 0 || res.Missing != 0 {
		t.Errorf("the resolved mail was counted: %+v", res)
	}
	notifyProbeAlerts(context.Background(), db, key, "", func(string) {})
	if n := len(smtpSrv.Messages()); n != 0 {
		t.Errorf("failure mails for a resolved mail: %d", n)
	}
}

// A display name with characters that are special in an address header
// (comma, colon) or a long Japanese one keeps the From header readable and
// every header line within the line limit.
func TestBuildMessageFromDisplayName(t *testing.T) {
	long := strings.Repeat("営業部東京支店", 20)
	for _, name := range []string{"Sales, Tokyo", "x: y", "a@b \"quoted\"", long, ""} {
		raw := buildMessage("sales@example.test", []string{"probe@example.test"}, OutgoingMail{FromName: name, Subject: "s", Body: "y"}, time.Now())
		for _, line := range strings.Split(raw, "\r\n") {
			if len(line) > maxLineLength {
				t.Errorf("%q: line of %d octets", name, len(line))
			}
		}
		msg, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		list, err := msg.Header.AddressList("From")
		want := strings.Join(strings.Fields(name), " ")
		if want == "" {
			want = SMTPFromName
		}
		if err != nil || len(list) != 1 || list[0].Address != "sales@example.test" || list[0].Name != want {
			t.Errorf("%q: From %q parsed as %v (%v)", name, msg.Header.Get("From"), list, err)
		}
	}
}

// A probe mail whose mailbox has such a display name is still verified on
// the probe server (the sender is read from the envelope).
func TestProbeVerifiedWithSpecialDisplayName(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	for i, name := range []string{"Sales, Tokyo", "x: y", strings.Repeat("営業部東京支店", 20)} {
		address := "sales" + strconv.Itoa(i) + "@example.test"
		p := addProbeMail(t, db, key, address, &models.ProbeMail{Token: probeToken(i), SentAt: time.Now().Add(-time.Minute),
			Status: models.ProbeStatusPending})
		srv.deliver(t, probeRaw(address, name, p.Token), time.Now())
	}
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Arrived != 3 || res.Deleted != 3 || srv.count(t) != 0 {
		t.Errorf("check: %+v %v, %d left", res, err, srv.count(t))
	}
}

// The failure mails not sent yet are tried again by a check with nothing
// waiting (only a send_failed mail is open: it is looked for once, nothing
// more is scheduled) and at the end of every send.
func TestProbeFailureMailRetried(t *testing.T) {
	db, key, _, smtpSrv := setupProbe(t)
	p := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-time.Hour),
		Status: models.ProbeStatusSendFailed, Level: models.ProbeLevelFailure, ErrorMessage: "connect to smtp.example.test:587: refused"})
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Skipped != "" || res.Missing != 0 || !res.NextAt.IsZero() {
		t.Fatalf("check: %+v %v", res, err)
	}
	got, _ := models.GetProbeMail(db, p.ID)
	msgs := smtpSrv.Messages()
	if !got.NotifiedAt.Valid || len(msgs) != 2 {
		t.Fatalf("failure mail not retried: notified %v, %d mails", got.NotifiedAt.Valid, len(msgs))
	}
	// The member reads that the details are for administrators.
	for _, m := range msgs {
		admin := m.To[0] == "admin@example.test"
		if strings.Contains(m.Data, "smtp.example.test") != admin {
			t.Errorf("error text to %s: %v", m.To[0], !admin)
		}
	}
	// A send without failures mails the failures left over as well.
	q := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().Add(-time.Hour),
		Status: models.ProbeStatusSendFailed, Level: models.ProbeLevelFailure, ErrorMessage: "x"})
	mb, _ := models.GetMailboxByAddress(db, "a@example.test")
	mb.SmtpHost, mb.SmtpPort = smtpSrv.Host(), smtpSrv.Port()
	if err := models.UpdateMailbox(db, mb); err != nil {
		t.Fatal(err)
	}
	if r, err := SendProbes(context.Background(), db, key, "", "", func(string) {}); err != nil || r.Sent != 1 || r.Failed != 0 {
		t.Fatalf("send: %+v %v", r, err)
	}
	if got, _ := models.GetProbeMail(db, q.ID); !got.NotifiedAt.Valid {
		t.Error("the send did not mail the failure left over")
	}
}

// A check that cannot read the probe server backs off (10 minutes, doubled,
// at most the failure threshold), mails the notification recipients once
// when the server stayed unreadable for the failure threshold, and a check
// that reads the server again resets all of it.
func TestProbeUnreadableServer(t *testing.T) {
	db, key, srv, smtpSrv := setupProbe(t)
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPPort: num(closedPort), IMAPPassword: str("probe-secret")}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, want := range []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute, 120 * time.Minute} {
		start := time.Now()
		if _, err := CheckProbes(ctx, db, key, "", func(string) {}); err == nil {
			t.Fatal("the closed port was read")
		}
		s, _ := ResolveProbeSettings(db, nil)
		if d := s.NextCheckAt.Time.Sub(start).Round(time.Minute); d != want || s.CheckFailures != i+1 || s.LastCheckError == "" {
			t.Errorf("failure %d: retry after %v, failures %d, error %q", i+1, d, s.CheckFailures, s.LastCheckError)
		}
	}
	if n := len(smtpSrv.Messages()); n != 0 {
		t.Fatalf("mailed before the failure threshold: %d", n)
	}
	// Unreadable since longer than the failure threshold: one mail.
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeCheckFailingSince, time.Now().Add(-121*time.Minute))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, _ = CheckProbes(ctx, db, key, "", func(string) {})
	}
	msgs := smtpSrv.Messages()
	if len(msgs) != 2 {
		t.Fatalf("unreadable mails: %d", len(msgs))
	}
	for _, m := range msgs {
		admin := m.To[0] == "admin@example.test"
		if strings.Contains(m.Data, "127.0.0.1") != admin {
			t.Errorf("error text to %s", m.To[0])
		}
	}
	// Readable again: everything is reset.
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPPort: num(srv.port), IMAPPassword: str("probe-secret")}); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckProbes(ctx, db, key, "", func(string) {}); err != nil {
		t.Fatal(err)
	}
	s, _ := ResolveProbeSettings(db, nil)
	if s.LastCheckError != "" || s.CheckFailures != 0 || s.FailingSince.Valid || s.UnreadableNotifiedAt.Valid {
		t.Errorf("not reset: %+v", s)
	}
}

// The delay threshold must be shorter than the shortest gap between two send
// times (across midnight too); at most 24 send times, 15 minutes apart.
func TestProbeSendTimesValidation(t *testing.T) {
	db := newTestDB(t)
	key, _ := LoadSecretKey(db)
	num := func(v int) *int { return &v }
	if gap, ok := minProbeSendGap([]string{"07:00", "23:50"}); !ok || gap != 7*60+10 {
		t.Errorf("gap = %d", gap)
	}
	if gap, _ := minProbeSendGap([]string{"12:00"}); gap != 24*60 {
		t.Errorf("single time gap = %d", gap)
	}
	wantWordingKey(t, SaveProbeSettings(db, key, &ProbeInput{SendTimes: &[]string{"23:55", "00:05"}}), "validation.probe.sendTimesTooClose")
	many := []string{}
	for h := range 25 {
		many = append(many, fmt.Sprintf("%02d:%02d", h%24, 30*(h/24)))
	}
	wantWordingKey(t, SaveProbeSettings(db, key, &ProbeInput{SendTimes: &many}), "validation.probe.sendTimesTooMany")
	// With the default delay threshold (15 minutes) send times 15 minutes
	// apart are refused and 20 minutes apart accepted.
	wantWordingKey(t, SaveProbeSettings(db, key, &ProbeInput{SendTimes: &[]string{"07:00", "07:15"}}), "validation.probe.delayNotBeforeNextSend")
	if err := SaveProbeSettings(db, key, &ProbeInput{SendTimes: &[]string{"07:00", "07:20"}}); err != nil {
		t.Errorf("default delay, 20 minutes apart: %v", err)
	}
	if err := SaveProbeSettings(db, key, &ProbeInput{SendTimes: &[]string{"07:00", "07:30"}, DelayMinutes: num(20), FailureMinutes: num(60)}); err != nil {
		t.Errorf("valid combination: %v", err)
	}
	wantWordingKey(t, SaveProbeSettings(db, key, &ProbeInput{DelayMinutes: num(30)}), "validation.probe.delayNotBeforeNextSend")
}

// The next check also comes when a missing mail reaches the failure
// threshold, if that is earlier than the regular next check.
func TestProbeNextCheckAtFailureThreshold(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	sent := time.Now().Add(-100 * time.Minute)
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: sent,
		Status: models.ProbeStatusDelayed, Level: models.ProbeLevelDelayed})
	// The latest send was long ago: the regular next check is up to 120
	// minutes away, the failure of the mail 20 minutes.
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeLastSendAt, time.Now().Add(-125*time.Minute))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || !res.NextAt.Equal(sent.Add(120*time.Minute)) {
		t.Errorf("next check %v, want %v (%v)", res.NextAt, sent.Add(120*time.Minute), err)
	}
}

// Switching the probe off, the send check of a mailbox off or the mailbox
// off closes the open probe mails as the system; no mail is sent by hand
// while the probe is off.
func TestProbeSwitchedOffClosesOpenMails(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	host := "smtp.example.test"
	on := true
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: "a@example.test", ImapHost: "imap.example.test", ImapUsername: "u",
		ImapPassword: "p", SmtpHost: &host, ProbeEnabled: &on, SmtpPort: 25})
	if err != nil {
		t.Fatal(err)
	}
	open := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	other := addProbeMail(t, db, key, "b@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now(), Status: models.ProbeStatusFailure, Level: models.ProbeLevelFailure})
	arrived := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(3), SentAt: time.Now(), Status: models.ProbeStatusArrived})
	in := MailboxInputFrom(mb)
	off := false
	in.ProbeEnabled = &off
	if err := UpdateMailbox(db, key, t.TempDir(), t.TempDir(), mb, in); err != nil {
		t.Fatal(err)
	}
	if p, _ := models.GetProbeMail(db, open.ID); p.Status != models.ProbeStatusResolved || p.ResolveReason != models.ProbeReasonProbeDisabled || p.ResolvedBy != "" {
		t.Errorf("open mail of the mailbox: %+v", p)
	}
	if p, _ := models.GetProbeMail(db, other.ID); p.Status != models.ProbeStatusFailure {
		t.Errorf("mail of another mailbox: %+v", p)
	}
	if err := SaveProbeSettings(db, key, &ProbeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if p, _ := models.GetProbeMail(db, other.ID); p.Status != models.ProbeStatusResolved || p.ResolveReason != models.ProbeReasonProbeDisabled {
		t.Errorf("open mail after the probe was switched off: %+v", p)
	}
	if p, _ := models.GetProbeMail(db, arrived.ID); p.Status != models.ProbeStatusArrived {
		t.Errorf("arrived mail: %+v", p)
	}
	if res, err := SendProbes(context.Background(), db, key, "", "", func(string) {}); err != nil || res.Skipped != SkipProbeDisabled {
		t.Errorf("send while off: %+v %v", res, err)
	}
}

// Only delayed, failed and unsent probe mails can be resolved by hand.
func TestProbeResolveOnlyOverdue(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	pending := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	if _, err := ResolveProbe(db, pending.ID, "bob", models.ProbeReasonOther, ""); err == nil {
		t.Error("a pending mail was resolved")
	} else {
		wantWordingKey(t, err, "result.probe.notOpen")
	}
	if _, err := ResolveProbe(db, pending.ID, "bob", models.ProbeReasonProbeDisabled, ""); err == nil {
		t.Error("the system reason was accepted by hand")
	}
}

// The send check needs a usable password once a username is set: given, or
// stored for the same SMTP connection.
func TestMailboxSMTPPasswordRequired(t *testing.T) {
	db := newTestDB(t)
	key, _ := LoadSecretKey(db)
	host := "smtp.example.test"
	on := true
	in := &MailboxInput{Address: "a@example.test", ImapHost: "imap.example.test", ImapUsername: "u", ImapPassword: "p",
		SmtpHost: &host, ProbeEnabled: &on, SmtpPort: 587, SmtpUsername: "ops"}
	if _, err := CreateMailbox(db, key, in); err != errSMTPPasswordRequired {
		t.Errorf("created without a password: %v", err)
	}
	in.SmtpUsername = ""
	mb, err := CreateMailbox(db, key, in)
	if err != nil {
		t.Fatal(err)
	}
	up := MailboxInputFrom(mb)
	up.SmtpUsername = "ops"
	if err := UpdateMailbox(db, key, t.TempDir(), t.TempDir(), mb, up); err != errSMTPPasswordRequired {
		t.Errorf("username added without a password: %v", err)
	}
}

// A partial update over HTTP keeps the SMTP fields it leaves out.
func TestKeepAbsentSMTPFields(t *testing.T) {
	stored := &models.Mailbox{ProbeEnabled: true, SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpSecurity: "starttls", SmtpUsername: "ops"}
	on := true
	in := &MailboxInput{ProbeEnabled: &on, SmtpPort: 2525}
	KeepAbsentSMTPFields(stored, in, func(name string) bool { return name == "smtp_port" || name == "probe_enabled" })
	if in.SmtpPort != 2525 || in.SmtpSecurity != "starttls" || in.SmtpUsername != "ops" {
		t.Errorf("kept: %+v", in)
	}
	// A field sent empty keeps its meaning (no authentication).
	in = &MailboxInput{ProbeEnabled: &on, SmtpUsername: ""}
	KeepAbsentSMTPFields(stored, in, func(name string) bool { return name == "smtp_username" || name == "probe_enabled" })
	if in.SmtpUsername != "" || in.SmtpPort != 587 {
		t.Errorf("empty username: %+v", in)
	}
}

// A probe_send with a send time is queued by the scheduler only.
func TestProbeSendSlotFromSchedulerOnly(t *testing.T) {
	db := newTestDB(t)
	if _, _, err := EnqueueJob(db, JobKindProbeSend, 0, "2026-10-15 07:00", "web:admin", 0, nil); err == nil {
		t.Error("a send time was accepted from the Web")
	}
	if _, _, err := EnqueueJob(db, JobKindProbeSend, 0, "2026-10-15 07:00", RequestedByScheduler, 0, nil); err != nil {
		t.Errorf("scheduler: %v", err)
	}
	if _, _, err := EnqueueJob(db, JobKindProbeSend, 0, "", "web:admin", 0, nil); err != nil {
		t.Errorf("send now: %v", err)
	}
}

// The arrival check holds the host of the probe server like the other IMAP
// jobs.
func TestProbeCheckHoldsProbeHost(t *testing.T) {
	db, _, srv, _ := setupProbe(t)
	jm, _ := newTestJobManager(t, db)
	keys := jm.resourceKeys(&models.Job{Kind: JobKindProbeCheck})
	if strings.Join(keys, ",") != lockProbe+","+lockHostPrefix+srv.host {
		t.Errorf("keys = %v", keys)
	}
	if keys := jm.resourceKeys(&models.Job{Kind: JobKindProbeSend}); strings.Join(keys, ",") != lockProbe {
		t.Errorf("send keys = %v", keys)
	}
}

// A recorded token found with another sender is reported in the progress
// and on the waiting row and stays on the server; a send_failed mail that
// arrives after all is recovered.
func TestProbeSenderMismatchAndLateSendFailed(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	waiting := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	failed := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().Add(-time.Hour),
		Status: models.ProbeStatusSendFailed, Level: models.ProbeLevelFailure, ErrorMessage: "EOF"})
	srv.deliver(t, probeRaw("bounce@relay.example.test", "", waiting.Token), time.Now())
	srv.deliver(t, probeRaw("a@example.test", "", failed.Token), time.Now())
	var lines []string
	res, err := CheckProbes(context.Background(), db, key, "", func(l string) { lines = append(lines, l) })
	if err != nil || res.Mismatched != 1 || res.Recovered != 1 || res.Deleted != 1 || srv.count(t) != 1 {
		t.Fatalf("check: %+v %v, %d left", res, err, srv.count(t))
	}
	want := "token matched, sender differs (a@example.test vs bounce@relay.example.test)"
	if !strings.Contains(strings.Join(lines, "\n"), want) {
		t.Errorf("progress lacks %q: %v", want, lines)
	}
	if p, _ := models.GetProbeMail(db, waiting.ID); p.Status != models.ProbeStatusPending || p.ErrorMessage != want {
		t.Errorf("waiting row: %+v", p)
	}
	if p, _ := models.GetProbeMail(db, failed.ID); p.Status != models.ProbeStatusRecovered || p.Level != models.ProbeLevelFailure ||
		!p.ArrivedAt.Valid || p.ErrorMessage != "EOF" {
		t.Errorf("send_failed row: %+v", p)
	}
}

// The daily cleanup keeps the probe mail records at least
// MinProbeRecordKeepDays, even with a shorter mail retention.
func TestCleanupKeepsProbeRecordsAWeek(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, _ := seedMailbox(t, db, key, "ops@example.test", testSamples[:1])
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveMailKeepDays(db, 1); err != nil {
		t.Fatal(err)
	}
	recent := addProbeMail(t, db, key, mb.Address, &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().AddDate(0, 0, -3), Status: models.ProbeStatusArrived})
	old := addProbeMail(t, db, key, mb.Address, &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().AddDate(0, 0, -8), Status: models.ProbeStatusArrived})
	if _, err := jm.RunJob(context.Background(), mailboxJob(JobKindCleanup, mb, ""), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := models.GetProbeMail(db, recent.ID); err != nil {
		t.Errorf("a 3 day old record was removed: %v", err)
	}
	if _, err := models.GetProbeMail(db, old.ID); err != sql.ErrNoRows {
		t.Errorf("an 8 day old record stays: %v", err)
	}
}

// The send schedules the check the delay threshold after its start before
// the first mail goes out.
func TestSendProbesSchedulesCheckFirst(t *testing.T) {
	db, key, _, smtpSrv := setupProbe(t)
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: "a@example.test", ImapHost: "imap.example.test", ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatal(err)
	}
	mb.SmtpHost, mb.ProbeEnabled, mb.SmtpPort, mb.SmtpSecurity = smtpSrv.Host(), true, smtpSrv.Port(), IMAPSecurityNone
	if err := models.UpdateMailbox(db, mb); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeNextCheckAt, time.Now().Add(-time.Minute)) // an older check, due
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var during sql.NullTime
	res, err := SendProbes(context.Background(), db, key, "", "", func(string) {
		if s, _ := ResolveProbeSettings(db, nil); !during.Valid {
			during = s.NextCheckAt
		}
	})
	if err != nil || res.Sent != 1 {
		t.Fatalf("send: %+v %v", res, err)
	}
	if !during.Valid || during.Time.Before(start.Add(29*time.Minute)) {
		t.Errorf("the check during the send is due at %v", during)
	}
}

// A run of failed checks ends with a check that has nothing to look for, so
// the first failed check of a later outage starts a new run (no mail about
// an unreadable server at once).
func TestProbeFailureRunEndsWithoutReadingCheck(t *testing.T) {
	db, key, _, smtpSrv := setupProbe(t)
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPPort: num(closedPort(t)), IMAPPassword: str("probe-secret")}); err != nil {
		t.Fatal(err)
	}
	p := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-40 * time.Minute),
		Status: models.ProbeStatusDelayed, Level: models.ProbeLevelDelayed})
	ctx := context.Background()
	if _, err := CheckProbes(ctx, db, key, "", func(string) {}); err == nil {
		t.Fatal("the closed port was read")
	}
	if _, err := ResolveProbe(db, p.ID, "bob", models.ProbeReasonProbeServer, ""); err != nil {
		t.Fatal(err)
	}
	if res, err := CheckProbes(ctx, db, key, "", func(string) {}); err != nil || res.Skipped != SkipProbeNothingToSee {
		t.Fatalf("%+v %v", res, err)
	}
	s, _ := ResolveProbeSettings(db, nil)
	if s.CheckFailures != 0 || s.FailingSince.Valid || s.LastCheckError != "" || s.UnreadableNotifiedAt.Valid {
		t.Errorf("the run survived a skipped check: %+v", s)
	}
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().Add(-30 * time.Minute),
		Status: models.ProbeStatusPending})
	before := len(smtpSrv.Messages())
	if _, err := CheckProbes(ctx, db, key, "", func(string) {}); err == nil {
		t.Fatal("the closed port was read")
	}
	if n := len(smtpSrv.Messages()) - before; n != 0 {
		t.Errorf("the first failed check of a new outage mailed %d recipients", n)
	}
	// Switching the probe off and on ends a run as well.
	off, on := false, true
	if err := SaveProbeSettings(db, key, &ProbeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProbeSettings(db, key, &ProbeInput{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if s, _ := ResolveProbeSettings(db, nil); s.CheckFailures != 0 || s.FailingSince.Valid || s.LastCheckError != "" {
		t.Errorf("the run survived switching the probe off and on: %+v", s)
	}
}

// A check that fails for another reason than the probe server (a canceled
// job, the settings) is reported but does not count in the run.
func TestProbeOtherErrorsOutsideTheRun(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckProbes(ctx, db, key, "", func(string) {}); err == nil {
		t.Fatal("a canceled check succeeded")
	}
	s, _ := ResolveProbeSettings(db, nil)
	if s.CheckFailures != 0 || s.FailingSince.Valid || s.LastCheckError != "" || !s.NextCheckAt.Valid {
		t.Errorf("canceled: %+v", s)
	}
	// Settings that cannot be read (a wrong key): the error is kept, the
	// run is not touched, so its start does not move.
	since := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.set(SettingProbeCheckFailures, "2", false)
		u.setTime(SettingProbeCheckFailingSince, since)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wrongKey := make([]byte, len(key))
	for range 2 {
		if _, err := CheckProbes(context.Background(), db, wrongKey, "", func(string) {}); err == nil {
			t.Fatal("decrypted with a wrong key")
		}
	}
	s, _ = ResolveProbeSettings(db, nil)
	if s.CheckFailures != 2 || !s.FailingSince.Time.Equal(since) || s.LastCheckError == "" {
		t.Errorf("settings error: failures %d, since %v, error %q", s.CheckFailures, s.FailingSince, s.LastCheckError)
	}
}

// The probe stays on only while it is fully set up: its address, host,
// username and password cannot be removed while it is on.
func TestProbeSetupKeptWhileOn(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	str := func(v string) *string { return &v }
	wantWordingKey(t, SaveProbeSettings(db, key, &ProbeInput{Address: str("")}), "validation.probe.offFirst")
	wantWordingKey(t, SaveProbeSettings(db, key, &ProbeInput{ClearIMAPPassword: true}), "validation.probe.offFirst")
	off := false
	if err := SaveProbeSettings(db, key, &ProbeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProbeSettings(db, key, &ProbeInput{ClearIMAPPassword: true}); err != nil {
		t.Errorf("removing the password while off: %v", err)
	}
	if s, _ := ResolveProbeSettings(db, nil); s.PasswordSet {
		t.Error("the password stays")
	}
}

// A send or a check after a switch-off that raced it closes the open probe
// mails of the mailboxes that no longer send, and a mailbox switched off
// during a send sends nothing.
func TestProbeStaleOpenMailsClosed(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	p := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	mb, _ := models.GetMailboxByAddress(db, "a@example.test")
	mb.ProbeEnabled = false // as a switch-off that raced the send would leave it
	if err := models.UpdateMailbox(db, mb); err != nil {
		t.Fatal(err)
	}
	if res, err := CheckProbes(context.Background(), db, key, "", func(string) {}); err != nil || res.Skipped != SkipProbeNothingToSee {
		t.Errorf("check: %+v %v", res, err)
	}
	if got, _ := models.GetProbeMail(db, p.ID); got.Status != models.ProbeStatusResolved || got.ResolveReason != models.ProbeReasonProbeDisabled {
		t.Errorf("stale open mail: %+v", got)
	}
	if err := sendOneProbe(context.Background(), db, key, mb, "probe@example.test", ""); !errors.Is(err, errProbeSendSkipped) {
		t.Errorf("send for a mailbox switched off: %v", err)
	}
}

// The test send uses a stored SMTP password only for the stored address and
// connection.
func TestProbeSMTPTestStoredPasswordAddress(t *testing.T) {
	db, key, _, smtpSrv := setupProbe(t)
	host := "smtp.example.test"
	on := true
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: "a@example.test", ImapHost: "imap.example.test", ImapUsername: "u",
		ImapPassword: "p", SmtpHost: &host, ProbeEnabled: &on, SmtpPort: smtpSrv.Port(), SmtpUsername: "ops", SmtpPassword: "smtp-secret"})
	if err != nil {
		t.Fatal(err)
	}
	in := MailboxInputFrom(mb)
	in.Address = "other@example.test"
	if err := TestProbeSMTP(context.Background(), db, key, in); err != ErrSMTPConnectionPasswordRequired {
		t.Errorf("stored password for another address: %v", err)
	}
}

// The SMTP connection flags of the CLI need the send check on.
func TestSMTPFlagsNeedSendCheck(t *testing.T) {
	port := 587
	f := smtpConnectionFlags{SMTPPort: port}
	if err := f.apply(&MailboxInput{}, false); err != errSMTPFlagsWithoutProbe {
		t.Errorf("port without the send check: %v", err)
	}
	f.Probe = "on"
	in := &MailboxInput{}
	if err := f.apply(in, false); err != nil || in.SmtpPort != port || in.ProbeEnabled == nil || !*in.ProbeEnabled {
		t.Errorf("with --probe on: %v %+v", err, in)
	}
	off := smtpConnectionFlags{Probe: "off", SMTPSecurity: "ssl"}
	on := true
	if err := off.apply(&MailboxInput{ProbeEnabled: &on}, false); err != errSMTPFlagsWithoutProbe {
		t.Errorf("security with --probe off: %v", err)
	}
}

// A check with only send_failed mails open still reads the probe server:
// the late arrival recovers the mail.
func TestProbeOnlySendFailedOpen(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	failed := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().Add(-time.Hour),
		Status: models.ProbeStatusSendFailed, Level: models.ProbeLevelFailure, ErrorMessage: "EOF"})
	srv.deliver(t, probeRaw("a@example.test", "", failed.Token), time.Now())
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	p, _ := models.GetProbeMail(db, failed.ID)
	if err != nil || p.Status != models.ProbeStatusRecovered || res.Recovered != 1 || srv.count(t) != 0 || !res.NextAt.IsZero() {
		t.Errorf("check %+v %v, status %s, %d left", res, err, p.Status, srv.count(t))
	}
}

// A settings change while a check ran (another connection) keeps the run of
// failed checks out of the result of that check.
func TestProbeCheckIgnoresRunAfterSettingsMoved(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	before, _ := ResolveProbeSettings(db, key)
	str := func(v string) *string { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPHost: str("other.example.test"), IMAPPassword: str("x")}); err != nil {
		t.Fatal(err)
	}
	res := &ProbeCheckResult{}
	recordProbeCheck(context.Background(), db, key, "", before, res, errors.New("probe server: refused"), true, time.Now(), func(string) {})
	s, _ := ResolveProbeSettings(db, nil)
	if s.CheckFailures != 0 || s.FailingSince.Valid || s.LastCheckError != "" || !s.NextCheckAt.Valid {
		t.Errorf("run recorded against the new connection: %+v", s)
	}
}

// probeLog collects the IMAP commands an in-memory probe server receives.
type probeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *probeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// searches returns the UID SEARCH commands logged since the last call.
func (l *probeLog) searches() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.b.String(), "\r\n") {
		if strings.Contains(line, "UID SEARCH") && !strings.Contains(line, "OK UID SEARCH") {
			out = append(out, line)
		}
	}
	l.b.Reset()
	return out
}

// startProbeIMAPLog is startProbeIMAP with the commands logged.
func startProbeIMAPLog(t *testing.T) (*probeIMAP, *probeLog) {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("probe", "probe-secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	log := &probeLog{}
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		Logger:       quietIMAPLog{},
		DebugWriter:  log,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return &probeIMAP{user: user, host: host, port: port}, log
}

// A check lists the probe mailbox once: one UID SEARCH by the subject mark
// (SUBJECT only, no SINCE, no header criterion), which finds the probe
// mails of every identifier, however many there are; other mail is not
// listed.
func TestProbeServerOneListing(t *testing.T) {
	for _, n := range []int{0, 1, 120} {
		srv, log := startProbeIMAPLog(t)
		for i := range n {
			tag := DefaultProbeSubjectTag
			if i%2 == 1 {
				tag = "OLD"
			}
			srv.deliver(t, probeRawTag(tag, "a@example.test", "", fmt.Sprintf("abcdef%010x", i)), time.Now())
		}
		srv.deliver(t, "From: x@example.test\r\nTo: probe@example.test\r\nSubject: hello\r\n\r\nx\r\n", time.Now())
		conn := &models.Mailbox{ImapHost: srv.host, ImapPort: srv.port, ImapSecurity: IMAPSecurityNone, ImapUsername: "probe", Folder: "INBOX"}
		seen := map[string]bool{}
		_, err := mailengine.CheckProbeServer(context.Background(), conn, "probe-secret", ProbeHeader, ProbeSubjectMark,
			func(found []mailengine.ProbeMessage) (map[uint32]bool, error) {
				for _, m := range found {
					seen[m.Token] = true
				}
				return nil, nil
			})
		searches := log.searches()
		if err != nil || len(searches) != 1 || len(seen) != n {
			t.Errorf("n=%d: %v, searches %q, found %d", n, err, searches, len(seen))
			continue
		}
		if line := searches[0]; !strings.Contains(line, `SUBJECT "] Probe "`) || strings.Contains(line, "SINCE") ||
			strings.Contains(line, "BEFORE") || strings.Contains(line, "HEADER") {
			t.Errorf("n=%d: %s", n, line)
		}
	}
}

// A flood of mails made to look like probe mails ([MC:<random>] with a
// matching header) never hides a real arrival, and every check deletes the
// ones a day old (ProbeLeftoverMinAge) from its one listing, however many
// there are.
func TestProbeForgedFloodDrained(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	srv, log := startProbeIMAPLog(t)
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPPort: num(srv.port), IMAPPassword: str("probe-secret")}); err != nil {
		t.Fatal(err)
	}
	flood, extra := 530, 30
	leftover := time.Now().Add(-ProbeLeftoverMinAge - time.Hour)
	for i := range flood {
		srv.deliver(t, probeRaw("spam@example.test", "", fmt.Sprintf("eeeeeeee%08x", i)), leftover)
	}
	p := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-2 * time.Minute),
		Status: models.ProbeStatusPending})
	srv.deliver(t, probeRaw("a@example.test", "", p.Token), time.Now().Add(-time.Minute))
	ctx := context.Background()
	res, err := CheckProbes(ctx, db, key, "", func(string) {})
	if err != nil || res.Arrived != 1 || res.Deleted != flood+1 || res.Unrecorded != flood || srv.count(t) != 0 {
		t.Fatalf("first check: %+v %v, %d left", res, err, srv.count(t))
	}
	if searches := log.searches(); len(searches) != 1 {
		t.Errorf("first check searched %d times: %q", len(searches), searches)
	}
	// The next check deletes what came meanwhile: the old forged mails and
	// the new arrival; a forged mail younger than a day stays.
	for i := range extra {
		srv.deliver(t, probeRaw("spam@example.test", "", fmt.Sprintf("dddddddd%08x", i)), leftover)
	}
	srv.deliver(t, probeRaw("spam@example.test", "", "cccccccc00000001"), time.Now())
	q := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	srv.deliver(t, probeRaw("a@example.test", "", q.Token), time.Now())
	if res, err := CheckProbes(ctx, db, key, "", func(string) {}); err != nil || res.Arrived != 1 || res.Deleted != extra+1 ||
		srv.count(t) != 1 {
		t.Fatalf("second check: %+v %v, %d left", res, err, srv.count(t))
	}
	if searches := log.searches(); len(searches) != 1 {
		t.Errorf("second check searched %d times: %q", len(searches), searches)
	}
}

// A check deletes, of the unrecorded probe mails, only confirmed
// ones (subject "[<current identifier>:<n>]", the probe header holding n
// alone) that arrived ProbeLeftoverMinAge ago or earlier; a recorded mail of an earlier
// identifier is cleaned as a recorded one.
func TestProbeUnrecordedCleanup(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	old := time.Now().Add(-ProbeLeftoverMinAge - 24*time.Hour)
	raw := func(subject, header string) string {
		h := ""
		if header != "" {
			h = ProbeHeader + ": " + header + "\r\n"
		}
		return "From: x@example.test\r\nTo: probe@example.test\r\nSubject: " + subject + " 2026-10-15 07:00 x@example.test\r\n" + h + "\r\nx\r\n"
	}
	srv.deliver(t, raw("[MC:1111111111111111] Probe", "1111111111111111"), old)                   // deleted
	srv.deliver(t, raw("[MC:2222222222222222] Probe", "2222222222222222"), time.Now())            // too young
	srv.deliver(t, raw("[MC:3333333333333333] Probe", ""), old)                                   // no header
	srv.deliver(t, raw("[MC:4444444444444444] Probe", "5555555555555555"), old)                   // another number
	srv.deliver(t, raw("[XY:6666666666666666] Probe", "6666666666666666"), old)                   // another identifier
	srv.deliver(t, raw("[MC:7777777777777777] Probe", "7777777777777777, 7777777777777777"), old) // not a single value
	// A recorded (resolved) mail of an earlier identifier, sent by its
	// mailbox: cleaned as a recorded leftover.
	rec := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: "8888888888888888", SentAt: time.Now().Add(-time.Hour),
		Status: models.ProbeStatusResolved})
	srv.deliver(t, probeRawTag("OLD", "a@example.test", "", rec.Token), time.Now())
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Deleted != 2 || res.Unrecorded != 1 || srv.count(t) != 5 {
		t.Errorf("check: %+v %v, %d left", res, err, srv.count(t))
	}
	if p, _ := models.GetProbeMail(db, rec.ID); !p.DeletedAt.Valid {
		t.Error("the deletion of the recorded mail was not recorded")
	}
}

// A mail sent before the identifier was changed is still found, recorded
// and deleted; a subject with something in front ([SPAM]) matches; a subject
// whose number differs from the probe header does not.
func TestProbeIdentifierChangedAndSubjectForms(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	a := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	b := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	c := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(3), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	str := func(v string) *string { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{SubjectTag: str("NEW")}); err != nil {
		t.Fatal(err)
	}
	srv.deliver(t, probeRaw("a@example.test", "", a.Token), time.Now()) // sent as [MC:...]
	srv.deliver(t, buildMessage("a@example.test", []string{"probe@example.test"}, OutgoingMail{
		Subject: "[SPAM] " + probeSubject("MC", "2026-10-15 07:00", "a@example.test", b.Token), Body: "x",
		Headers: []MailHeader{{Name: ProbeHeader, Value: b.Token}}}, time.Now()), time.Now())
	srv.deliver(t, buildMessage("a@example.test", []string{"probe@example.test"}, OutgoingMail{
		Subject: probeSubject("MC", "2026-10-15 07:00", "a@example.test", c.Token), Body: "x",
		Headers: []MailHeader{{Name: ProbeHeader, Value: probeToken(9)}}}, time.Now()), time.Now())
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Arrived != 2 || res.Missing != 1 || res.Deleted != 2 || srv.count(t) != 1 {
		t.Errorf("check: %+v %v, %d left", res, err, srv.count(t))
	}
	if p, _ := models.GetProbeMail(db, c.ID); p.Status != models.ProbeStatusPending {
		t.Errorf("number mismatch accepted: %+v", p)
	}
}

// The identifier: 1 to 16 letters, digits, dots, underscores or hyphens; ""
// on the command line restores MC; changing it keeps a run of failed checks.
func TestProbeSubjectTag(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	str := func(v string) *string { return &v }
	for _, bad := range []string{"", "a:b", "[MC]", "has space", "12345678901234567", "é"} {
		wantWordingKey(t, SaveProbeSettings(db, key, &ProbeInput{SubjectTag: str(bad)}), "validation.probe.subjectTagInvalid")
	}
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.set(SettingProbeCheckFailures, "3", false)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProbeSettings(db, key, &ProbeInput{SubjectTag: str("Ops-1.x_2")}); err != nil {
		t.Fatal(err)
	}
	if s, _ := ResolveProbeSettings(db, nil); s.SubjectTag != "Ops-1.x_2" || s.CheckFailures != 3 {
		t.Errorf("tag %q, failures %d", s.SubjectTag, s.CheckFailures)
	}
	if shown, err := ApplySetting(db, SettingProbeSubjectTag, " OPS ", nil); err != nil || shown != "OPS" {
		t.Errorf("trimmed tag: %q %v", shown, err)
	}
	if s, _ := ResolveProbeSettings(db, nil); s.SubjectTag != "OPS" {
		t.Errorf("trimmed tag saved as %q", s.SubjectTag)
	}
	if _, err := ApplySetting(db, SettingProbeSubjectTag, "", nil); err != nil {
		t.Fatal(err)
	}
	if s, _ := ResolveProbeSettings(db, nil); s.SubjectTag != DefaultProbeSubjectTag {
		t.Errorf("default tag %q", s.SubjectTag)
	}
}

// A relay that puts a non-ASCII tag in front of the subject and re-encodes
// it (RFC 2047) does not hide the arrival.
func TestProbeEncodedSubject(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	p := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-time.Minute),
		Status: models.ProbeStatusPending})
	subject := "【迷惑メール】" + probeSubject(DefaultProbeSubjectTag, "2026-10-15 07:00", "a@example.test", p.Token)
	raw := "From: a@example.test\r\nTo: probe@example.test\r\nSubject: =?UTF-8?B?" +
		base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\n" + ProbeHeader + ": " + p.Token + "\r\n\r\nx\r\n"
	srv.deliver(t, raw, time.Now())
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Arrived != 1 || res.Deleted != 1 || srv.count(t) != 0 {
		t.Errorf("check: %+v %v, %d left", res, err, srv.count(t))
	}
}

// A recorded probe mail left on the probe server (an earlier deletion
// failed), whatever the age of its record, goes with the next check, which
// clears the deletion error and the time of the daily sweep of older
// versions; every check lists the probe mailbox once.
func TestProbeCheckDeletesRecordedLeftovers(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	srv, log := startProbeIMAPLog(t)
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPPort: num(srv.port), IMAPPassword: str("probe-secret")}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeWideSweepAt, time.Now().Add(-time.Hour))
		u.set(SettingProbeLastDeleteError, "boom", false)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recent := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(2), SentAt: time.Now().Add(-2 * time.Hour),
		Status: models.ProbeStatusResolved})
	older := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(3), SentAt: time.Now().AddDate(0, 0, -30),
		Status: models.ProbeStatusResolved})
	srv.deliver(t, probeRaw("a@example.test", "", recent.Token), time.Now().Add(-time.Hour))
	srv.deliver(t, probeRaw("a@example.test", "", older.Token), time.Now().AddDate(0, 0, -29))
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Deleted != 2 || res.Unrecorded != 0 || srv.count(t) != 0 {
		t.Errorf("check: %+v %v, %d left", res, err, srv.count(t))
	}
	if searches := log.searches(); len(searches) != 1 {
		t.Errorf("%d searches: %q", len(searches), searches)
	}
	for _, p := range []*models.ProbeMail{recent, older} {
		if got, _ := models.GetProbeMail(db, p.ID); !got.DeletedAt.Valid {
			t.Errorf("deletion of %s not recorded", p.Token)
		}
	}
	s, _ := ResolveProbeSettings(db, nil)
	if s.LastDeleteError != "" {
		t.Errorf("delete error %q stayed", s.LastDeleteError)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingProbeWideSweepAt); found {
		t.Error("the time of the daily sweep of older versions stayed")
	}
}

// Mails that look like probe mails but cannot be deleted (no probe header)
// do not hold back the unrecorded probe mails: one check deletes every
// unrecorded probe mail a day old at once.
func TestProbeSweepWindowNotStuck(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	old := time.Now().Add(-ProbeLeftoverMinAge - 24*time.Hour)
	stuck, unrecorded := 600, 700
	for i := range stuck {
		srv.deliver(t, fmt.Sprintf("From: x@example.test\r\nTo: probe@example.test\r\nSubject: [MC:%016x] Probe 2026-10-15 07:00 x@example.test\r\n\r\nx\r\n", i), old)
	}
	for i := range unrecorded {
		srv.deliver(t, probeRaw("b@example.test", "", fmt.Sprintf("ffff%012x", i)), old)
	}
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Unrecorded != unrecorded || res.Deleted != unrecorded {
		t.Errorf("sweep: %+v %v, %d left", res, err, srv.count(t))
	}
	if left := srv.count(t); left != uint32(stuck) {
		t.Errorf("%d left, want the %d without a probe header", left, stuck)
	}
}

// The identifier is compared as written: an unrecorded mail of "mc" is not
// deleted by the sweep of "MC".
func TestProbeSweepIdentifierCase(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	srv.deliver(t, probeRawTag("mc", "b@example.test", "", "ffffffffffffffff"), time.Now().Add(-ProbeLeftoverMinAge-24*time.Hour))
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Deleted != 0 || srv.count(t) != 1 {
		t.Errorf("lower-case identifier: %+v %v, %d left", res, err, srv.count(t))
	}
}

// An old copy with another sender of an open probe mail stays on the server
// (and is reported); the sweep deletes only copies of closed ones.
func TestProbeSweepKeepsOpenRecords(t *testing.T) {
	db, key, srv, _ := setupProbe(t)
	old := time.Now().Add(-ProbeLeftoverMinAge - 24*time.Hour)
	p := addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: old.Add(-time.Hour),
		Status: models.ProbeStatusFailure, Level: models.ProbeLevelFailure})
	srv.deliver(t, probeRaw("other@example.test", "", p.Token), old)
	res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
	if err != nil || res.Deleted != 0 || srv.count(t) != 1 || res.Mismatched != 1 {
		t.Errorf("open record with another sender: %+v %v, %d left", res, err, srv.count(t))
	}
}

// A check that read the probe server keeps the deletion error also when the
// settings changed meanwhile; only the run of failed checks is left alone.
func TestProbeCleanupStateKeptAfterSettingsMoved(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	before, _ := ResolveProbeSettings(db, key)
	str := func(v string) *string { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPHost: str("other.example.test"), IMAPPassword: str("x")}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.set(SettingProbeCheckFailures, "2", false)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res := &ProbeCheckResult{DeleteError: "boom"}
	recordProbeCheck(context.Background(), db, key, "", before, res, nil, false, time.Now(), func(string) {})
	s, _ := ResolveProbeSettings(db, nil)
	if s.LastDeleteError != "boom" || s.CheckFailures != 2 || s.LastCheckAt.Valid {
		t.Errorf("after the settings moved: %+v", s)
	}
}

// Every probe mail is mailed once when it becomes delayed and once when it
// becomes a failure, in one mail per recipient and check; one that goes
// straight to failure gets the failure mail only, one that arrives after its
// delay mail gets no more, and one the recipients could not get is mailed by
// a later check.
func TestProbeAlertMails(t *testing.T) {
	db, key, srv, smtpSrv := setupProbe(t)
	check := func(step string) []testkit.Message {
		t.Helper()
		before := len(smtpSrv.Messages())
		if _, err := CheckProbes(context.Background(), db, key, "", func(string) {}); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		return smtpSrv.Messages()[before:]
	}
	lists := func(msgs []testkit.Message, in, out []string) bool {
		for _, m := range msgs {
			for _, a := range in {
				if !strings.Contains(m.Data, a) {
					return false
				}
			}
			for _, a := range out {
				if strings.Contains(m.Data, a) {
					return false
				}
			}
		}
		return true
	}
	pending := func(address string, n int, ago time.Duration) *models.ProbeMail {
		return addProbeMail(t, db, key, address, &models.ProbeMail{Token: probeToken(n), SentAt: time.Now().Add(-ago),
			Status: models.ProbeStatusPending})
	}
	a := pending("box-a@example.test", 1, 40*time.Minute)
	b := pending("box-b@example.test", 2, 130*time.Minute)
	// One mail per recipient: the delay of a and the failure of b.
	if msgs := check("first"); len(msgs) != 2 || !lists(msgs, []string{"box-a@example.test", "box-b@example.test"}, nil) {
		t.Fatalf("first check: %d mails", len(msgs))
	}
	ga, _ := models.GetProbeMail(db, a.ID)
	gb, _ := models.GetProbeMail(db, b.ID)
	if !ga.DelayNotifiedAt.Valid || ga.NotifiedAt.Valid || !gb.NotifiedAt.Valid || !gb.DelayNotifiedAt.Valid {
		t.Errorf("marks: a %v/%v, b %v/%v", ga.DelayNotifiedAt.Valid, ga.NotifiedAt.Valid, gb.DelayNotifiedAt.Valid, gb.NotifiedAt.Valid)
	}
	if msgs := check("nothing new"); len(msgs) != 0 {
		t.Errorf("nothing new: %d mails", len(msgs))
	}
	// a becomes a failure: mailed once more, alone.
	if _, err := db.Exec(`UPDATE probe_mails SET sent_at = ? WHERE id = ?`, time.Now().Add(-130*time.Minute).UTC(), a.ID); err != nil {
		t.Fatal(err)
	}
	if msgs := check("failure of a"); len(msgs) != 2 || !lists(msgs, []string{"box-a@example.test"}, []string{"box-b@example.test"}) {
		t.Errorf("failure of a: %d mails", len(msgs))
	}
	// c is mailed as delayed, then arrives: no more mail.
	c := pending("box-c@example.test", 3, 40*time.Minute)
	if msgs := check("delay of c"); len(msgs) != 2 || !lists(msgs, []string{"box-c@example.test"}, []string{"box-a@example.test"}) {
		t.Errorf("delay of c: %d mails", len(msgs))
	}
	srv.deliver(t, probeRaw("box-c@example.test", "", c.Token), time.Now())
	if msgs := check("arrival of c"); len(msgs) != 0 {
		t.Errorf("arrival of c: %d mails", len(msgs))
	}
	// The delay of d cannot be mailed: it stays unmailed until a check can.
	d := pending("box-d@example.test", 4, 40*time.Minute)
	smtpSrv.RejectRecipients = []string{"admin@example.test", "bob@example.test"}
	if msgs := check("refused"); len(msgs) != 0 {
		t.Errorf("refused: %d mails", len(msgs))
	}
	if gd, _ := models.GetProbeMail(db, d.ID); gd.DelayNotifiedAt.Valid {
		t.Error("an undelivered delay mail was marked")
	}
	smtpSrv.RejectRecipients = nil
	if msgs := check("retry"); len(msgs) != 2 || !lists(msgs, []string{"box-d@example.test"}, []string{"box-c@example.test"}) {
		t.Errorf("retry: %d mails", len(msgs))
	}
}

// With the default thresholds a check while mails are missing schedules the
// next delay step after the latest send, and a check run by hand in between
// keeps it.
func TestProbeCheckStepsAfterSend(t *testing.T) {
	db, key, _, _ := setupProbe(t)
	num := func(v int) *int { return &v }
	if err := SaveProbeSettings(db, key, &ProbeInput{DelayMinutes: num(DefaultProbeDelayMinutes), FailureMinutes: num(DefaultProbeFailureMinutes)}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeLastSendAt, time.Now().Add(-20*time.Minute))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now().Add(-21 * time.Minute),
		Status: models.ProbeStatusPending})
	s, _ := ResolveProbeSettings(db, nil)
	want := s.LastSendAt.Time.Add(30 * time.Minute)
	for _, step := range []string{"first", "by hand"} {
		res, err := CheckProbes(context.Background(), db, key, "", func(string) {})
		if err != nil || res.Missing != 1 || !res.NextAt.Equal(want) {
			t.Errorf("%s: %+v %v, want next %v", step, res, err, want)
		}
	}
}

// A probe send or check and an analysis asked for by hand are refused when
// they could do nothing (ManualJobRefusal); other kinds are not.
func TestManualJobRefusal(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no agent CLI
	db, key, _, _ := setupProbe(t)
	key2 := func(err error) string {
		m, _ := wording.As(err)
		return m.Key
	}
	now := time.Now()
	if got := key2(ManualJobRefusal(db, JobKindProbeSend, now)); got != "result.probe.noMailbox" {
		t.Errorf("send without a mailbox: %q", got)
	}
	if got := key2(ManualJobRefusal(db, JobKindProbeCheck, now)); got != "result.probe.nothingOpen" {
		t.Errorf("check with nothing open: %q", got)
	}
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: now, Status: models.ProbeStatusPending})
	for _, kind := range []string{JobKindProbeSend, JobKindProbeCheck, JobKindSync, JobKindCleanup} {
		if err := ManualJobRefusal(db, kind, now); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if got := key2(ManualJobRefusal(db, JobKindAnalyze, now)); got != "result.agent.unavailable" {
		t.Errorf("analysis without the agent CLI: %q", got)
	}
	off := false
	if err := SaveProbeSettings(db, key, &ProbeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got := key2(ManualJobRefusal(db, JobKindProbeSend, now)); got != "result.probe.disabled" {
		t.Errorf("send while off: %q", got)
	}
}
