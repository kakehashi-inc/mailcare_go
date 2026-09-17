package mailengine

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"mailcare/app/models"
)

const (
	memUsername = "newsletter@example.jp"
	memPassword = "secret"
	memFolder   = "INBOX"
)

// memIMAP is an in-memory IMAP server listening on the loopback interface.
type memIMAP struct {
	user *imapmemserver.User
	host string
	port int
}

// startMemIMAP starts imapmemserver with one user and an INBOX. The server
// is closed when the test ends.
func startMemIMAP(t *testing.T) *memIMAP {
	t.Helper()
	memServer := imapmemserver.New()
	user := imapmemserver.NewUser(memUsername, memPassword)
	if err := user.Create(memFolder, nil); err != nil {
		t.Fatal(err)
	}
	memServer.AddUser(user)

	server := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memServer.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		Logger:       testLogger{t},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return &memIMAP{user: user, host: host, port: port}
}

// testLogger routes server log lines to the test log.
type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...interface{}) {
	l.t.Logf("imapserver: "+format, args...)
}

// literal adapts a byte slice to imap.LiteralReader.
type literal struct {
	*bytes.Reader
}

func (l literal) Size() int64 { return int64(l.Reader.Len()) }

// appendRaw appends a message to the INBOX with the given INTERNALDATE.
func (m *memIMAP) appendRaw(t *testing.T, raw []byte, internalDate time.Time) {
	t.Helper()
	_, err := m.user.Append(memFolder, literal{bytes.NewReader(raw)}, &imap.AppendOptions{Time: internalDate})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
}

// appendSample appends a testdata file dated the given number of days ago.
func (m *memIMAP) appendSample(t *testing.T, name string, daysAgo int) {
	t.Helper()
	m.appendRaw(t, readSample(t, name), time.Now().AddDate(0, 0, -daysAgo))
}

// mailbox builds the mailbox row pointing at the in-memory server.
func (m *memIMAP) mailbox() *models.Mailbox {
	return &models.Mailbox{
		ID:           1,
		Address:      memUsername,
		ImapHost:     m.host,
		ImapPort:     m.port,
		ImapSecurity: imapSecurityNone,
		ImapUsername: memUsername,
		Folder:       memFolder,
		Enabled:      true,
		InitialDays:  90,
		RecentDays:   30,
	}
}

func TestTestConnectionAgainstMemServer(t *testing.T) {
	srv := startMemIMAP(t)
	ctx := context.Background()
	if err := TestConnection(ctx, srv.mailbox(), memPassword); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if err := TestConnection(ctx, srv.mailbox(), "wrong"); err == nil {
		t.Error("wrong password accepted")
	}
	mb := srv.mailbox()
	mb.Folder = "Nope"
	if err := TestConnection(ctx, mb, memPassword); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("missing folder err = %v", err)
	}
	mb = srv.mailbox()
	mb.ImapSecurity = imapSecuritySSL
	if err := TestConnection(ctx, mb, memPassword); err == nil {
		t.Error("TLS handshake against a plaintext server should fail")
	}
}

