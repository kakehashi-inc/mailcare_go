package modules

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/wording"
)

// While probe mails are missing the arrival is checked every delay
// threshold after the latest send up to the failure threshold, then every
// failure threshold; a check run by hand in between keeps the steps.
func TestNextProbeCheckAt(t *testing.T) {
	send := localDate(2026, 10, 15, 7, 0, 0)
	at := func(h, m int) time.Time { return localDate(2026, 10, 15, h, m, 0) }
	delay, failure := time.Duration(DefaultProbeDelayMinutes)*time.Minute, time.Duration(DefaultProbeFailureMinutes)*time.Minute
	// The scheduled sequence with the defaults: 15, 30, 45, 60, 120, 180.
	checked := send
	for _, want := range []time.Time{at(7, 15), at(7, 30), at(7, 45), at(8, 0), at(9, 0), at(10, 0)} {
		if got := NextProbeCheckAt(send, checked, delay, failure); !got.Equal(want) {
			t.Errorf("after %v: got %v, want %v", checked, got, want)
		}
		checked = want
	}
	cases := []struct {
		name           string
		delay, failure time.Duration
		checked, want  time.Time
	}{
		{"by hand before the first step keeps it", delay, failure, at(7, 10), at(7, 15)},
		{"by hand between two steps keeps the next", delay, failure, at(7, 20), at(7, 30)},
		{"by hand after the last step: the failure time", delay, failure, at(7, 50), at(8, 0)},
		{"a late check catches up", delay, failure, at(9, 5), at(10, 0)},
		{"a send recorded after the check", delay, failure, at(6, 50), at(7, 15)},
		{"steps that do not divide the failure threshold", 25 * time.Minute, failure, at(7, 50), at(8, 0)},
		{"30 / 120", 30 * time.Minute, 2 * time.Hour, at(7, 30), at(8, 0)},
		{"30 / 120 after the steps", 30 * time.Minute, 2 * time.Hour, at(8, 30), at(9, 0)},
		{"30 / 120 after the failure time", 30 * time.Minute, 2 * time.Hour, at(9, 0), at(11, 0)},
	}
	for _, c := range cases {
		if got := NextProbeCheckAt(send, c.checked, c.delay, c.failure); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	checked = at(8, 0)
	if got := NextProbeCheckAt(time.Time{}, checked, delay, failure); !got.Equal(checked.Add(failure)) {
		t.Errorf("no recorded send: got %v", got)
	}
}

// The next check comes earlier when a missing mail reaches its next level
// before it; moments within a minute of each other (the mails of one send)
// and of the regular check are checked once.
func TestProbeLevelCheckAt(t *testing.T) {
	now := localDate(2026, 10, 15, 7, 30, 0)
	next := now.Add(30 * time.Minute)
	cases := []struct {
		name    string
		moments []time.Time
		want    time.Time
	}{
		{"none", nil, next},
		{"past moments only", []time.Time{now.Add(-time.Minute), now}, next},
		{"after the regular check", []time.Time{next.Add(time.Minute)}, next},
		{"earlier", []time.Time{now.Add(10 * time.Minute)}, now.Add(10 * time.Minute)},
		{"one send", []time.Time{now.Add(10*time.Minute + 20*time.Second), now.Add(10 * time.Minute), now.Add(10*time.Minute + 50*time.Second)},
			now.Add(10*time.Minute + 50*time.Second)},
		{"close to the regular check", []time.Time{next.Add(-30 * time.Second)}, next},
	}
	for _, c := range cases {
		if got := probeLevelCheckAt(next, c.moments, now); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestApplyProbeCheck(t *testing.T) {
	sent := localDate(2026, 10, 15, 7, 0, 0)
	delay, failure := 30*time.Minute, 2*time.Hour
	fresh := func() *models.ProbeMail {
		return &models.ProbeMail{SentAt: sent, Status: models.ProbeStatusPending}
	}

	// Missing, then delayed, then failed; the delay is updated every time.
	p := fresh()
	if lvl := ApplyProbeCheck(p, time.Time{}, sent.Add(10*time.Minute), delay, failure); lvl != "" || p.Status != models.ProbeStatusPending {
		t.Errorf("not overdue: %q %q", lvl, p.Status)
	}
	if lvl := ApplyProbeCheck(p, time.Time{}, sent.Add(30*time.Minute), delay, failure); lvl != models.ProbeLevelDelayed ||
		p.Status != models.ProbeStatusDelayed || p.DelaySeconds != 1800 {
		t.Errorf("delayed: %q %q %d", lvl, p.Status, p.DelaySeconds)
	}
	if lvl := ApplyProbeCheck(p, time.Time{}, sent.Add(2*time.Hour), delay, failure); lvl != models.ProbeLevelFailure ||
		p.Status != models.ProbeStatusFailure {
		t.Errorf("failure: %q %q", lvl, p.Status)
	}
	if lvl := ApplyProbeCheck(p, time.Time{}, sent.Add(4*time.Hour), delay, failure); lvl != "" || p.DelaySeconds != 4*3600 {
		t.Errorf("still failing: %q %d", lvl, p.DelaySeconds)
	}
	// It arrives: recovered, the level stays, the delay is the real one.
	if ApplyProbeCheck(p, sent.Add(3*time.Hour), sent.Add(6*time.Hour), delay, failure); p.Status != models.ProbeStatusRecovered ||
		p.Level != models.ProbeLevelFailure || p.DelaySeconds != 3*3600 || !p.ArrivedAt.Valid {
		t.Errorf("recovered: %+v", p)
	}

	// Arrived in time.
	p = fresh()
	if ApplyProbeCheck(p, sent.Add(2*time.Minute), sent.Add(30*time.Minute), delay, failure); p.Status != models.ProbeStatusArrived ||
		p.Level != models.ProbeLevelNone || p.DelaySeconds != 120 {
		t.Errorf("arrived: %+v", p)
	}
	// First looked for late (the check was handed over to a later send):
	// the real delay decides the level.
	p = fresh()
	if ApplyProbeCheck(p, sent.Add(45*time.Minute), sent.Add(5*time.Hour), delay, failure); p.Status != models.ProbeStatusRecovered ||
		p.Level != models.ProbeLevelDelayed {
		t.Errorf("arrived late, never marked: %+v", p)
	}
}

func TestProbeSendDue(t *testing.T) {
	times := []string{"07:00", "11:00"}
	fired := map[string]string{}
	last, now := localDate(2026, 10, 15, 6, 59, 40), localDate(2026, 10, 15, 7, 0, 10)
	due, ok := ProbeSendDue(last, now, times, fired)
	if !ok || due.minute != "2026-10-15 07:00" {
		t.Fatalf("due = %+v %v", due, ok)
	}
	fired[due.time] = due.minute
	if _, ok := ProbeSendDue(last, now, times, fired); ok {
		t.Error("fired twice")
	}
	// A tick spanning two send times sends once, for the latest.
	due, ok = ProbeSendDue(localDate(2026, 10, 15, 6, 0, 0), localDate(2026, 10, 15, 12, 0, 0), times, map[string]string{})
	if !ok || due.minute != "2026-10-15 11:00" {
		t.Errorf("latest = %+v %v", due, ok)
	}
}

func TestSaveProbeSettingsValidation(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	on := true
	wantKey := func(err error, key string) {
		t.Helper()
		m, ok := wording.As(err)
		if !ok || m.Key != key {
			t.Errorf("error = %v, want %s", err, key)
		}
	}

	s, err := ResolveProbeSettings(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.SendTimes, ",") != DefaultProbeSendTimes || s.DelayMinutes != DefaultProbeDelayMinutes ||
		s.FailureMinutes != DefaultProbeFailureMinutes || s.Enabled || s.Configured() {
		t.Fatalf("defaults: %+v", s)
	}
	wantKey(SaveProbeSettings(db, key, &ProbeInput{Enabled: &on}), "result.probe.notConfigured")
	wantKey(SaveProbeSettings(db, key, &ProbeInput{DelayMinutes: num(120)}), "validation.probe.failureNotAfterDelay")
	wantKey(SaveProbeSettings(db, key, &ProbeInput{FailureMinutes: num(0)}), "validation.common.numberOutOfRange")
	wantKey(SaveProbeSettings(db, key, &ProbeInput{Address: str("not an address")}), "validation.common.emailFormat")
	wantKey(SaveProbeSettings(db, key, &ProbeInput{SendTimes: &[]string{"25:00"}}), "validation.common.timeFormat")

	if err := SaveProbeSettings(db, key, &ProbeInput{
		Address: str("Probe@Example.test"), IMAPHost: str("imap.example.test"), IMAPUsername: str("probe"),
		IMAPPassword: str("secret"), SendTimes: &[]string{"19:00", "07:00"}, DelayMinutes: num(20), FailureMinutes: num(90),
		Enabled: &on,
	}); err != nil {
		t.Fatal(err)
	}
	s, _ = ResolveProbeSettings(db, key)
	if !s.Enabled || !s.Configured() || s.Address != "probe@example.test" || s.Password != "secret" ||
		strings.Join(s.SendTimes, ",") != "07:00,19:00" || s.DelayMinutes != 20 || s.FailureMinutes != 90 {
		t.Errorf("saved: %+v", s)
	}
	// The stored password is never pointed at another server.
	wantKey(SaveProbeSettings(db, key, &ProbeInput{IMAPHost: str("other.example.test")}), "validation.connection.passwordRequiredOnChange")
	if err := SaveProbeSettings(db, key, &ProbeInput{IMAPHost: str("other.example.test"), IMAPPassword: str("new")}); err != nil {
		t.Errorf("with a password: %v", err)
	}
	// An empty list means no scheduled send (not the default).
	if err := SaveProbeSettings(db, key, &ProbeInput{SendTimes: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	if s, _ = ResolveProbeSettings(db, nil); len(s.SendTimes) != 0 {
		t.Errorf("empty send times: %v", s.SendTimes)
	}
}

func TestMailboxSMTPPasswordGuard(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	host := "smtp.example.test"
	on := true
	base := func(addr string) *MailboxInput {
		return &MailboxInput{Address: addr, ImapHost: "imap.example.test", ImapUsername: "ops", ImapPassword: "p"}
	}
	// The send check needs a host and a port; the authentication is optional.
	in := base("a@example.test")
	in.ProbeEnabled, in.SmtpPort = &on, 587
	if _, err := CreateMailbox(db, key, in); err != errSMTPHostRequiredForProbe {
		t.Errorf("send check without host: %v", err)
	}
	in = base("a@example.test")
	in.SmtpHost, in.ProbeEnabled = &host, &on
	if _, err := CreateMailbox(db, key, in); err != errSMTPPortRequired {
		t.Errorf("send check without port: %v", err)
	}
	// Off (the default), only the host is kept: it serves the judgement.
	in = base("b@example.test")
	in.SmtpHost, in.SmtpPort, in.SmtpUsername, in.SmtpPassword = &host, 2525, "ops", "x"
	hostOnly, err := CreateMailbox(db, key, in)
	if err != nil || hostOnly.SmtpHost != host || hostOnly.ProbeEnabled || hostOnly.SmtpPort != 0 || hostOnly.SmtpUsername != "" ||
		hostOnly.SmtpPasswordEnc != "" || ProbeParticipates(hostOnly) {
		t.Fatalf("host only: %+v %v", hostOnly, err)
	}
	in = base("ops@example.test")
	in.SmtpHost, in.ProbeEnabled, in.SmtpPort, in.SmtpUsername, in.SmtpPassword = &host, &on, 587, "ops", "smtp-secret"
	mb, err := CreateMailbox(db, key, in)
	if err != nil {
		t.Fatal(err)
	}
	if mb.SmtpPort != 587 || mb.SmtpSecurity != DefaultSMTPSecurity || mb.SmtpPasswordEnc == "" || !ProbeParticipates(mb) {
		t.Fatalf("send check: %+v", mb)
	}
	stored, _ := models.GetMailboxByID(db, mb.ID)
	// Unchanged connection: the stored password is kept.
	in = MailboxInputFrom(stored)
	if err := UpdateMailbox(db, key, t.TempDir(), t.TempDir(), stored, in); err != nil {
		t.Fatal(err)
	}
	stored, _ = models.GetMailboxByID(db, mb.ID)
	if pw, _ := DecryptSecret(key, stored.SmtpPasswordEnc); pw != "smtp-secret" {
		t.Errorf("password not kept: %q", pw)
	}
	// Another host without a password is refused.
	in = MailboxInputFrom(stored)
	other := "other.example.test"
	in.SmtpHost = &other
	if err := UpdateMailbox(db, key, t.TempDir(), t.TempDir(), stored, in); err != ErrSMTPConnectionPasswordRequired {
		t.Errorf("changed host: %v", err)
	}
	// A partial update without the send check keeps it.
	stored, _ = models.GetMailboxByID(db, mb.ID)
	partial := &MailboxInput{Address: stored.Address, ImapHost: stored.ImapHost, ImapUsername: stored.ImapUsername}
	if err := UpdateMailbox(db, key, t.TempDir(), t.TempDir(), stored, partial); err != nil {
		t.Fatal(err)
	}
	if stored, _ = models.GetMailboxByID(db, mb.ID); !stored.ProbeEnabled || stored.SmtpPort != 587 || stored.SmtpPasswordEnc == "" {
		t.Errorf("partial update dropped the send check: %+v", stored)
	}
	// Switching it off keeps the host and drops the rest of the connection.
	in = MailboxInputFrom(stored)
	off := false
	in.ProbeEnabled = &off
	if err := UpdateMailbox(db, key, t.TempDir(), t.TempDir(), stored, in); err != nil {
		t.Fatal(err)
	}
	stored, _ = models.GetMailboxByID(db, mb.ID)
	if stored.SmtpHost != host || stored.SmtpPasswordEnc != "" || stored.SmtpPort != 0 || stored.ProbeEnabled || ProbeParticipates(stored) {
		t.Errorf("not switched off: %+v", stored)
	}
}

// probeIMAP is an in-memory IMAP server standing in for the probe server.
type probeIMAP struct {
	user *imapmemserver.User
	host string
	port int
}

type probeLiteral struct{ *bytes.Reader }

// quietIMAPLog drops the error log of the fake IMAP servers (a client that
// closes right after LOGOUT makes the reply fail to write).
type quietIMAPLog struct{}

func (quietIMAPLog) Printf(string, ...any) {}

func (l probeLiteral) Size() int64 { return int64(l.Reader.Len()) }

func startProbeIMAP(t *testing.T) *probeIMAP {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("probe", "probe-secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		Logger:       quietIMAPLog{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return &probeIMAP{user: user, host: host, port: port}
}

// count returns the number of messages in the INBOX of the probe server.
func (p *probeIMAP) count(t *testing.T) uint32 {
	t.Helper()
	st, err := p.user.Status("INBOX", &imap.StatusOptions{NumMessages: true})
	if err != nil || st.NumMessages == nil {
		t.Fatalf("status: %v", err)
	}
	return *st.NumMessages
}

func (p *probeIMAP) deliver(t *testing.T, raw string, at time.Time) {
	t.Helper()
	if _, err := p.user.Append("INBOX", probeLiteral{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: at}); err != nil {
		t.Fatal(err)
	}
}

func TestProbeEndToEnd(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	progress := func(string) {}
	smtpSrv := startFakeSMTP(t)
	probeSrv := startProbeIMAP(t)
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }

	// Nothing to do before the probe server is set.
	if res, err := SendProbes(ctx, db, key, "", "", progress); err != nil || res.Skipped != SkipProbeNotConfigured {
		t.Fatalf("not configured: %+v %v", res, err)
	}
	on := true
	if err := SaveProbeSettings(db, key, &ProbeInput{
		Address: str("probe@example.test"), IMAPHost: str(probeSrv.host), IMAPPort: num(probeSrv.port),
		IMAPSecurity: str(IMAPSecurityNone), IMAPUsername: str("probe"), IMAPPassword: str("probe-secret"),
		DelayMinutes: num(30), FailureMinutes: num(120), Enabled: &on,
	}); err != nil {
		t.Fatal(err)
	}
	if err := TestProbeServer(ctx, db, key, nil); err != nil {
		t.Fatalf("TestProbeServer: %v", err)
	}
	if res, _ := SendProbes(ctx, db, key, "", "", progress); res.Skipped != SkipProbeNoMailbox {
		t.Fatalf("no mailbox: %+v", res)
	}

	// Two mailboxes send through the fake SMTP server; a disabled one and
	// one without an SMTP server do not. The host of the fake server is an
	// IP address, which smtp_host does not accept as input, so it is set on
	// the rows directly.
	for _, addr := range []string{"a@example.test", "b@example.test", "off@example.test"} {
		in := &MailboxInput{Address: addr, ImapHost: "imap.example.test", ImapUsername: addr, ImapPassword: "p"}
		if addr == "off@example.test" {
			off := false
			in.Enabled = &off
		}
		mb, err := CreateMailbox(db, key, in)
		if err != nil {
			t.Fatal(err)
		}
		mb.SmtpHost, mb.ProbeEnabled, mb.SmtpPort, mb.SmtpSecurity = smtpSrv.Host(), true, smtpSrv.Port(), IMAPSecurityNone
		if err := models.UpdateMailbox(db, mb); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CreateMailbox(db, key, &MailboxInput{Address: "c@example.test", ImapHost: "imap.example.test", ImapUsername: "c", ImapPassword: "p"}); err != nil {
		t.Fatal(err)
	}
	res, err := SendProbes(ctx, db, key, "http://mailcare.test", "2026-10-15 07:00", progress)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("send: %+v %v", res, err)
	}
	sent := smtpSrv.Messages()
	if len(sent) != 2 {
		t.Fatalf("SMTP got %d mails", len(sent))
	}
	for _, m := range sent {
		subject := regexp.MustCompile(`\r\nSubject: \[MC:([0-9a-f]{16})\] Probe 2026-10-15 07:00 ` + regexp.QuoteMeta(m.From) + `\r\n`).FindStringSubmatch(m.Data)
		if len(m.To) != 1 || m.To[0] != "probe@example.test" || subject == nil || !strings.Contains(m.Data, "\r\n"+ProbeHeader+": "+subject[1]+"\r\n") {
			t.Errorf("probe mail from %s:\n%s", m.From, m.Data)
		}
	}
	s, _ := ResolveProbeSettings(db, nil)
	if !s.LastSendAt.Valid || !s.NextCheckAt.Valid || s.NextCheckAt.Time.Sub(s.LastSendAt.Time) != 30*time.Minute {
		t.Errorf("check not scheduled: %+v", s)
	}

	// The failure mail goes to the notification recipients.
	admin, _ := CreateUserFrom(db, NewUser{Username: "admin", Email: "admin@example.test", Password: "password123", Role: RoleAdmin})
	ids := []int64{admin.ID}
	if err := SaveNotificationSettings(db, key, &NotificationInput{SMTPHost: str(smtpSrv.Host()), SMTPPort: num(smtpSrv.Port()),
		SMTPSecurity: str(IMAPSecurityNone), SMTPFrom: str("mailcare@example.test"), UserIDs: &ids}); err != nil {
		t.Fatal(err)
	}

	// The mail of a arrives after a minute; b was sent three hours ago and
	// never arrives.
	waiting, _ := models.ListWaitingProbeMails(db)
	var pa, pb *models.ProbeMail
	for _, p := range waiting {
		if p.Address == "a@example.test" {
			pa = p
		} else {
			pb = p
		}
	}
	for _, m := range sent {
		if m.From == "a@example.test" {
			probeSrv.deliver(t, m.Data, pa.SentAt.Add(time.Minute))
		}
	}
	// Other mail of the probe mailbox is never touched: a reply that quotes
	// the subject of a probe mail (no probe header) stays.
	for _, m := range sent {
		if m.From == "a@example.test" {
			subject := m.Data[strings.Index(m.Data, "Subject: ")+len("Subject: "):]
			subject = subject[:strings.Index(subject, "\r\n")]
			probeSrv.deliver(t, "From: someone@example.test\r\nTo: probe@example.test\r\nSubject: Re: "+subject+
				"\r\nMessage-ID: <reply@example.test>\r\n\r\nthanks\r\n", time.Now().Add(-48*time.Hour))
		}
	}
	// A probe-shaped message of another identifier whose token was never
	// recorded stays as well, however old it is.
	forged := "0123456789abcdef"
	probeSrv.deliver(t, "From: a@example.test\r\nTo: probe@example.test\r\nSubject: [OTHER:"+forged+"] Probe 2026-01-01 07:00 a@example.test"+
		"\r\n"+ProbeHeader+": "+forged+"\r\nMessage-ID: <forged@example.test>\r\n\r\nx\r\n", time.Now().Add(-72*time.Hour))
	if _, err := db.Exec(`UPDATE probe_mails SET sent_at = ? WHERE id = ?`, time.Now().Add(-3*time.Hour).UTC(), pb.ID); err != nil {
		t.Fatal(err)
	}
	check, err := CheckProbes(ctx, db, key, "http://mailcare.test", progress)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.Checked != 2 || check.Arrived != 1 || check.Failed != 1 || check.Missing != 1 || check.NextAt.IsZero() {
		t.Errorf("check: %+v", check)
	}
	pa, _ = models.GetProbeMail(db, pa.ID)
	pb, _ = models.GetProbeMail(db, pb.ID)
	// INTERNALDATE has whole seconds.
	if pa.Status != models.ProbeStatusArrived || pa.DelaySeconds < 59 || pa.DelaySeconds > 60 {
		t.Errorf("a: %+v", pa)
	}
	if pb.Status != models.ProbeStatusFailure || pb.Level != models.ProbeLevelFailure || !pb.NotifiedAt.Valid {
		t.Errorf("b: %+v", pb)
	}
	// The confirmed mail of a is deleted from the probe server; the reply
	// and the unrecorded one stay.
	if check.Deleted != 1 || probeSrv.count(t) != 2 {
		t.Errorf("deleted %d, %d left on the probe server", check.Deleted, probeSrv.count(t))
	}
	all := smtpSrv.Messages()
	if len(all) != 3 || all[2].To[0] != "admin@example.test" || !strings.Contains(all[2].Data, "b@example.test") ||
		strings.Contains(all[2].Data, "a@example.test\r\n") {
		t.Errorf("failure mail: %d mails\n%s", len(all), all[len(all)-1].Data)
	}
	// A failure is mailed once.
	if _, err := CheckProbes(ctx, db, key, "", progress); err != nil {
		t.Fatal(err)
	}
	if len(smtpSrv.Messages()) != 3 {
		t.Errorf("failure mailed again")
	}

	// b arrives at last: recovered, still marked as a failure.
	for _, m := range sent {
		if m.From == "b@example.test" {
			probeSrv.deliver(t, m.Data, time.Now())
		}
	}
	check, _ = CheckProbes(ctx, db, key, "", progress)
	pb, _ = models.GetProbeMail(db, pb.ID)
	if check.Recovered != 1 || pb.Status != models.ProbeStatusRecovered || pb.Level != models.ProbeLevelFailure || !check.NextAt.IsZero() ||
		check.Deleted != 1 || probeSrv.count(t) != 2 {
		t.Errorf("recovered: %+v %+v", check, pb)
	}
	if s, _ = ResolveProbeSettings(db, nil); s.NextCheckAt.Valid {
		t.Errorf("a check is still scheduled with nothing missing: %v", s.NextCheckAt)
	}
	// A closed mail cannot be resolved by hand.
	if _, err := ResolveProbe(db, pb.ID, "admin", models.ProbeReasonServerRecovered, ""); err == nil {
		t.Error("resolved a recovered mail")
	}

	// A mail that cannot be sent is recorded and can be resolved by hand.
	smtpSrv.Close()
	res, _ = SendProbes(ctx, db, key, "", "", progress)
	if res.Failed != 2 {
		t.Fatalf("send to a closed server: %+v", res)
	}
	open, _, _ := models.ListProbeMails(db, models.ProbeFilterOpen, 0, 0, 10)
	if len(open) != 2 || open[0].Status != models.ProbeStatusSendFailed || open[0].ErrorMessage == "" || !open[0].Manual || open[0].Slot == "" {
		t.Fatalf("send failures: %+v", open)
	}
	if _, err := ResolveProbe(db, open[0].ID, "admin", "nope", ""); err == nil {
		t.Error("unknown reason accepted")
	}
	p, err := ResolveProbe(db, open[0].ID, "admin", models.ProbeReasonConfigError, " fixed the port ")
	if err != nil || p.Status != models.ProbeStatusResolved || p.ResolvedBy != "admin" || p.ResolveNote != "fixed the port" ||
		p.Level != models.ProbeLevelFailure {
		t.Errorf("resolved: %+v %v", p, err)
	}
	counts, _ := models.CountProbeMails(db, 0, time.Now().AddDate(0, 0, -1))
	if counts.SendFailed != 1 || counts.Resolved != 1 || counts.Recovered != 1 {
		t.Errorf("counts: %+v", counts)
	}
}

// The scheduler queues the send at a send time and the check once it is due.
func TestSchedulerQueuesProbeJobs(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	str := func(v string) *string { return &v }
	on := true
	if err := SaveProbeSettings(db, key, &ProbeInput{Address: str("probe@example.test"), IMAPHost: str("imap.example.test"),
		IMAPUsername: str("probe"), IMAPPassword: str("x"), SendTimes: &[]string{"07:00"}, Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	// A mailbox that sends probe mails, with one open probe mail (a send and
	// a check have something to do).
	addProbeMail(t, db, key, "a@example.test", &models.ProbeMail{Token: probeToken(1), SentAt: time.Now(), Status: models.ProbeStatusPending})
	s := NewScheduler(db, jm)
	clock := localDate(2026, 10, 15, 6, 59, 50)
	s.now = func() time.Time { return clock }
	s.lastTick = clock
	clock = clock.Add(20 * time.Second)
	s.Tick()
	probeJobs := func() []*models.Job {
		active, _ := models.ListActiveJobs(db)
		var out []*models.Job
		for _, j := range active {
			if j.Kind == JobKindProbeSend || j.Kind == JobKindProbeCheck {
				out = append(out, j)
			}
		}
		return out
	}
	active := probeJobs()
	if len(active) != 1 || active[0].Kind != JobKindProbeSend || active[0].Target != "2026-10-15 07:00" {
		t.Fatalf("send not queued: %+v", active)
	}
	// A due check waits while the send is queued or running (the send
	// moves the check).
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeNextCheckAt, clock.Add(-time.Second))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET status = 'running', started_at = ? WHERE kind = 'probe_send'`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	s.Tick()
	countChecks := func() int {
		n := 0
		for _, j := range probeJobs() {
			if j.Kind == JobKindProbeCheck {
				n++
			}
		}
		return n
	}
	if n := countChecks(); n != 0 {
		t.Errorf("a check was queued while the send runs: %d", n)
	}
	// Once the send is over, a due check is queued once (an identical
	// active job is reused).
	if _, err := db.Exec(`UPDATE jobs SET status = 'done', finished_at = ? WHERE kind = 'probe_send'`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	s.Tick()
	s.Tick()
	if n := countChecks(); n != 1 {
		t.Errorf("checks queued: %d", n)
	}
	// The queued check re-reads the time when it starts: moved by a send in
	// between, it does nothing.
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeNextCheckAt, time.Now().Add(time.Hour))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var check *models.Job
	for _, j := range probeJobs() {
		if j.Kind == JobKindProbeCheck {
			check = j
		}
	}
	line, err := jm.RunJob(context.Background(), check, func(string) {})
	if err != nil || line != "skipped: "+SkipProbeCheckNotDue {
		t.Errorf("moved check: %q %v", line, err)
	}
	// Switched off: nothing more.
	off := false
	if err := SaveProbeSettings(db, key, &ProbeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM jobs`); err != nil {
		t.Fatal(err)
	}
	clock = localDate(2026, 10, 16, 7, 0, 5)
	s.lastTick = clock.Add(-30 * time.Second)
	s.Tick()
	if active = probeJobs(); len(active) != 0 {
		t.Errorf("queued while off: %+v", active)
	}
}

// Verified probe mails (recorded token, recorded sender) are arrivals and
// are deleted. Any other probe mail is deleted only when it carries the
// current identifier, is ProbeLeftoverMinAge old and its token is no open
// record; a recorded token found only with another sender is reported.
func TestSortProbeMessages(t *testing.T) {
	now := time.Now()
	tok := func(s string) string { return "00000000000000" + s }
	recorded := map[string]*models.ProbeMail{
		tok("aa"): {Token: tok("aa"), Address: "a@example.test", Status: models.ProbeStatusPending},
		tok("dd"): {Token: tok("dd"), Address: "d@example.test", Status: models.ProbeStatusPending},
		tok("ee"): {Token: tok("ee"), Address: "e@example.test", Status: models.ProbeStatusDelayed},
		tok("99"): {Token: tok("99"), Address: "g@example.test", Status: models.ProbeStatusArrived},
	}
	day := 24 * time.Hour
	old := now.Add(-ProbeLeftoverMinAge - day)
	found := []mailengine.ProbeMessage{
		{UID: 1, Token: tok("aa"), Identifier: "MC", From: "A@example.test", ReceivedAt: now.Add(-2 * time.Minute)},
		{UID: 2, Token: tok("aa"), Identifier: "MC", From: "a@example.test", ReceivedAt: now.Add(-time.Minute)}, // the same mail twice
		{UID: 3, Token: tok("aa"), Identifier: "MC", From: "other@example.test", ReceivedAt: old},               // open row: kept
		{UID: 4, Token: tok("bb"), Identifier: "MC", From: "b@example.test", ReceivedAt: now.Add(-time.Hour)},   // too young
		{UID: 5, Token: tok("cc"), Identifier: "MC", From: "c@example.test", ReceivedAt: now.Add(-400 * day)},   // unrecorded, old
		{UID: 6, Token: tok("dd"), Identifier: "MC", From: "d@example.test"},                                    // no arrival time
		{UID: 7, Token: tok("ee"), Identifier: "MC", From: "bounce@relay.example.test", ReceivedAt: now.Add(-time.Minute)},
		{UID: 8, Token: tok("ff"), Identifier: "OTHER", From: "f@example.test", ReceivedAt: old}, // another identifier
		{UID: 9, Token: tok("99"), Identifier: "MC", From: "x@example.test", ReceivedAt: old},    // closed row, old
		{UID: 10, Token: tok("cc"), Identifier: "MC", From: "c@example.test"},                    // no time: kept
	}
	got := sortProbeMessages(found, recorded, "MC", now)
	if len(got.arrivals) != 2 || !got.arrivals[tok("aa")].Equal(now.Add(-2*time.Minute)) || !got.arrivals[tok("dd")].Equal(now) {
		t.Errorf("arrivals: %v", got.arrivals)
	}
	want := map[uint32]bool{1: true, 2: true, 5: true, 6: true, 9: true}
	if len(got.remove) != len(want) {
		t.Errorf("remove: %v", got.remove)
	}
	for uid := range want {
		if !got.remove[uid] {
			t.Errorf("remove: %v, missing %d", got.remove, uid)
		}
	}
	if len(got.verified) != 3 || got.verified[2] != tok("aa") || got.verified[6] != tok("dd") ||
		len(got.unrecorded) != 2 || !got.unrecorded[5] || !got.unrecorded[9] {
		t.Errorf("verified %v, unrecorded %v", got.verified, got.unrecorded)
	}
	// aa has a verified copy, so its odd sender is no mismatch to report.
	if got.mismatched[tok("ee")] != "bounce@relay.example.test" || got.mismatched[tok("aa")] != "" {
		t.Errorf("mismatched: %v", got.mismatched)
	}
	// Every unrecorded one old enough goes, however many.
	var many []mailengine.ProbeMessage
	const count = 1200
	for i := range count {
		many = append(many, mailengine.ProbeMessage{UID: uint32(i + 1), Token: fmt.Sprintf("1%015x", i), Identifier: "MC",
			From: "x@example.test", ReceivedAt: old})
	}
	many = append(many, mailengine.ProbeMessage{UID: 9999, Token: tok("aa"), Identifier: "MC", From: "a@example.test", ReceivedAt: now})
	got = sortProbeMessages(many, recorded, "MC", now)
	if len(got.unrecorded) != count || !got.remove[9999] || len(got.remove) != count+1 {
		t.Errorf("many: %d unrecorded, %d to remove", len(got.unrecorded), len(got.remove))
	}
}

// After a partly failed deletion only the records whose every verified copy
// was deleted are marked, and only the unrecorded mails deleted are counted.
func TestProbeSortDeletedOf(t *testing.T) {
	s := probeSort{verified: map[uint32]string{1: "a", 2: "a", 3: "b"}, unrecorded: map[uint32]bool{4: true, 5: true}}
	gone, unrecorded := s.deletedOf([]mailengine.ProbeMessage{{UID: 1}, {UID: 3}, {UID: 4}})
	if len(gone) != 1 || gone[0] != "b" || unrecorded != 1 {
		t.Errorf("partly deleted: %v, %d unrecorded", gone, unrecorded)
	}
	gone, unrecorded = s.deletedOf([]mailengine.ProbeMessage{{UID: 1}, {UID: 2}, {UID: 3}, {UID: 4}, {UID: 5}})
	if len(gone) != 2 || unrecorded != 2 {
		t.Errorf("all deleted: %v, %d unrecorded", gone, unrecorded)
	}
	if gone, unrecorded = s.deletedOf(nil); len(gone) != 0 || unrecorded != 0 {
		t.Errorf("none deleted: %v, %d unrecorded", gone, unrecorded)
	}
}

// The records of the probe mails of a mailbox older than the mail retention
// are removed whatever their status; the newer ones and the other
// mailboxes' stay.
func TestDeleteOldProbeMails(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, addr := range []string{"a@example.test", "b@example.test"} {
		mb, err := CreateMailbox(db, key, &MailboxInput{Address: addr, ImapHost: "imap.example.test", ImapUsername: "u", ImapPassword: "p"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, mb.ID)
	}
	now := time.Now()
	old := now.AddDate(0, 0, -DefaultMailKeepDays-1)
	insert := func(mailboxID int64, token, status string, sent time.Time) {
		if err := models.InsertProbeMail(db, &models.ProbeMail{MailboxID: mailboxID, Address: "x@example.test",
			Token: token, Slot: "2026-01-01 07:00", Subject: "s", SentAt: sent, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	insert(ids[0], "01", models.ProbeStatusArrived, old)
	insert(ids[0], "02", models.ProbeStatusResolved, old)
	insert(ids[0], "03", models.ProbeStatusFailure, old)
	insert(ids[0], "04", models.ProbeStatusArrived, now)
	insert(ids[1], "05", models.ProbeStatusArrived, old)
	n, err := models.DeleteOldProbeMails(db, ids[0], now.AddDate(0, 0, -DefaultMailKeepDays))
	if err != nil || n != 3 {
		t.Fatalf("removed %d: %v", n, err)
	}
	if _, total, _ := models.ListProbeMails(db, models.ProbeFilterAll, 0, 0, 10); total != 2 {
		t.Errorf("left %d records", total)
	}
}

// The error text of a probe mail that could not be sent names the SMTP
// server: the alert mail carries it to administrators only. The subject
// counts the failures and the delays.
func TestRenderProbeAlertMail(t *testing.T) {
	failures := []*models.ProbeMail{{Address: "a@example.test", Slot: "2026-10-15 07:00", SentAt: time.Now(),
		Status: models.ProbeStatusSendFailed, ErrorMessage: "connect to smtp.example.test:587: refused"}}
	delays := []*models.ProbeMail{{Address: "d@example.test", Slot: "2026-10-15 07:00", SentAt: time.Now(),
		Status: models.ProbeStatusDelayed, DelaySeconds: 20 * 60}}
	subjects := map[string][3]string{
		"ja": {"確認メールの障害 1 件・遅延 1 件", "確認メールの障害 1 件（", "確認メールの遅延 1 件（"},
		"en": {"Delivery probe failure: 1, delay: 1 (", "Delivery probe failure: 1 (", "Delivery probe delay: 1 ("},
	}
	for _, lang := range []string{"ja", "en"} {
		loc := MailLocale{Language: lang, Location: time.UTC}
		_, admin, err := renderProbeAlertMail(nil, failures, "http://mailcare.test", loc, true)
		if err != nil || !strings.Contains(admin, "smtp.example.test") || !strings.Contains(admin, "a@example.test") {
			t.Errorf("%s admin: %v\n%s", lang, err, admin)
		}
		_, member, err := renderProbeAlertMail(nil, failures, "http://mailcare.test", loc, false)
		if err != nil || strings.Contains(member, "smtp.example.test") || !strings.Contains(member, "a@example.test") {
			t.Errorf("%s member: %v\n%s", lang, err, member)
		}
		for i, c := range [][2][]*models.ProbeMail{{delays, failures}, {nil, failures}, {delays, nil}} {
			subject, body, err := renderProbeAlertMail(c[0], c[1], "http://mailcare.test", loc, false)
			if err != nil || !strings.Contains(subject, subjects[lang][i]) || strings.Contains(body, "d@example.test") != (c[0] != nil) {
				t.Errorf("%s case %d: %q %v\n%s", lang, i, subject, err, body)
			}
		}
	}
}
