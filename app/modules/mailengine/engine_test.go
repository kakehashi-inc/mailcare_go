package mailengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

func TestSanitizeAddress(t *testing.T) {
	cases := map[string]string{
		"user@example.com":         "user@example.com",
		"User.Name+tag@Example.JP": "User.Name+tag@Example.JP",
		`a<b>c:d"e/f\g|h?i*j@x.y`:  "a_b_c_d_e_f_g_h_i_j@x.y",
		"tab\there@x.y":            "tab_here@x.y",
		"space here@x.y":           "space_here@x.y",
		"日本語@example.jp":           "___@example.jp",
		"trailing.@x.y.":           "trailing.@x.y_",
		"dots...":                  "dots.._",
		"":                         "_",
		"CON":                      "_CON",
		"con.example":              "_con.example",
		"com1@x.y":                 "com1@x.y",
		"COM1.txt":                 "_COM1.txt",
		"nul":                      "_nul",
		"lpt9.mail":                "_lpt9.mail",
		"consent@x.y":              "consent@x.y",
	}
	for in, want := range cases {
		if got := SanitizeAddress(in); got != want {
			t.Errorf("SanitizeAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathsAndKeyValidation(t *testing.T) {
	root := filepath.Join("root", "mails")
	if got := MailboxDir(root, "a/b@x.y"); got != filepath.Join(root, "a_b@x.y") {
		t.Fatalf("MailboxDir = %q", got)
	}
	if got := MailboxIndexPath(root, "a@x.y"); got != filepath.Join(root, "a@x.y.sqlite") {
		t.Fatalf("MailboxIndexPath = %q", got)
	}
	key := MessageKey(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), "INBOX", 1, 2)
	if got := MessageFilePath(root, "a@x.y", key, "eml"); got != filepath.Join(root, "a@x.y", key+".eml") {
		t.Fatalf("MessageFilePath = %q", got)
	}
	for _, bad := range []string{"", "../etc/passwd", "20260901-120000_abcdef012345/x", "20260901-120000_ABCDEF012345",
		"20260901-120000_abcdef01234", `..\..\x`, "20260901-120000_abcdef012345.eml"} {
		if got := MessageFilePath(root, "a@x.y", bad, "eml"); got != "" {
			t.Errorf("MessageFilePath accepted key %q: %q", bad, got)
		}
		if _, err := ReadMessageFile(root, "a@x.y", bad, "eml"); err != ErrInvalidMessageKey {
			t.Errorf("ReadMessageFile(%q) err = %v, want ErrInvalidMessageKey", bad, err)
		}
	}
	if got := MessageFilePath(root, "a@x.y", key, "exe"); got != "" {
		t.Errorf("MessageFilePath accepted extension exe: %q", got)
	}
	if _, err := ReadMessageFile(t.TempDir(), "a@x.y", key, "txt"); !os.IsNotExist(err) {
		t.Errorf("ReadMessageFile missing file err = %v, want not-exist", err)
	}
}

func TestMessageKeyDeterministic(t *testing.T) {
	d := time.Date(2026, 9, 1, 3, 4, 5, 0, time.FixedZone("JST", 9*3600))
	k1 := MessageKey(d, "INBOX", 1234, 56)
	k2 := MessageKey(d.UTC(), "INBOX", 1234, 56)
	if k1 != k2 {
		t.Fatalf("keys differ for the same message: %s vs %s", k1, k2)
	}
	if !strings.HasPrefix(k1, "20260831-180405_") || !ValidMessageKey(k1) {
		t.Fatalf("unexpected key shape %q", k1)
	}
	if MessageKey(d, "INBOX", 1234, 57) == k1 || MessageKey(d, "INBOX", 1235, 56) == k1 || MessageKey(d, "Junk", 1234, 56) == k1 {
		t.Fatal("different messages produced the same key")
	}
}

func TestDiagnosticTemplate(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"550 5.1.1 <taro.yamada@customer.example.com>: Recipient address rejected: User unknown in virtual mailbox table",
			"550 5.1.1 <addr>: recipient address rejected: user unknown in virtual mailbox table",
		},
		{
			"host mx.customer.example.com[198.51.100.25] said: 550 5.1.1 User unknown",
			"host <host><ip> said: 550 5.1.1 user unknown",
		},
		{
			"connect to mx.slowmail.example.net[2001:db8::25]:25: Connection timed out",
			"connect to <host><ip>:<n>: connection timed out",
		},
		{
			"Message delayed since Tue, 2 Sep 2025 09:15:30 +0900; queue id 4Xk1Yz2b3Vz9tLp; 12 retries",
			"message delayed since <date>; queue id <id>; <n> retries",
		},
		{
			"421 4.7.0 Try again later, closing connection. (EHLO) a12-34.smtp-out.example.org 2025-09-02T00:00:00Z",
			"421 4.7.0 try again later, closing connection. (ehlo) <host> <date>",
		},
		{"   Mixed   CASE\n\twhitespace  ", "mixed case whitespace"},
		{"", ""},
	}
	for _, c := range cases {
		if got := DiagnosticTemplate(c.in); got != c.want {
			t.Errorf("DiagnosticTemplate(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("word ", 200)
	if got := DiagnosticTemplate(long); len([]rune(got)) > maxTemplateLen {
		t.Errorf("template not capped: %d runes", len([]rune(got)))
	}
	// Two notices for different addresses / IPs / IDs must collapse.
	a := DiagnosticTemplate("<a@x.example>: host mx1.x.example[192.0.2.1] said: 550 5.1.1 User unknown (id 1A2B3C4D5E)")
	b := DiagnosticTemplate("<b@y.example>: host mx2.y.example[198.51.100.9] said: 550 5.1.1 User unknown (id 9F8E7D6C5B)")
	if a != b {
		t.Errorf("templates differ:\n%s\n%s", a, b)
	}
	if GroupKey("failed", "x.example", "5.1.1", a) == GroupKey("failed", "y.example", "5.1.1", a) {
		t.Error("group keys must differ per domain")
	}
	if len(GroupKey("failed", "x.example", "5.1.1", a)) != 16 {
		t.Error("group key must be 16 hex digits")
	}
}

func TestResponsible(t *testing.T) {
	cases := []struct{ status, diag, want string }{
		{"5.1.1", "", responsibleRecipient},
		{"4.2.2", "", responsibleRecipient},
		{"5.1.10", "", responsibleDomain},
		{"5.4.4", "", responsibleDomain},
		{"5.7.1", "", responsibleSender},
		{"4.7.0", "", responsibleSender},
		{"5.3.0", "", responsibleSender},
		{"4.4.1", "", responsibleSender},
		{"4.2.1", "", responsibleSender},
		{"5.0.0", "", responsibleUnknown},
		{"", "550 User unknown", responsibleRecipient},
		{"", "Host or domain name not found", responsibleDomain},
		{"", "554 Message rejected: blocked using spamhaus", responsibleSender},
		{"", "", responsibleUnknown},
	}
	for _, c := range cases {
		if got := Responsible(c.status, c.diag); got != c.want {
			t.Errorf("Responsible(%q, %q) = %q, want %q", c.status, c.diag, got, c.want)
		}
	}
}

// sample is one testdata file with what the parser, classifier and extractor
// must produce for it.
type sample struct {
	file       string
	isBounce   bool
	kind       string
	reason     string
	subject    string
	fromAddr   string
	recipient  string
	domain     string
	status     string
	smtp       string
	remoteIP   string
	remoteMTA  string
	origSubj   string
	origMsgID  string
	responsble string
	diagHas    string
	hasHTML    bool
}

var samples = []sample{
	{
		file: "postfix_dsn.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "taro.yamada@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteMTA: "mx.customer.example.com", origSubj: "【重要】9月のお知らせ", origMsgID: "news-20250902-0001@example.jp",
		responsble: responsibleRecipient, diagHas: "User unknown in virtual mailbox table",
	},
	{
		file: "postfix_delayed.eml", isBounce: true, kind: bounceKindDelayed, reason: "dsn_report",
		subject: "Delayed Mail (still being retried)", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "hanako@slowmail.example.net", domain: "slowmail.example.net", status: "4.4.1", smtp: "",
		remoteIP: "203.0.113.7", remoteMTA: "mx.slowmail.example.net", origSubj: "Weekly digest", origMsgID: "digest-0003@example.jp",
		responsble: responsibleSender, diagHas: "Connection timed out",
	},
	{
		file: "exim_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "daemon_sender",
		subject: "Mail delivery failed: returning message to sender", fromAddr: "Mailer-Daemon@mail.example.org",
		recipient: "jiro@nowhere.example.org", domain: "nowhere.example.org", status: "5.1.1", smtp: "550",
		remoteIP: "203.0.113.55", remoteMTA: "mx.nowhere.example.org",
		responsble: responsibleRecipient, diagHas: "does not exist",
	},
	{
		file: "qmail_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "daemon_sender",
		subject: "failure notice", fromAddr: "MAILER-DAEMON@qmail.example.net",
		recipient: "saburo@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteIP: "198.51.100.25", responsble: responsibleRecipient, diagHas: "User unknown",
	},
	{
		file: "office365_dsn.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Undeliverable: Service maintenance notice", fromAddr: "MicrosoftExchange329e71ec88ae4615bbc36ab6ce41109e@corp.example.co.jp",
		recipient: "shiro@corp.example.co.jp", domain: "corp.example.co.jp", status: "5.1.10", smtp: "550",
		origSubj: "Service maintenance notice", origMsgID: "maint-0006@example.jp",
		responsble: responsibleDomain, diagHas: "RecipientNotFound", hasHTML: true,
	},
	{
		file: "gmail_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Delivery Status Notification (Failure)", fromAddr: "mailer-daemon@googlemail.com",
		recipient: "goro@gmail.com", domain: "gmail.com", status: "5.1.1", smtp: "550",
		remoteMTA: "gmail-smtp-in.l.google.com", origSubj: "Welcome aboard", origMsgID: "gm-0007@example.jp",
		responsble: responsibleRecipient, diagHas: "does not exist", hasHTML: true,
	},
	{
		file: "sendmail_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Returned mail: see transcript for details", fromAddr: "MAILER-DAEMON@relay.example.edu",
		recipient: "rokuro@dept.example.edu", domain: "dept.example.edu", status: "5.1.1", smtp: "550",
		remoteMTA: "mail.dept.example.edu", origSubj: "Campus event", origMsgID: "campus-0008@example.jp",
		responsble: responsibleRecipient, diagHas: "User unknown",
	},
	{
		file: "autoreply.eml", isBounce: true, kind: bounceKindAutoReply, reason: "auto_reply",
		subject: "自動返信: Re: 9月のお知らせ", fromAddr: "nanako@partner.example.com",
	},
	{
		file: "normal.eml", isBounce: false, subject: "Re: September campaign - question about pricing",
		fromAddr: "hachiro@client.example.com", hasHTML: true,
	},
	{
		file: "malformed.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		fromAddr:  "MAILER-DAEMON@broken.example.net",
		recipient: "kuro@broken.example.net", domain: "broken.example.net", status: "5.7.1", smtp: "550",
		remoteIP: "192.0.2.99", remoteMTA: "mx.broken.example.net", responsble: responsibleSender,
		diagHas: "rejected due to policy",
	},
}

func readSample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}

func TestParseClassifyExtractSamples(t *testing.T) {
	for _, s := range samples {
		t.Run(s.file, func(t *testing.T) {
			pm := ParseMessage(readSample(t, s.file))
			if s.subject != "" && pm.Subject != s.subject {
				t.Errorf("subject = %q, want %q", pm.Subject, s.subject)
			}
			if pm.FromAddress != s.fromAddr {
				t.Errorf("from = %q, want %q", pm.FromAddress, s.fromAddr)
			}
			if pm.HasHTML != s.hasHTML {
				t.Errorf("has_html = %v, want %v", pm.HasHTML, s.hasHTML)
			}
			cls := Classify(pm)
			if cls.IsBounce != s.isBounce || cls.Kind != s.kind || cls.Reason != s.reason {
				t.Fatalf("classification = %+v, want bounce=%v kind=%s reason=%s", cls, s.isBounce, s.kind, s.reason)
			}
			if !s.isBounce || s.kind == bounceKindAutoReply {
				return
			}
			b := ExtractBounce(pm, cls.Kind, "newsletter@example.jp")
			check := func(name, got, want string) {
				if want != "" && got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			check("recipient", b.OriginalRecipient, s.recipient)
			check("domain", b.RecipientDomain, s.domain)
			check("status", b.StatusCode, s.status)
			check("smtp", b.SMTPCode, s.smtp)
			check("remote_ip", b.RemoteIP, s.remoteIP)
			check("remote_mta", b.RemoteMTA, s.remoteMTA)
			check("original_subject", b.OriginalSubject, s.origSubj)
			check("original_message_id", b.OriginalMessageID, s.origMsgID)
			check("responsible", b.Responsible, s.responsble)
			if s.diagHas != "" && !strings.Contains(b.Diagnostic, s.diagHas) {
				t.Errorf("diagnostic %q does not contain %q", b.Diagnostic, s.diagHas)
			}
			if b.DiagnosticTemplate == "" {
				t.Error("diagnostic template is empty")
			}
			if b.SMTPCode != "" && !regexp.MustCompile(`^[245][0-9]{2}$`).MatchString(b.SMTPCode) {
				t.Errorf("smtp code %q is not a 3-digit reply code", b.SMTPCode)
			}
			if strings.Contains(b.DiagnosticTemplate, s.recipient) {
				t.Errorf("template still contains the recipient: %q", b.DiagnosticTemplate)
			}
		})
	}
}

func TestNoSyntheticSMTPCode(t *testing.T) {
	pm := ParseMessage(readSample(t, "postfix_delayed.eml"))
	b := ExtractBounce(pm, bounceKindDelayed, "newsletter@example.jp")
	if b.SMTPCode != "" {
		t.Errorf("smtp code = %q, want empty when the notice has no reply code", b.SMTPCode)
	}
	if b.StatusCode != "4.4.1" {
		t.Errorf("status = %q", b.StatusCode)
	}
	// The group identity never depends on the SMTP code.
	g := groupForBounce(bounceKindDelayed, b)
	other := *b
	other.SMTPCode = "421"
	if g2 := groupForBounce(bounceKindDelayed, &other); g2.GroupKey != g.GroupKey || g2.Title != g.Title {
		t.Errorf("group key/title depend on the SMTP code: %s/%s vs %s/%s", g.GroupKey, g.Title, g2.GroupKey, g2.Title)
	}
}

func TestParsedMessageDetails(t *testing.T) {
	pm := ParseMessage(readSample(t, "postfix_dsn.eml"))
	if pm.ParseError != "" {
		t.Fatalf("unexpected parse error: %s", pm.ParseError)
	}
	if pm.ContentType != "multipart/report" || pm.ReportType != "delivery-status" {
		t.Errorf("content type = %q / %q", pm.ContentType, pm.ReportType)
	}
	if pm.FromName != "Mail Delivery System" {
		t.Errorf("from name = %q", pm.FromName)
	}
	if pm.ReturnPath != "" {
		t.Errorf("return path = %q, want empty", pm.ReturnPath)
	}
	if pm.Date.IsZero() || pm.Date.UTC().Format(time.RFC3339) != "2025-09-02T00:15:30Z" {
		t.Errorf("date = %v", pm.Date)
	}
	if pm.DeliveryStatus == nil || pm.DeliveryStatus.ReportingMTA != "mx1.example.jp" || len(pm.DeliveryStatus.Recipients) != 1 {
		t.Fatalf("delivery status = %+v", pm.DeliveryStatus)
	}
	r := pm.DeliveryStatus.Recipients[0]
	if r.FinalRecipient != "taro.yamada@customer.example.com" || r.Action != "failed" || r.Status != "5.1.1" ||
		r.RemoteMTA != "mx.customer.example.com" || !strings.HasPrefix(r.DiagnosticCode, "smtp; 550 5.1.1") {
		t.Errorf("recipient block = %+v", r)
	}
	if pm.OriginalMessage == nil || pm.OriginalMessage.From != "テストメール <newsletter@example.jp>" ||
		pm.OriginalMessage.Date.IsZero() {
		t.Errorf("original message = %+v", pm.OriginalMessage)
	}
	if !strings.Contains(pm.TextBody, "This is the mail system at host mx1.example.jp.") {
		t.Errorf("text body not taken from the notification part: %q", pm.TextBody)
	}
	if strings.Contains(pm.TextBody, "9月のお知らせをお送りします") {
		t.Error("text body must not include the embedded original message")
	}
	if pm.Headers["Message-Id"] == "" && pm.Headers["Message-ID"] == "" {
		t.Errorf("headers map lacks Message-Id: %v", pm.Headers)
	}

	// The malformed sample must still yield headers, a body and no panic.
	bad := ParseMessage(readSample(t, "malformed.eml"))
	if bad.ParseError == "" {
		t.Error("malformed message should record a parse error")
	}
	if bad.FromAddress != "MAILER-DAEMON@broken.example.net" || bad.TextBody == "" {
		t.Errorf("malformed fallback = from %q body %q", bad.FromAddress, bad.TextBody)
	}
	// Garbage in, no panic out.
	for _, garbage := range []string{"", "\x00\xff\xfe", "Subject: only\n", strings.Repeat("--", 5000), "Content-Type: multipart/mixed; boundary=\n\n--\n"} {
		pm := ParseMessage([]byte(garbage))
		if pm == nil {
			t.Fatal("nil parsed message")
		}
		_ = Classify(pm)
		_ = ExtractBounce(pm, bounceKindFailed)
	}
	// Round trip through JSON keeps the structure.
	data, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	var back ParsedMessage
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.DeliveryStatus == nil || back.OriginalMessage == nil || back.Subject != pm.Subject {
		t.Error("JSON round trip lost data")
	}
}

func TestUnfoldAndClassifyText(t *testing.T) {
	if got := unfold("<a@b.example>: host mx[1.2.3.4] said: 550\n    5.1.1 user unknown\nnext line"); !strings.Contains(got, "said: 550 5.1.1 user unknown\nnext line") {
		t.Errorf("unfold = %q", got)
	}
	pm := &ParsedMessage{Subject: "Warning: could not send message for past 4 hours", FromAddress: "postmaster@x.example",
		TextBody: "Will keep trying until message is 5 days old", Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindDelayed || c.Reason != "daemon_sender" {
		t.Errorf("delayed daemon mail classified as %+v", c)
	}
	pm = &ParsedMessage{Subject: "Out of Office: Re: hello", FromAddress: "someone@x.example", Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindAutoReply {
		t.Errorf("out of office classified as %+v", c)
	}
	pm = &ParsedMessage{Subject: "hello", FromAddress: "someone@x.example", FromName: "Mail Delivery Subsystem", Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindOther || c.Reason != "daemon_display_name" {
		t.Errorf("daemon display name classified as %+v", c)
	}
	pm = &ParsedMessage{Subject: "配信できませんでした", FromAddress: "someone@x.example", Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindFailed || c.Reason != "subject_pattern" {
		t.Errorf("japanese subject classified as %+v", c)
	}
}

// buildMailbox stores every sample through storeMessage into a fresh index
// and returns the paths (used by the round-trip tests).
func buildMailbox(t *testing.T) (root, address string) {
	t.Helper()
	root = t.TempDir()
	address = "newsletter@example.jp"
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tracker := newGroupTracker()
	for i, s := range samples {
		raw := readSample(t, s.file)
		src := Source{Folder: "INBOX", UIDValidity: 1700000000, UID: uint32(i + 1), ReceivedAt: time.Now().UTC(), FetchedAt: time.Now().UTC()}
		if _, _, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: true}, tracker, address); err != nil {
			t.Fatalf("store %s: %v", s.file, err)
		}
	}
	if _, err := tracker.refresh(db); err != nil {
		t.Fatal(err)
	}
	return root, address
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStoreAndReindexRoundTrip(t *testing.T) {
	root, address := buildMailbox(t)
	dir := MailboxDir(root, address)

	entries, _ := os.ReadDir(dir)
	var emls, jsons, htmls int
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".eml":
			emls++
		case ".json":
			jsons++
		case ".html":
			htmls++
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
	if emls != len(samples) || jsons != len(samples) || htmls != 3 {
		t.Fatalf("files: %d eml, %d json, %d html", emls, jsons, htmls)
	}

	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	total, bounces, err := models.CountMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	wantBounces := 0
	for _, s := range samples {
		if s.isBounce {
			wantBounces++
		}
	}
	if total != len(samples) || bounces != wantBounces {
		t.Fatalf("index has %d messages / %d bounces, want %d / %d", total, bounces, len(samples), wantBounces)
	}
	groupsBefore := countRows(t, db, "groups")
	bouncesBefore := countRows(t, db, "bounces")
	if groupsBefore == 0 || bouncesBefore != wantBounces-1 { // the auto-reply has no bounces row
		t.Fatalf("groups=%d bounces=%d before reindex", groupsBefore, bouncesBefore)
	}
	var withGroup int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE group_key <> ''`).Scan(&withGroup); err != nil {
		t.Fatal(err)
	}
	if withGroup != wantBounces-1 {
		t.Errorf("%d messages carry a group key, want %d", withGroup, wantBounces-1)
	}
	var autoGroup string
	if err := db.QueryRow(`SELECT group_key FROM messages WHERE bounce_kind = 'auto_reply'`).Scan(&autoGroup); err != nil || autoGroup != "" {
		t.Errorf("auto reply group key = %q (err %v), want empty", autoGroup, err)
	}
	var cnt, recipients int
	var first, last sql.NullTime
	if err := db.QueryRow(`SELECT message_count, recipient_count, first_seen, last_seen FROM groups ORDER BY message_count DESC LIMIT 1`).
		Scan(&cnt, &recipients, &first, &last); err != nil {
		t.Fatal(err)
	}
	if cnt == 0 || recipients == 0 || !first.Valid || !last.Valid {
		t.Errorf("group counters not refreshed: count=%d recipients=%d first=%v last=%v", cnt, recipients, first, last)
	}
	// Two 5.1.1 "user unknown in virtual mailbox table" notices for the same
	// domain (postfix and qmail samples) must land in one group.
	var sameGroup int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT group_key) FROM messages WHERE message_key IN (
		SELECT m.message_key FROM messages m JOIN bounces b ON b.message_id = m.id WHERE b.recipient_domain = 'customer.example.com')`).
		Scan(&sameGroup); err != nil {
		t.Fatal(err)
	}
	if sameGroup != 1 {
		t.Errorf("customer.example.com notices spread over %d groups, want 1", sameGroup)
	}
	rowsBefore := snapshotMessages(t, db)
	db.Close()

	// Corrupt the derived files: reindex must regenerate them.
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".txt" {
			if err := os.WriteFile(filepath.Join(dir, e.Name()), []byte("stale"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var lines []string
	res, err := Reindex(context.Background(), root, address, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("reindex: %v (progress: %v)", err, lines)
	}
	if res.Messages != len(samples) || res.Bounces != wantBounces || res.Groups != groupsBefore {
		t.Errorf("reindex result = %+v, want %d/%d/%d", res, len(samples), wantBounces, groupsBefore)
	}
	db, err = models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rowsAfter := snapshotMessages(t, db)
	if len(rowsAfter) != len(rowsBefore) {
		t.Fatalf("reindex changed the row count: %d -> %d", len(rowsBefore), len(rowsAfter))
	}
	for key, before := range rowsBefore {
		after, ok := rowsAfter[key]
		if !ok {
			t.Errorf("message %s lost by reindex", key)
			continue
		}
		if before != after {
			t.Errorf("message %s changed by reindex:\n before %+v\n after  %+v", key, before, after)
		}
	}
	if countRows(t, db, "bounces") != bouncesBefore || countRows(t, db, "groups") != groupsBefore {
		t.Errorf("bounces/groups differ after reindex: %d/%d", countRows(t, db, "bounces"), countRows(t, db, "groups"))
	}
	for key := range rowsBefore {
		txt, err := ReadMessageFile(root, address, key, "txt")
		if err != nil {
			t.Errorf("read txt %s: %v", key, err)
		} else if string(txt) == "stale" {
			t.Errorf("reindex did not regenerate %s.txt", key)
		}
	}

	// Reclassify keeps the messages and rebuilds the classification.
	res, err = Reclassify(context.Background(), root, address, nil)
	if err != nil {
		t.Fatalf("reclassify: %v", err)
	}
	if res.Messages != len(samples) || res.Bounces != wantBounces || res.Groups != groupsBefore {
		t.Errorf("reclassify result = %+v", res)
	}
	rowsReclassified := snapshotMessages(t, db)
	for key, before := range rowsBefore {
		if after := rowsReclassified[key]; after != before {
			t.Errorf("message %s changed by reclassify:\n before %+v\n after  %+v", key, before, after)
		}
	}

	// DeleteMailboxData removes everything.
	db.Close()
	if err := DeleteMailboxData(root, address); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("mailbox dir still exists: %v", err)
	}
	if _, err := os.Stat(MailboxIndexPath(root, address)); !os.IsNotExist(err) {
		t.Errorf("index still exists: %v", err)
	}
}

// messageRow is the comparable subset of a messages row.
type messageRow struct {
	UID, UIDValidity                                                    uint32
	Folder, MessageID, Subject, From, FromName, To, Kind, Reason, Group string
	IsBounce, HasText, HasHTML                                          bool
	Date                                                                string
}

func snapshotMessages(t *testing.T, db *sql.DB) map[string]messageRow {
	t.Helper()
	msgs, err := models.ListAllMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]messageRow{}
	for _, m := range msgs {
		date := ""
		if m.Date.Valid {
			date = m.Date.Time.UTC().Format(time.RFC3339)
		}
		out[m.MessageKey] = messageRow{
			UID: m.UID, UIDValidity: m.UIDValidity, Folder: m.Folder, MessageID: m.MessageID, Subject: m.Subject,
			From: m.FromAddress, FromName: m.FromName, To: m.ToAddress, Kind: m.BounceKind, Reason: m.ClassifyReason,
			Group: m.GroupKey, IsBounce: m.IsBounce, HasText: m.HasText, HasHTML: m.HasHTML, Date: date,
		}
	}
	return out
}

// seedReport stores a completed agent report on the largest group, marks the
// group resolved with an agent-chosen responsible party and returns the key,
// plus the key of a single-message group (whose files the caller may delete).
func seedReport(t *testing.T, root, address string) (kept, single string, stateUpdated time.Time) {
	t.Helper()
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	groups, err := models.ListGroups(db, models.GroupFilter{})
	if err != nil || len(groups) < 2 {
		t.Fatalf("need at least two groups: %v (%d)", err, len(groups))
	}
	for _, g := range groups {
		if kept == "" && g.MessageCount >= 2 {
			kept = g.GroupKey
		} else if single == "" && g.MessageCount == 1 {
			single = g.GroupKey
		}
	}
	if kept == "" || single == "" {
		t.Fatalf("no suitable groups: %+v", groups)
	}
	for _, key := range []string{kept, single} {
		r := &models.AgentReport{GroupKey: key, Provider: "codex", MessageCount: 2}
		if err := models.InsertAgentReport(db, r); err != nil {
			t.Fatal(err)
		}
		if err := models.CompleteAgentReport(db, r.ID, "summary of "+key, responsibleDomain, "high", "# report"); err != nil {
			t.Fatal(err)
		}
		if err := models.UpdateGroupResponsible(db, key, responsibleDomain); err != nil {
			t.Fatal(err)
		}
		if err := models.SetGroupNeedsAnalysis(db, key, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := models.SetGroupState(db, kept, "resolved"); err != nil {
		t.Fatal(err)
	}
	g, err := models.GetGroup(db, kept)
	if err != nil {
		t.Fatal(err)
	}
	return kept, single, g.StateUpdatedAt.Time
}

// assertCarried checks that the kept group still has its report, state and
// responsible party and that the vanished group and its report are gone.
func assertCarried(t *testing.T, root, address, kept, single string, stateUpdated time.Time, singleGone bool, label string) {
	t.Helper()
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g, err := models.GetGroup(db, kept)
	if err != nil {
		t.Fatalf("%s: kept group missing: %v", label, err)
	}
	if g.State != "resolved" || !g.StateUpdatedAt.Valid || !g.StateUpdatedAt.Time.Equal(stateUpdated) {
		t.Errorf("%s: state = %s at %v, want resolved at %v", label, g.State, g.StateUpdatedAt, stateUpdated)
	}
	if g.NeedsAnalysis {
		t.Errorf("%s: needs_analysis was set although the group did not grow", label)
	}
	if g.Responsible != responsibleDomain {
		t.Errorf("%s: responsible = %q, want the agent's %q", label, g.Responsible, responsibleDomain)
	}
	if g.MessageCount < 2 {
		t.Errorf("%s: kept group lost messages: %+v", label, g)
	}
	r, err := models.LatestCompletedAgentReport(db, kept)
	if err != nil || r.Summary != "summary of "+kept || r.ReportMarkdown != "# report" || r.Severity != "high" {
		t.Errorf("%s: report of kept group = %+v (err %v)", label, r, err)
	}
	_, err = models.GetGroup(db, single)
	if singleGone {
		if err == nil {
			t.Errorf("%s: vanished group still exists", label)
		}
		if reports, _ := models.ListAgentReports(db, single); len(reports) != 0 {
			t.Errorf("%s: vanished group kept %d reports", label, len(reports))
		}
	} else if err != nil {
		t.Errorf("%s: single group missing: %v", label, err)
	} else if reports, _ := models.ListAgentReports(db, single); len(reports) != 1 {
		t.Errorf("%s: single group has %d reports, want 1", label, len(reports))
	}
}

func TestReclassifyKeepsReports(t *testing.T) {
	root, address := buildMailbox(t)
	kept, single, stateUpdated := seedReport(t, root, address)
	if _, err := Reclassify(context.Background(), root, address, nil); err != nil {
		t.Fatal(err)
	}
	assertCarried(t, root, address, kept, single, stateUpdated, false, "reclassify")
	// The report row itself survives with its id (nothing was deleted).
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := countRows(t, db, "agent_reports"); n != 2 {
		t.Errorf("agent_reports rows = %d, want 2", n)
	}
}

func TestReindexCarriesReportsOver(t *testing.T) {
	root, address := buildMailbox(t)
	kept, single, stateUpdated := seedReport(t, root, address)
	// Remove the raw files of the single-message group so that it vanishes.
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	msgs, _, err := models.ListMessages(db, models.MessageFilter{GroupKey: single})
	db.Close()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("single group messages: %v (%d)", err, len(msgs))
	}
	for _, ext := range []string{"eml", "txt", "html", "json"} {
		_ = os.Remove(MessageFilePath(root, address, msgs[0].MessageKey, ext))
	}
	var lines []string
	if _, err := Reindex(context.Background(), root, address, func(m string) { lines = append(lines, m) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "group states and 1 agent reports") {
		t.Errorf("carry-over not reported:\n%s", strings.Join(lines, "\n"))
	}
	assertCarried(t, root, address, kept, single, stateUpdated, true, "reindex")

	// A second reindex without a readable previous index (deleted) simply
	// rebuilds without reports.
	if err := removeIndexFiles(MailboxIndexPath(root, address)); err != nil {
		t.Fatal(err)
	}
	if _, err := Reindex(context.Background(), root, address, nil); err != nil {
		t.Fatal(err)
	}
	db, err = models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := countRows(t, db, "agent_reports"); n != 0 {
		t.Errorf("agent_reports rows after fresh reindex = %d, want 0", n)
	}
}

func TestOpenIndexRebuildsOutdatedSchema(t *testing.T) {
	root, address := buildMailbox(t)
	path := MailboxIndexPath(root, address)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Pretend the file was written by an older program version.
	if _, err := db.Exec(`PRAGMA user_version = -1`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := models.OpenMailIndex(path); err != models.ErrMailIndexOutdated {
		t.Fatalf("precondition: OpenMailIndex err = %v, want outdated", err)
	}
	var lines []string
	db, err = OpenIndex(context.Background(), root, address, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer db.Close()
	total, _, err := models.CountMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(samples) {
		t.Errorf("rebuilt index holds %d messages, want %d", total, len(samples))
	}
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "outdated") {
		t.Errorf("no rebuild progress reported: %v", lines)
	}
}

func TestReindexWithoutJSONAndCancel(t *testing.T) {
	root := t.TempDir()
	address := "ops@example.jp"
	dir := MailboxDir(root, address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Raw files copied in by hand: no .json with the IMAP identity.
	for i, name := range []string{"postfix_dsn.eml", "exim_bounce.eml", "normal.eml"} {
		key := MessageKey(time.Date(2025, 9, 1+i, 0, 0, 0, 0, time.UTC), "INBOX", 1, uint32(i+1))
		if err := os.WriteFile(filepath.Join(dir, key+".eml"), readSample(t, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Files that are not message keys are ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.eml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An older .json with an empty folder in its source block: the identity
	// is used, but the folder falls back to the default.
	oldKey := MessageKey(time.Date(2025, 9, 10, 0, 0, 0, 0, time.UTC), "", 77, 9)
	if err := os.WriteFile(filepath.Join(dir, oldKey+".eml"), readSample(t, "qmail_bounce.eml"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldJSON := `{"source":{"message_key":"` + oldKey + `","folder":"","uidvalidity":77,"uid":9}}`
	if err := os.WriteFile(filepath.Join(dir, oldKey+".json"), []byte(oldJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Reindex(context.Background(), root, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 4 || res.Bounces != 3 {
		t.Errorf("reindex = %+v", res)
	}
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := models.ListAllMessages(db)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint32]bool{}
	for _, m := range msgs {
		if m.UID == 0 || seen[m.UID] {
			t.Errorf("synthetic uid missing or duplicated: %+v", m)
		}
		if m.Folder != defaultFolder {
			t.Errorf("folder = %q, want %q: %+v", m.Folder, defaultFolder, m)
		}
		if m.MessageKey == oldKey && (m.UIDValidity != 77 || m.UID != 9) {
			t.Errorf("identity from the old json not used: %+v", m)
		}
		seen[m.UID] = true
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Reindex(ctx, root, address, nil); err != context.Canceled {
		t.Errorf("cancelled reindex err = %v", err)
	}
	if _, err := Reclassify(ctx, root, address, nil); err != context.Canceled {
		t.Errorf("cancelled reclassify err = %v", err)
	}
}

func TestStoreDedupesByMessageID(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw := readSample(t, "postfix_dsn.eml")
	tracker := newGroupTracker()
	opts := storeOptions{writeEML: true, dedupeByMessageID: true}
	if _, _, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 1, UID: 1}, opts, tracker); err != nil {
		t.Fatal(err)
	}
	// The same message seen again after a UIDVALIDITY change has a new key
	// but the same Message-ID: it must be skipped.
	_, _, err = storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 2, UID: 7}, opts, tracker)
	if !errors.Is(err, errDuplicateMessage) {
		t.Fatalf("second store err = %v, want errDuplicateMessage", err)
	}
	if total, _, _ := models.CountMessages(db); total != 1 {
		t.Errorf("index holds %d messages, want 1", total)
	}
	// Reindex does not deduplicate (files on disk are the truth).
	if _, _, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 2, UID: 7}, storeOptions{}, tracker); err != nil {
		t.Fatalf("store without dedupe: %v", err)
	}
}

func TestDateFallbackWhenHeaderMissing(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tracker := newGroupTracker()
	received := time.Date(2025, 9, 20, 1, 2, 3, 0, time.UTC)
	fetched := time.Date(2025, 9, 21, 4, 5, 6, 0, time.UTC)

	noDate := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: no date\r\nMessage-ID: <nodate@example.com>\r\n\r\nbody\r\n")
	msg, pm, err := storeMessage(db, dir, noDate, Source{Folder: "INBOX", UIDValidity: 1, UID: 1, ReceivedAt: received, FetchedAt: fetched}, storeOptions{writeEML: true}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Date.Valid || !msg.Date.Time.Equal(received) {
		t.Errorf("date = %v, want INTERNALDATE %v", msg.Date, received)
	}
	if !pm.Date.IsZero() {
		t.Errorf("parsed date should stay empty, got %v", pm.Date)
	}
	if _, ok := pm.Headers["Date"]; ok {
		t.Error("headers must not invent a Date")
	}
	data, err := ReadMessageFile(root, address, msg.MessageKey, "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\"date\":") {
		t.Errorf("json must omit the missing header date:\n%s", data)
	}

	badDate := []byte("From: a@example.com\r\nDate: yesterday-ish\r\nSubject: bad date\r\nMessage-ID: <baddate@example.com>\r\n\r\nbody\r\n")
	msg, pm, err = storeMessage(db, dir, badDate, Source{Folder: "INBOX", UIDValidity: 1, UID: 2, FetchedAt: fetched}, storeOptions{writeEML: true}, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Date.Valid || !msg.Date.Time.Equal(fetched) {
		t.Errorf("date = %v, want fetch time %v (no INTERNALDATE)", msg.Date, fetched)
	}
	if pm.Headers["Date"] != "yesterday-ish" {
		t.Errorf("raw Date header lost: %q", pm.Headers["Date"])
	}
	if !strings.HasPrefix(msg.MessageKey, fetched.Format("20060102-150405")) {
		t.Errorf("key %s does not use the same fallback as the date", msg.MessageKey)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "file.txt")
	if err := writeFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("content = %q, err %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", info.Mode())
		}
	}
}

func TestTestConnectionRejectsBadInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := TestConnection(ctx, &models.Mailbox{ImapHost: "imap.invalid", ImapSecurity: "ssl"}, "x"); err == nil {
		t.Error("expected an error with a cancelled context")
	}
	if err := TestConnection(context.Background(), &models.Mailbox{ImapHost: ""}, "x"); err == nil {
		t.Error("expected an error for an empty host")
	}
	if err := TestConnection(context.Background(), &models.Mailbox{ImapHost: "h", ImapSecurity: "bogus"}, "x"); err == nil {
		t.Error("expected an error for an unknown security mode")
	}
	if err := TestConnection(context.Background(), nil, "x"); err == nil {
		t.Error("expected an error for a nil mailbox")
	}
}
