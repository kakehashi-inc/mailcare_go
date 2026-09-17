package mailengine

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
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

func TestCheckMailboxAgainstMemServer(t *testing.T) {
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

	// Run 1: initial window (90 days).
	res, err := CheckMailbox(ctx, root, mb, memPassword, progress)
	if err != nil {
		t.Fatalf("run 1: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if res.Fetched != len(recent) || res.Skipped != 0 {
		t.Fatalf("run 1 fetched %d skipped %d, want %d / 0\n%s", res.Fetched, res.Skipped, len(recent), strings.Join(lines, "\n"))
	}
	if res.Bounces != len(recent) { // every recent sample except normal.eml is a bounce or auto-reply
		t.Errorf("run 1 bounces = %d, want %d", res.Bounces, len(recent))
	}
	if len(res.GroupsTouched) == 0 {
		t.Error("run 1 touched no groups")
	}
	if res.UIDValidity == 0 {
		t.Error("run 1 reported no UIDVALIDITY")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"connecting to " + srv.host, "searching since", "initial window, 90 days", "fetching", "indexed 6 messages"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress lacks %q:\n%s", want, joined)
		}
	}
	mb.LastUIDValidity = int64(res.UIDValidity)
	assertIndexState(t, root, mb.Address, len(recent), len(recent), "run 1")
	for _, key := range res.GroupsTouched {
		db, err := models.OpenMailIndex(MailboxIndexPath(root, mb.Address))
		if err != nil {
			t.Fatal(err)
		}
		g, err := models.GetGroup(db, key)
		db.Close()
		if err != nil {
			t.Errorf("touched group %s not found: %v", key, err)
		} else if g.MessageCount == 0 || !g.NeedsAnalysis {
			t.Errorf("group %s counters not refreshed: %+v", key, g)
		}
	}

	// Run 2: recent window, nothing new.
	lines = nil
	res, err = CheckMailbox(ctx, root, mb, memPassword, progress)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res.Fetched != 0 || res.Skipped != 0 || len(res.GroupsTouched) != 0 {
		t.Errorf("run 2 = %+v, want nothing fetched", res)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "recent window, 30 days") {
		t.Errorf("run 2 did not use the recent window:\n%s", strings.Join(lines, "\n"))
	}

	// Run 3: one fresh bounce, one bounce older than the recent window (not
	// fetched) and one oversized message (skipped by size).
	srv.appendSample(t, "exim_bounce.eml", 0)
	srv.appendRaw(t, withNewMessageID(readSample(t, "sendmail_bounce.eml"), "old-but-new@example.jp"), time.Now().AddDate(0, 0, -45))
	big := append([]byte("From: big@example.jp\r\nSubject: big\r\nMessage-ID: <big@example.jp>\r\n\r\n"), bytes.Repeat([]byte("x"), maxMessageSize+1)...)
	srv.appendRaw(t, big, time.Now())
	lines = nil
	res, err = CheckMailbox(ctx, root, mb, memPassword, progress)
	if err != nil {
		t.Fatalf("run 3: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if res.Fetched != 1 || res.Bounces != 1 || res.Skipped != 1 || len(res.GroupsTouched) != 1 {
		t.Fatalf("run 3 = %+v, want 1 fetched / 1 bounce / 1 skipped / 1 group\n%s", res, strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "exceeds the limit") {
		t.Errorf("oversized skip not reported:\n%s", strings.Join(lines, "\n"))
	}
	assertIndexState(t, root, mb.Address, len(recent)+1, len(recent)+1, "run 3")
	db, err := models.OpenMailIndex(MailboxIndexPath(root, mb.Address))
	if err != nil {
		t.Fatal(err)
	}
	msgs, _, err := models.ListMessages(db, models.MessageFilter{GroupKey: res.GroupsTouched[0]})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "Mail delivery failed: returning message to sender" {
		t.Errorf("run 3 group members = %+v", msgs)
	}
	for _, ext := range []string{"eml", "txt", "json"} {
		if _, err := ReadMessageFile(root, mb.Address, msgs[0].MessageKey, ext); err != nil {
			t.Errorf("missing %s for run 3 message: %v", ext, err)
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
	res, err = CheckMailbox(ctx, root, mb, memPassword, progress)
	if err != nil {
		t.Fatalf("run 4: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if int64(res.UIDValidity) == mb.LastUIDValidity {
		t.Skip("memserver kept the same UIDVALIDITY after re-creating the folder")
	}
	if res.Fetched != 0 || res.Skipped != 2 || len(res.GroupsTouched) != 0 {
		t.Errorf("run 4 = %+v, want 0 fetched / 2 skipped duplicates\n%s", res, strings.Join(lines, "\n"))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "uidvalidity changed") {
		t.Errorf("UIDVALIDITY change not reported:\n%s", strings.Join(lines, "\n"))
	}
	assertIndexState(t, root, mb.Address, len(recent)+1, len(recent)+1, "run 4")

	// Cancellation is honoured mid-run.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := CheckMailbox(cctx, root, mb, memPassword, nil); err == nil {
		t.Error("cancelled check succeeded")
	}
}

// assertIndexState checks the number of index rows and that every row has
// its .eml / .txt / .json files.
func assertIndexState(t *testing.T, root, address string, wantMessages, wantBounces int, label string) {
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
	msgs, err := models.ListAllMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	dir := MailboxDir(root, address)
	for _, m := range msgs {
		if m.UID == 0 || m.UIDValidity == 0 || m.Folder != memFolder || !m.ReceivedAt.Valid {
			t.Errorf("%s: row lacks IMAP identity: %+v", label, m)
		}
		for _, ext := range []string{".eml", ".txt", ".json"} {
			if _, err := os.Stat(filepath.Join(dir, m.MessageKey+ext)); err != nil {
				t.Errorf("%s: %s%s missing: %v", label, m.MessageKey, ext, err)
			}
		}
	}
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
