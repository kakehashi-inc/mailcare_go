package mailengine

import (
	"bytes"
	"context"
	"errors"
	"net"
	"slices"
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
	return startMemIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}})
}

// startMemIMAPCaps is startMemIMAP advertising the given capabilities.
func startMemIMAPCaps(t *testing.T, caps imap.CapSet) *memIMAP {
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
		Caps:         caps,
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
// is reported), an earlier one changes nothing. FetchOptions.AllTime
// ignores both and searches the whole folder.
func TestFetchNotBeforeBoundsWindow(t *testing.T) {
	srv := startMemIMAP(t)
	ctx := context.Background()
	for _, s := range []struct {
		name    string
		daysAgo int
	}{{"normal.eml", 200}, {"postfix_dsn.eml", 80}, {"qmail_bounce.eml", 45}, {"gmail_bounce.eml", 20}, {"postfix_delayed.eml", 1}} {
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
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "recent window, 30 days, limited to the mail retention") || !strings.Contains(joined, "found 1 messages, 0 new") {
		t.Errorf("later bounded run progress:\n%s", joined)
	}
	// An all-time run searches the whole folder despite the window and the
	// bound: only the mail older than the initial window is new.
	lines = nil
	res, err = FetchMailbox(ctx, root, srv.mailbox(), memPassword,
		FetchOptions{NotBefore: time.Now().AddDate(0, 0, -10), AllTime: true}, func(m string) { lines = append(lines, m) })
	if err != nil || res.Fetched != 1 {
		t.Errorf("all-time run: %+v, %v", res, err)
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "searching the whole folder (all time)") || !strings.Contains(joined, "found 5 messages, 1 new") {
		t.Errorf("all-time run progress:\n%s", joined)
	}
}

// assertIndexState checks the number of index rows (total, detected bounces,
// rows still awaiting grouping, groups) and that every row has its .eml and
// exactly the section files its counts announce.
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

// serverCount returns how many messages the folder of the in-memory server
// holds.
func (m *memIMAP) serverCount(t *testing.T) uint32 {
	t.Helper()
	s, err := connect(context.Background(), m.mailbox(), memPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close(true)
	sel, err := s.selectFolder(context.Background(), memFolder)
	if err != nil {
		t.Fatal(err)
	}
	return sel.NumMessages
}

// fetchAndGroup fetches the server's messages into a fresh data root and
// groups them.
func fetchAndGroup(t *testing.T, srv *memIMAP) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	if _, err := FetchMailbox(ctx, root, srv.mailbox(), memPassword, FetchOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupMailbox(ctx, root, memUsername, false, nil); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDeleteFromServer(t *testing.T) {
	srv := startMemIMAP(t)
	for i, name := range []string{"postfix_dsn.eml", "office365_dsn.eml", "spamhaus_block_a.eml", "qmail_bounce.eml",
		"gmail_bounce.eml", "autoreply.eml", "normal.eml"} {
		srv.appendSample(t, name, 10+i)
	}
	// A message a system generated on its own, a person's mail with an
	// out-of-office word in the subject and a person's mail under a
	// "Postmaster Team" display name stay on the server: they are matched
	// on weaker evidence than serverDeletableRules.
	generated := append([]byte("Auto-Submitted: auto-generated\r\n"), withNewMessageID(readSample(t, "normal.eml"), "generated@example.jp")...)
	srv.appendRaw(t, generated, time.Now().AddDate(0, 0, -20))
	srv.appendRaw(t, []byte("From: Jiro <jiro@partner.example.com>\r\nTo: newsletter@example.jp\r\n"+
		"Subject: Re: vacation plans (out of office next week)\r\nMessage-Id: <subject-only@example.jp>\r\n"+
		"Date: Tue, 2 Sep 2025 09:15:30 +0900\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"Here is my contact while I am away.\r\n"), time.Now().AddDate(0, 0, -21))
	srv.appendRaw(t, []byte("From: Postmaster Team <support@hosting.example.net>\r\nTo: newsletter@example.jp\r\n"+
		"Subject: Scheduled maintenance of the mail platform\r\nMessage-Id: <display-name@example.jp>\r\n"+
		"Date: Tue, 2 Sep 2025 09:15:30 +0900\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"The mail platform will be upgraded on Saturday.\r\n"), time.Now().AddDate(0, 0, -22))
	root := fetchAndGroup(t, srv)
	ctx := context.Background()
	mb := srv.mailbox()
	keep := 24 * time.Hour
	indexPath := MailboxIndexPath(root, mb.Address)

	db, err := models.OpenMailIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	// Every daemon notice classified by a deletable rule is due whatever the
	// state of its group (all groups stay open here); the ordinary mail and
	// the three messages above are not.
	due, err := models.ListServerDeletionCandidates(db, memFolder, time.Now().Add(-keep), serverDeletableRules)
	if err != nil || len(due) != 6 {
		t.Fatalf("candidates: %v (%d, want 6)", err, len(due))
	}
	for _, c := range due {
		m, _ := models.GetMessageByKey(db, c.MessageKey)
		if !m.IsBounce || !slices.Contains(serverDeletableRules, m.Rule) {
			t.Errorf("candidate %s: is_bounce %v, rule %q", c.MessageKey, m.IsBounce, m.Rule)
		}
	}
	for id, want := range map[string]string{"generated@example.jp": ruleAutoGenerated, "subject-only@example.jp": ruleAutoReplySubject, "display-name@example.jp": ruleDaemonDisplayName} {
		var rule string
		var isBounce bool
		if err := db.QueryRow(`SELECT rule, is_bounce FROM messages WHERE message_id LIKE ?`, "%"+id+"%").Scan(&rule, &isBounce); err != nil || rule != want || !isBounce {
			t.Errorf("%s: rule %q, is_bounce %v, %v (want %s)", id, rule, isBounce, err, want)
		}
	}
	// The target messages are the same messages whatever their date.
	if n, err := CountTargetMessages(db); err != nil || n != len(due) {
		t.Errorf("target messages: %d %v, want %d", n, err, len(due))
	}
	// A message not classified yet is never due.
	if _, err := db.Exec(`UPDATE messages SET classified = 0 WHERE id = ?`, due[len(due)-1].ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := models.ListServerDeletionCandidates(db, memFolder, time.Now().Add(-keep), serverDeletableRules); len(again) != len(due)-1 {
		t.Errorf("unclassified message still due: %d candidates", len(again))
	}
	if _, err := db.Exec(`UPDATE messages SET classified = 1 WHERE id = ?`, due[len(due)-1].ID); err != nil {
		t.Fatal(err)
	}
	// A candidate without a Message-ID (the qmail notice) stays on the
	// server, and so does one whose Message-ID no longer matches.
	skipped := map[int64]bool{}
	for _, c := range due {
		if c.MessageID == "" {
			skipped[c.ID] = true
		}
	}
	if len(skipped) != 1 {
		t.Fatalf("want exactly one candidate without a Message-ID, got %d", len(skipped))
	}
	mismatch := due[0]
	if skipped[mismatch.ID] {
		mismatch = due[1]
	}
	skipped[mismatch.ID] = true
	if _, err := db.Exec(`UPDATE messages SET message_id = 'someone-else@example.jp' WHERE id = ?`, mismatch.ID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before := srv.serverCount(t)

	// keep 0 and a disabled mailbox do nothing (and connect to nothing).
	if res, err := DeleteFromServer(ctx, root, mb, memPassword, 0, nil); err != nil || res.Deleted != 0 {
		t.Errorf("keep 0: %+v %v", res, err)
	}
	disabled := srv.mailbox()
	disabled.Enabled = false
	if res, err := DeleteFromServer(ctx, root, disabled, "wrong password", keep, nil); err != nil || res.Deleted != 0 {
		t.Errorf("disabled mailbox: %+v %v", res, err)
	}

	var lines []string
	res, err := DeleteFromServer(ctx, root, mb, memPassword, keep, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("DeleteFromServer: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if res.Deleted != len(due)-len(skipped) || res.Skipped != len(skipped) {
		t.Errorf("result %+v, want %d deleted / %d skipped", res, len(due)-len(skipped), len(skipped))
	}
	if after := srv.serverCount(t); after != before-uint32(len(due)-len(skipped)) {
		t.Errorf("server holds %d messages, want %d", after, before-uint32(len(due)-len(skipped)))
	}
	db, err = models.OpenMailIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	// The messages deleted from the server are still target messages.
	if n, err := CountTargetMessages(db); err != nil || n != len(due) {
		t.Errorf("target messages after the deletion: %d %v, want %d", n, err, len(due))
	}
	for _, c := range due {
		m, err := models.GetMessageByKey(db, c.MessageKey)
		if err != nil {
			t.Fatalf("the local row must stay: %v", err)
		}
		if m.ServerDeletedAt.Valid == skipped[c.ID] {
			t.Errorf("%s: server_deleted_at = %v", c.MessageKey, m.ServerDeletedAt)
		}
	}
	db.Close()

	// A second run deletes nothing more.
	if res, err := DeleteFromServer(ctx, root, mb, memPassword, keep, nil); err != nil || res.Deleted != 0 || res.Skipped != len(skipped) {
		t.Errorf("second run: %+v %v", res, err)
	}
	// Reindex carries the deletion time over.
	if _, err := Reindex(ctx, root, mb.Address, nil); err != nil {
		t.Fatal(err)
	}
	db, err = models.OpenMailIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, c := range due {
		m, err := models.GetMessageByKey(db, c.MessageKey)
		if err != nil || m.ServerDeletedAt.Valid == skipped[c.ID] {
			t.Errorf("after reindex %s: %+v %v", c.MessageKey, m, err)
		}
	}
}

func TestDeleteFromServerSafety(t *testing.T) {
	ctx := context.Background()
	keep := 24 * time.Hour

	// A server without UIDPLUS (IMAP4rev1 only): the plain EXPUNGE is used
	// while no other message carries \Deleted ...
	srv := startMemIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.appendSample(t, "postfix_dsn.eml", 10)
	srv.appendSample(t, "normal.eml", 10)
	root := fetchAndGroup(t, srv)
	res, err := DeleteFromServer(ctx, root, srv.mailbox(), memPassword, keep, nil)
	if err != nil || res.Deleted != 1 {
		t.Errorf("without UIDPLUS: %+v %v", res, err)
	}
	if n := srv.serverCount(t); n != 1 {
		t.Errorf("without UIDPLUS the server holds %d messages, want 1 (the ordinary mail)", n)
	}

	// ... and nothing is expunged while another client's message carries it.
	srv = startMemIMAPCaps(t, imap.CapSet{imap.CapIMAP4rev1: {}})
	srv.appendSample(t, "postfix_dsn.eml", 10)
	srv.appendSample(t, "normal.eml", 10)
	root = fetchAndGroup(t, srv)
	s, err := connect(ctx, srv.mailbox(), memPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.selectFolder(ctx, memFolder); err != nil {
		t.Fatal(err)
	}
	// The ordinary mail (appended last, UID 2) is flagged by "another client".
	if err := s.client.Store(imap.UIDSetNum(2), deletedFlag(imap.StoreFlagsAdd), nil).Close(); err != nil {
		t.Fatal(err)
	}
	s.close(true)
	res, err = DeleteFromServer(ctx, root, srv.mailbox(), memPassword, keep, nil)
	if !errors.Is(err, ErrOtherDeletedFlags) || res.Deleted != 0 {
		t.Errorf("foreign \\Deleted flag: %+v %v", res, err)
	}
	if n := srv.serverCount(t); n != 2 {
		t.Errorf("with a foreign \\Deleted flag the server holds %d messages, want 2", n)
	}
	db, err := models.OpenMailIndex(MailboxIndexPath(root, memUsername))
	if err != nil {
		t.Fatal(err)
	}
	if due, _ := models.ListServerDeletionCandidates(db, memFolder, time.Now().Add(-keep), serverDeletableRules); len(due) != 1 {
		t.Errorf("the candidate must stay due for the next cleanup, got %d", len(due))
	}
	db.Close()

	// A folder re-created under another UIDVALIDITY: every candidate is
	// skipped.
	srv = startMemIMAP(t)
	srv.appendSample(t, "postfix_dsn.eml", 10)
	root = fetchAndGroup(t, srv)
	db, err = models.OpenMailIndex(MailboxIndexPath(root, memUsername))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messages SET uidvalidity = uidvalidity + 1`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	res, err = DeleteFromServer(ctx, root, srv.mailbox(), memPassword, keep, nil)
	if err != nil || res.Deleted != 0 || res.Skipped != 1 {
		t.Errorf("other UIDVALIDITY: %+v %v", res, err)
	}
	if n := srv.serverCount(t); n != 1 {
		t.Errorf("other UIDVALIDITY: the server holds %d messages, want 1", n)
	}
}