func TestFetchAndGroupAgainstMemServer(t *testing.T) {
	srv := startMemIMAP(t)
	root := t.TempDir()
	mb := srv.mailbox()
	ctx := context.Background()

	// Spread the samples over the last 120 days. The two oldest are outside
	// the 90 day initial window and must not be fetched.
	old := []struct {
		name    string
		daysAgo int
	}{
		{"sendmail_bounce.eml", 110},
		{"normal.eml", 100},
	}
	recent := []struct {
		name    string
		daysAgo int
	}{
		{"postfix_dsn.eml", 80},
		{"office365_dsn.eml", 60},
		{"spamhaus_block_a.eml", 50},
		{"qmail_bounce.eml", 45},
		{"gmail_bounce.eml", 20},
		{"autoreply.eml", 10},
		{"postfix_delayed.eml", 1},
	}
	for _, s := range old {
		srv.appendSample(t, s.name, s.daysAgo)
	}
	for _, s := range recent {
		srv.appendSample(t, s.name, s.daysAgo)
	}

	var lines []string
	progress := func(m string) { lines = append(lines, m) }

	// Run 1: initial window (90 days). Fetching only downloads and indexes.
	res, err := FetchMailbox(ctx, root, mb, memPassword, FetchOptions{}, progress)
	if err != nil {
		t.Fatalf("run 1: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if res.Fetched != len(recent) || res.Skipped != 0 {
		t.Fatalf("run 1 fetched %d skipped %d, want %d / 0\n%s", res.Fetched, res.Skipped, len(recent), strings.Join(lines, "\n"))
	}
	if res.UIDValidity == 0 {
		t.Error("run 1 reported no UIDVALIDITY")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"connecting to " + srv.host, "searching since", "initial window, 90 days", "fetching", "indexed 7 messages"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress lacks %q:\n%s", want, joined)
		}
	}
	firstValidity := res.UIDValidity
	assertIndexState(t, root, mb.Address, len(recent), 0, len(recent), 0, "run 1 fetch")

	// Grouping processes the fetched rows; only the actionable Spamhaus
	// group is reported as touched.
	lines = nil
	gres, err := GroupMailbox(ctx, root, mb.Address, false, progress)
	if err != nil {
		t.Fatalf("group 1: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if gres.Processed != len(recent) || gres.Bounces != len(recent) { // every recent sample is a bounce or auto-reply
		t.Errorf("group 1 = %+v, want %d processed / %d bounces", gres, len(recent), len(recent))
	}
	spamKey := GroupKey(categoryIPBlocked, "203.0.113.5", "spamhaus.org")
	if len(gres.GroupsTouched) != 1 || gres.GroupsTouched[0] != spamKey {
		t.Errorf("group 1 touched %v, want [%s]", gres.GroupsTouched, spamKey)
	}
	assertIndexState(t, root, mb.Address, len(recent), len(recent), 0, gres.Groups, "run 1 group")
	db, err := models.OpenMailIndex(MailboxIndexPath(root, mb.Address))
	if err != nil {
		t.Fatal(err)
	}
	g, err := models.GetGroup(db, spamKey)
	db.Close()
	if err != nil {
		t.Errorf("touched group %s not found: %v", spamKey, err)
	} else if g.MessageCount != 1 || !g.NeedsAnalysis || !g.Actionable {
		t.Errorf("group %s counters not refreshed: %+v", spamKey, g)
	}

	// Run 2: recent window, nothing new.
	lines = nil
	res, err = FetchMailbox(ctx, root, mb, memPassword, FetchOptions{}, progress)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res.Fetched != 0 || res.Skipped != 0 {
		t.Errorf("run 2 = %+v, want nothing fetched", res)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "recent window, 30 days") {
		t.Errorf("run 2 did not use the recent window:\n%s", strings.Join(lines, "\n"))
	}
	if gres, err = GroupMailbox(ctx, root, mb.Address, false, nil); err != nil || gres.Processed != 0 || len(gres.GroupsTouched) != 0 {
		t.Errorf("group 2 = %+v (err %v), want nothing processed", gres, err)
	}

	// Run 3: a fresh recipient-side bounce, a second Spamhaus listing (same
	// IP, other recipient domain), a bounce older than the recent window (not
	// fetched) and a large message (5 MB; there is no size limit, it is
	// stored whole).
	srv.appendSample(t, "exim_bounce.eml", 0)
	srv.appendSample(t, "spamhaus_block_b.eml", 0)
	srv.appendRaw(t, withNewMessageID(readSample(t, "sendmail_bounce.eml"), "old-but-new@example.jp"), time.Now().AddDate(0, 0, -45))
	bigBody := bytes.Repeat([]byte("x"), 5*1024*1024)
	big := append([]byte("From: big@example.jp\r\nSubject: big\r\nMessage-ID: <big@example.jp>\r\n\r\n"), bigBody...)
	srv.appendRaw(t, big, time.Now())
	lines = nil
	res, err = FetchMailbox(ctx, root, mb, memPassword, FetchOptions{}, progress)
	if err != nil {
		t.Fatalf("run 3: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if res.Fetched != 3 || res.Skipped != 0 {
		t.Fatalf("run 3 = %+v, want 3 fetched / 0 skipped\n%s", res, strings.Join(lines, "\n"))
	}
	{
		idx, err := models.OpenMailIndex(MailboxIndexPath(root, mb.Address))
		if err != nil {
			t.Fatal(err)
		}
		bigMsgs, _, err := models.ListMessages(idx, models.MessageFilter{Query: "big"})
		if err != nil || len(bigMsgs) != 1 || bigMsgs[0].Size != int64(len(big)) || bigMsgs[0].TextCount != 1 {
			t.Fatalf("large message row = %+v (err %v), want size %d and one text section", bigMsgs, err, len(big))
		}
		if raw, err := ReadMessageFile(root, mb.Address, bigMsgs[0].MessageKey, "eml"); err != nil || !bytes.Equal(raw, big) {
			t.Errorf("large message .eml differs from the original (err %v)", err)
		}
		if txt, err := readFirstSection(root, mb.Address, bigMsgs[0].MessageKey, "txt"); err != nil || len(txt) < len(bigBody) {
			t.Errorf("large message text section = %d bytes (err %v), want the whole %d byte body", len(txt), err, len(bigBody))
		}
		idx.Close()
	}
	assertIndexState(t, root, mb.Address, len(recent)+3, len(recent), 3, gres.Groups, "run 3 fetch")
	gres, err = GroupMailbox(ctx, root, mb.Address, false, nil)
	if err != nil {
		t.Fatalf("group 3: %v", err)
	}
	if gres.Processed != 3 || gres.Bounces != 2 || len(gres.GroupsTouched) != 1 || gres.GroupsTouched[0] != spamKey {
		t.Fatalf("group 3 = %+v, want 3 processed (2 bounces) / touched [%s]", gres, spamKey)
	}
	// The large message is ordinary mail, so bounces stay at the previous
	// count plus the two new notices.
	assertIndexState(t, root, mb.Address, len(recent)+3, len(recent)+2, 0, gres.Groups, "run 3 group")
	db, err = models.OpenMailIndex(MailboxIndexPath(root, mb.Address))
	if err != nil {
		t.Fatal(err)
	}
	msgs, _, err := models.ListMessages(db, models.MessageFilter{GroupKey: spamKey})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Errorf("run 3 spamhaus group members = %+v", msgs)
	}
	for _, m := range msgs {
		if _, err := ReadMessageFile(root, mb.Address, m.MessageKey, "eml"); err != nil {
			t.Errorf("missing eml for %s: %v", m.MessageKey, err)
		}
		if _, err := readFirstSection(root, mb.Address, m.MessageKey, "txt"); err != nil {
			t.Errorf("missing txt for %s: %v", m.MessageKey, err)
		}
		if m.TextCount != 1 || m.BodySource != "text" {
			t.Errorf("%s: text_count=%d body_source=%q, want one text section", m.MessageKey, m.TextCount, m.BodySource)
		}
		if sections, err := ReadBodySections(root, mb.Address, m.MessageKey, "txt", m.TextCount); err != nil || len(sections) != 1 ||
			!strings.Contains(sections[0], "spamhaus") {
			t.Errorf("%s: text sections = %q (err %v)", m.MessageKey, sections, err)
		}
	}

	// Run 4: the folder is re-created, which changes UIDVALIDITY. The same
	// messages come back with new UIDs and must be recognised by Message-ID.
	if err := srv.user.Delete(memFolder); err != nil {
		t.Fatal(err)
	}
	if err := srv.user.Create(memFolder, nil); err != nil {
		t.Fatal(err)
	}
	srv.appendSample(t, "gmail_bounce.eml", 20)
	srv.appendSample(t, "exim_bounce.eml", 0)
	srv.appendSample(t, "postfix_dsn.eml", 80) // outside the recent window anyway
	lines = nil
	res, err = FetchMailbox(ctx, root, mb, memPassword, FetchOptions{}, progress)
	if err != nil {
		t.Fatalf("run 4: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if res.UIDValidity == firstValidity {
		t.Skip("memserver kept the same UIDVALIDITY after re-creating the folder")
	}
	if res.Fetched != 0 || res.Skipped != 2 {
		t.Errorf("run 4 = %+v, want 0 fetched / 2 skipped duplicates\n%s", res, strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "uidvalidity changed") {
		t.Errorf("UIDVALIDITY change not reported:\n%s", strings.Join(lines, "\n"))
	}
	assertIndexState(t, root, mb.Address, len(recent)+3, len(recent)+2, 0, gres.Groups, "run 4")

	// Cancellation is honoured mid-run.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := FetchMailbox(cctx, root, mb, memPassword, FetchOptions{}, nil); err == nil {
		t.Error("cancelled fetch succeeded")
	}
}

// TestFetchNotBeforeBoundsWindow: FetchOptions.NotBefore caps how far back
// a run searches. A bound later than the window's start replaces it (and
// is reported), an earlier one changes nothing.
func TestFetchNotBeforeBoundsWindow(t *testing.T) {
	srv := startMemIMAP(t)
	ctx := context.Background()
	for _, s := range []struct {
		name    string
		daysAgo int
	}{{"postfix_dsn.eml", 80}, {"qmail_bounce.eml", 45}, {"gmail_bounce.eml", 20}, {"postfix_delayed.eml", 1}} {
		srv.appendSample(t, s.name, s.daysAgo)
	}
	// A 30 day bound on a fresh index (initial window 90 days): only the
	// two mails within 30 days are fetched.
	root := t.TempDir()
	var lines []string
	res, err := FetchMailbox(ctx, root, srv.mailbox(), memPassword,
		FetchOptions{NotBefore: time.Now().AddDate(0, 0, -30)}, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("bounded run: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if res.Fetched != 2 {
		t.Errorf("bounded run fetched %d, want 2\n%s", res.Fetched, strings.Join(lines, "\n"))
	}
	joined := strings.Join(lines, "\n")
	wantSince := "searching since " + time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02") + " (initial window, 90 days, limited to the mail retention)"
	if !strings.Contains(joined, wantSince) {
		t.Errorf("progress lacks %q:\n%s", wantSince, joined)
	}
	assertIndexState(t, root, srv.mailbox().Address, 2, 0, 2, 0, "bounded run")
	// A bound earlier than the window changes nothing: the whole initial
	// window is searched on another fresh index.
	root = t.TempDir()
	lines = nil
	res, err = FetchMailbox(ctx, root, srv.mailbox(), memPassword,
		FetchOptions{NotBefore: time.Now().AddDate(0, 0, -365)}, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("unbounded run: %v", err)
	}
	if res.Fetched != 4 || strings.Contains(strings.Join(lines, "\n"), "limited to") {
		t.Errorf("unbounded run fetched %d, want 4 without a limit note\n%s", res.Fetched, strings.Join(lines, "\n"))
	}
	// On a later run the recent window (30 days) and a 10 day bound leave
	// only the newest mail to consider, which is indexed already.
	lines = nil
	res, err = FetchMailbox(ctx, root, srv.mailbox(), memPassword,
		FetchOptions{NotBefore: time.Now().AddDate(0, 0, -10)}, func(m string) { lines = append(lines, m) })
	if err != nil || res.Fetched != 0 {
		t.Errorf("later bounded run: %+v, %v", res, err)
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "recent window, 30 days, limited to the mail retention") || !strings.Contains(joined, "found 1 messages in the window, 0 new") {
		t.Errorf("later bounded run progress:\n%s", joined)
	}
}

// assertIndexState checks the number of index rows (total, detected bounces,
// rows still awaiting grouping, groups) and that every row has its .eml and
// exactly the section files its counts announce (and no parsed sidecar).
func assertIndexState(t *testing.T, root, address string, wantMessages, wantBounces, wantUnclassified, wantGroups int, label string) {
	t.Helper()
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	total, bounces, err := models.CountMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	if total != wantMessages || bounces != wantBounces {
		t.Errorf("%s: index has %d messages / %d bounces, want %d / %d", label, total, bounces, wantMessages, wantBounces)
	}
	if n, err := models.CountUnclassifiedMessages(db); err != nil || n != wantUnclassified {
		t.Errorf("%s: %d unclassified messages (err %v), want %d", label, n, err, wantUnclassified)
	}
	if n, err := countAllGroups(db); err != nil || n != wantGroups {
		t.Errorf("%s: %d groups (err %v), want %d", label, n, err, wantGroups)
	}
	msgs, err := models.ListAllMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.UID == 0 || m.UIDValidity == 0 || m.Folder != memFolder || !m.ReceivedAt.Valid {
			t.Errorf("%s: row lacks IMAP identity: %+v", label, m)
		}
		hasText, hasHTML := m.TextCount > 0, m.HTMLCount > 0
		if (m.BodySource == "text") != hasText || (m.BodySource == "html") != (!hasText && hasHTML) {
			t.Errorf("%s: body_source %q does not match text_count=%d html_count=%d", label, m.BodySource, m.TextCount, m.HTMLCount)
		}
	}
	assertSectionFiles(t, root, address, msgs, label)
}

// withNewMessageID rewrites the Message-ID header of a raw sample so that the
// copy is not treated as a duplicate.
func withNewMessageID(raw []byte, id string) []byte {
	lines := strings.Split(string(raw), "\r\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), "message-id:") {
			lines[i] = "Message-Id: <" + id + ">"
			break
		}
	}
	return []byte(strings.Join(lines, "\r\n"))
}
