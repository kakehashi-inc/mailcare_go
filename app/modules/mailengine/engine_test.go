package mailengine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/unicode"

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
	// Files live in the year / month directory of the key.
	if got := MessageFilePath(root, "a@x.y", key, "eml"); got != filepath.Join(root, "a@x.y", "2026", "09", key+".eml") {
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
	for _, ext := range []string{"exe", "json", "txt", "html", ""} {
		if got := MessageFilePath(root, "a@x.y", key, ext); got != "" {
			t.Errorf("MessageFilePath accepted extension %q: %q", ext, got)
		}
	}
	if _, err := ReadMessageFile(t.TempDir(), "a@x.y", key, "eml"); !os.IsNotExist(err) {
		t.Errorf("ReadMessageFile missing file err = %v, want not-exist", err)
	}

	// Section files are numbered from 1 whatever their count: -1, -2, ...
	dir := MailboxDir(root, "a@x.y")
	if got := SectionFilePath(dir, key, "txt", 1); got != filepath.Join(dir, "2026", "09", key+"-1.txt") {
		t.Errorf("SectionFilePath(1) = %q", got)
	}
	if got := SectionFilePath(dir, key, "html", 2); got != filepath.Join(dir, "2026", "09", key+"-2.html") {
		t.Errorf("SectionFilePath(2) = %q", got)
	}
	for _, bad := range []struct {
		key, ext string
		n        int
	}{{"../x", "txt", 1}, {key, "eml", 1}, {key, "json", 1}, {key, "txt", 0}, {key, "txt", -1}} {
		if got := SectionFilePath(dir, bad.key, bad.ext, bad.n); got != "" {
			t.Errorf("SectionFilePath(%q, %q, %d) = %q, want empty", bad.key, bad.ext, bad.n, got)
		}
	}
	// ReadBodySections reads the numbered files in order and skips missing ones.
	root = t.TempDir()
	dir = MailboxDir(root, "a@x.y")
	for i, content := range []string{"first", "second", "third"} {
		if err := writeFileAtomic(SectionFilePath(dir, key, "txt", i+1), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ReadBodySections(root, "a@x.y", key, "txt", 3); err != nil || strings.Join(got, ",") != "first,second,third" {
		t.Errorf("ReadBodySections = %q (err %v)", got, err)
	}
	if got, err := ReadBodySections(root, "a@x.y", key, "txt", 2); err != nil || strings.Join(got, ",") != "first,second" {
		t.Errorf("ReadBodySections(count 2) = %q (err %v)", got, err)
	}
	if err := os.Remove(SectionFilePath(dir, key, "txt", 2)); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadBodySections(root, "a@x.y", key, "txt", 3); err != nil || strings.Join(got, ",") != "first,third" {
		t.Errorf("ReadBodySections with a missing file = %q (err %v), want the others", got, err)
	}
	if got, err := ReadBodySections(root, "a@x.y", key, "html", 0); err != nil || got == nil || len(got) != 0 {
		t.Errorf("ReadBodySections(count 0) = %v (err %v), want an empty slice", got, err)
	}
	if _, err := ReadBodySections(root, "a@x.y", "../etc/passwd", "txt", 1); err != ErrInvalidMessageKey {
		t.Errorf("ReadBodySections invalid key err = %v", err)
	}
	if _, err := ReadBodySections(root, "a@x.y", key, "eml", 1); err == nil {
		t.Error("ReadBodySections accepted extension eml")
	}
	// ReadBodySection reads one numbered file and reports a missing one.
	if got, err := ReadBodySection(root, "a@x.y", key, "txt", 3); err != nil || got != "third" {
		t.Errorf("ReadBodySection(3) = %q (err %v)", got, err)
	}
	if _, err := ReadBodySection(root, "a@x.y", key, "txt", 2); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadBodySection of a missing file err = %v, want ErrNotExist", err)
	}
	if _, err := ReadBodySection(root, "a@x.y", key, "txt", 0); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadBodySection(0) err = %v, want an argument error", err)
	}
	if _, err := ReadBodySection(root, "a@x.y", "../etc/passwd", "txt", 1); err != ErrInvalidMessageKey {
		t.Errorf("ReadBodySection invalid key err = %v", err)
	}
	if _, err := ReadBodySection(root, "a@x.y", key, "eml", 1); err == nil {
		t.Error("ReadBodySection accepted extension eml")
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
	// A long diagnostic is kept whole: the template has no length cap.
	long := strings.TrimSpace(strings.Repeat("word ", 200))
	if got := DiagnosticTemplate(long); got != long {
		t.Errorf("long template altered: %d runes, want %d", len([]rune(got)), len([]rune(long)))
	}
	// Two notices for different addresses / IPs / IDs must collapse.
	a := DiagnosticTemplate("<a@x.example>: host mx1.x.example[192.0.2.1] said: 550 5.1.1 User unknown (id 1A2B3C4D5E)")
	b := DiagnosticTemplate("<b@y.example>: host mx2.y.example[198.51.100.9] said: 550 5.1.1 User unknown (id 9F8E7D6C5B)")
	if a != b {
		t.Errorf("templates differ:\n%s\n%s", a, b)
	}
}

func TestGroupKey(t *testing.T) {
	key := GroupKey(categoryIPBlocked, "203.0.113.5", "spamhaus.org")
	if len(key) != 16 {
		t.Errorf("group key must be 16 hex digits: %q", key)
	}
	// unit_value and authority are compared lower-cased and trimmed.
	if GroupKey(categoryIPBlocked, " 203.0.113.5 ", "Spamhaus.ORG") != key {
		t.Error("group key must ignore case and surrounding spaces")
	}
	if GroupKey(categoryIPBlocked, "203.0.113.5", "barracudacentral.org") == key {
		t.Error("a different authority must give a different key")
	}
	if GroupKey(categoryUserUnknown, "a@x.example", "") == GroupKey(categoryUserUnknown, "b@x.example", "") {
		t.Error("a different unit must give a different key")
	}
	if GroupKey(categoryUserUnknown, "a@x.example", "") == GroupKey(categoryMailboxFull, "a@x.example", "") {
		t.Error("a different category must give a different key")
	}
}

// sample is one testdata file with what the parser, classifier and extractor
// must produce for it.
type sample struct {
	file       string
	isBounce   bool
	kind       string
	rule       string
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
	responsble string // responsible party of the group (from the category)
	diagHas    string
	hasHTML    bool   // an HTML section with content exists (html_count = 1)
	noText     bool   // the text/plain part is missing or blank (text_count = 0)
	bodySource string // "" in the table means "text" unless noText, then "html" when hasHTML
	category   string // expected group category ("" = not grouped)
	unit       string
	authority  string
	actionable bool
}

var samples = []sample{
	{
		file: "postfix_dsn.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "taro.yamada@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteMTA: "mx.customer.example.com", origSubj: "【重要】9月のお知らせ", origMsgID: "news-20250902-0001@example.jp",
		responsble: responsibleRecipient, diagHas: "User unknown in virtual mailbox table",
		category: categoryUserUnknown, unit: "taro.yamada@customer.example.com",
	},
	{
		file: "postfix_delayed.eml", isBounce: true, kind: bounceKindDelayed, rule: "dsn_report",
		subject: "Delayed Mail (still being retried)", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "hanako@slowmail.example.net", domain: "slowmail.example.net", status: "4.4.1", smtp: "",
		remoteIP: "203.0.113.7", remoteMTA: "mx.slowmail.example.net", origSubj: "Weekly digest", origMsgID: "digest-0003@example.jp",
		responsble: responsibleDomain, diagHas: "Connection timed out",
		category: categoryDeliveryDelay, unit: "slowmail.example.net",
	},
	{
		file: "exim_bounce.eml", isBounce: true, kind: bounceKindFailed, rule: "daemon_sender",
		subject: "Mail delivery failed: returning message to sender", fromAddr: "Mailer-Daemon@mail.example.org",
		recipient: "jiro@nowhere.example.org", domain: "nowhere.example.org", status: "5.1.1", smtp: "550",
		remoteIP: "203.0.113.55", remoteMTA: "mx.nowhere.example.org",
		responsble: responsibleRecipient, diagHas: "does not exist",
		category: categoryUserUnknown, unit: "jiro@nowhere.example.org",
	},
	{
		file: "qmail_bounce.eml", isBounce: true, kind: bounceKindFailed, rule: "daemon_sender",
		subject: "failure notice", fromAddr: "MAILER-DAEMON@qmail.example.net",
		recipient: "saburo@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteIP: "198.51.100.25", responsble: responsibleRecipient, diagHas: "User unknown",
		category: categoryUserUnknown, unit: "saburo@customer.example.com",
	},
	{
		file: "office365_dsn.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		subject: "Undeliverable: Service maintenance notice", fromAddr: "MicrosoftExchange329e71ec88ae4615bbc36ab6ce41109e@corp.example.co.jp",
		recipient: "shiro@corp.example.co.jp", domain: "corp.example.co.jp", status: "5.1.10", smtp: "550",
		origSubj: "Service maintenance notice", origMsgID: "maint-0006@example.jp",
		responsble: responsibleRecipient, diagHas: "RecipientNotFound", hasHTML: true,
		category: categoryUserUnknown, unit: "shiro@corp.example.co.jp",
	},
	{
		file: "gmail_bounce.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		subject: "Delivery Status Notification (Failure)", fromAddr: "mailer-daemon@googlemail.com",
		recipient: "goro@gmail.com", domain: "gmail.com", status: "5.1.1", smtp: "550",
		remoteMTA: "gmail-smtp-in.l.google.com", origSubj: "Welcome aboard", origMsgID: "gm-0007@example.jp",
		responsble: responsibleRecipient, diagHas: "does not exist", hasHTML: true,
		category: categoryUserUnknown, unit: "goro@gmail.com",
	},
	{
		file: "gmail_overquota.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		subject: "Delivery Status Notification (Failure)", fromAddr: "mailer-daemon@googlemail.com",
		recipient: "kyuro@gmail.com", domain: "gmail.com", status: "4.2.2", smtp: "452",
		remoteMTA: "gmail-smtp-in.l.google.com", origSubj: "Welcome aboard", origMsgID: "gm-0009@example.jp",
		responsble: responsibleRecipient, diagHas: "over quota",
		category: categoryMailboxFull, unit: "kyuro@gmail.com",
	},
	{
		file: "sendmail_bounce.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		subject: "Returned mail: see transcript for details", fromAddr: "MAILER-DAEMON@relay.example.edu",
		recipient: "rokuro@dept.example.edu", domain: "dept.example.edu", status: "5.1.1", smtp: "550",
		remoteMTA: "mail.dept.example.edu", origSubj: "Campus event", origMsgID: "campus-0008@example.jp",
		responsble: responsibleRecipient, diagHas: "User unknown",
		category: categoryUserUnknown, unit: "rokuro@dept.example.edu",
	},
	{
		file: "spamhaus_block_a.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "ichiro@customer.example.com", domain: "customer.example.com", status: "5.7.1", smtp: "550",
		remoteMTA: "mx.customer.example.com", origSubj: "September campaign", origMsgID: "news-20250903-0011@example.jp",
		responsble: responsibleSender, diagHas: "blocked using zen.spamhaus.org",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "spamhaus.org", actionable: true,
	},
	{
		file: "spamhaus_block_b.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "hanako@partner.example.org", domain: "partner.example.org", status: "5.7.1", smtp: "550",
		remoteMTA: "mx.partner.example.org", origSubj: "September campaign", origMsgID: "news-20250904-0012@example.jp",
		responsble: responsibleSender, diagHas: "blocked using zen.spamhaus.org",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "spamhaus.org", actionable: true,
	},
	{
		// HTML-only Exchange NDR without a delivery-status part: detection
		// and extraction work from the HTML rendered as text.
		file: "exchange_html_only.eml", isBounce: true, kind: bounceKindFailed, rule: "daemon_sender",
		subject: "Undeliverable: Service maintenance notice", fromAddr: "postmaster@corp.example.co.jp",
		recipient: "kuro.suzuki@corp.example.co.jp", domain: "corp.example.co.jp", status: "5.1.1", smtp: "550",
		responsble: responsibleRecipient, diagHas: "RESOLVER.ADR.RecipNotFound", hasHTML: true, noText: true,
		category: categoryUserUnknown, unit: "kuro.suzuki@corp.example.co.jp",
	},
	{
		// A blank text/plain part next to an HTML bounce: the HTML is used.
		file: "blank_plain_html.eml", isBounce: true, kind: bounceKindFailed, rule: "daemon_sender",
		subject: "Mail delivery failed: returning message to sender", fromAddr: "MAILER-DAEMON@relay.example.net",
		recipient: "momo@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteIP: "198.51.100.25", remoteMTA: "mx.customer.example.com",
		responsble: responsibleRecipient, diagHas: "User unknown", hasHTML: true, noText: true,
		category: categoryUserUnknown, unit: "momo@customer.example.com",
	},
	{
		// A blank HTML part next to a text bounce: the text is used, has_html is false.
		file: "plain_blank_html.eml", isBounce: true, kind: bounceKindFailed, rule: "daemon_sender",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "yuki@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteIP: "198.51.100.25", remoteMTA: "mx.customer.example.com",
		responsble: responsibleRecipient, diagHas: "User unknown in virtual mailbox table",
		category: categoryUserUnknown, unit: "yuki@customer.example.com",
	},
	{
		file: "autoreply.eml", isBounce: true, kind: bounceKindAutoReply, rule: "auto_reply",
		subject: "自動返信: Re: 9月のお知らせ", fromAddr: "nanako@partner.example.com",
	},
	{
		file: "normal.eml", isBounce: false, subject: "Re: September campaign - question about pricing",
		fromAddr: "hachiro@client.example.com", hasHTML: true,
	},
	{
		file: "malformed.eml", isBounce: true, kind: bounceKindFailed, rule: "dsn_report",
		fromAddr:  "MAILER-DAEMON@broken.example.net",
		recipient: "kuro@broken.example.net", domain: "broken.example.net", status: "5.7.1", smtp: "550",
		remoteIP: "192.0.2.99", remoteMTA: "mx.broken.example.net", responsble: responsibleSender,
		diagHas: "rejected due to policy",
		// No original message in the broken sample: the sender address falls
		// back to the reporting MTA, which the sample lacks too, so the final
		// fallback (the recipient domain) becomes the unit.
		category: categoryContentRejected, unit: "broken.example.net", authority: "broken.example.net", actionable: true,
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
			wantSource := s.bodySource
			if wantSource == "" {
				switch {
				case !s.noText:
					wantSource = bodySourceText
				case s.hasHTML:
					wantSource = bodySourceHTML
				}
			}
			wantText, wantHTML := 1, 0
			if s.noText {
				wantText = 0
			}
			if s.hasHTML {
				wantHTML = 1
			}
			if pm.TextCount() != wantText || pm.HTMLCount() != wantHTML || pm.BodySource != wantSource {
				t.Errorf("text_count = %d, html_count = %d, body_source = %q; want %d / %d / %q",
					pm.TextCount(), pm.HTMLCount(), pm.BodySource, wantText, wantHTML, wantSource)
			}
			for _, sec := range pm.TextSections {
				if strings.TrimSpace(sec) == "" {
					t.Error("blank text section kept")
				}
			}
			for _, sec := range pm.HTMLSections {
				if htmlToText(sec) == "" {
					t.Error("skeleton HTML section kept")
				}
			}
			cls := Classify(pm)
			if cls.IsBounce != s.isBounce || cls.Kind != s.kind || cls.Rule != s.rule {
				t.Fatalf("classification = %+v, want bounce=%v kind=%s rule=%s", cls, s.isBounce, s.kind, s.rule)
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
			check("recipient", b.Recipient, s.recipient)
			check("domain", b.RecipientDomain, s.domain)
			check("status", b.StatusCode, s.status)
			check("smtp", b.SMTPCode, s.smtp)
			check("remote_ip", b.RemoteIP, s.remoteIP)
			check("remote_mta", b.RemoteMTA, s.remoteMTA)
			check("original_subject", b.OriginalSubject, s.origSubj)
			check("original_message_id", b.OriginalMessageID, s.origMsgID)
			if b.ID != 0 || b.GroupKey != "" {
				t.Errorf("ExtractBounce set id %d / group %q; groupMessage decides them", b.ID, b.GroupKey)
			}
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
			c := Categorize(b, cls.Kind)
			if c.Category != s.category || c.UnitValue != s.unit || c.Authority != s.authority || c.Actionable != s.actionable {
				t.Errorf("category = %+v, want %s / %q / %q / actionable=%v", c, s.category, s.unit, s.authority, s.actionable)
			}
			g := groupForBounce(cls.Kind, b)
			if g == nil || g.Category != c.Category || g.UnitValue != c.UnitValue || g.Authority != c.Authority ||
				g.Actionable != c.Actionable || g.Responsible != c.Responsible || g.GroupKey != GroupKey(c.Category, c.UnitValue, c.Authority) {
				t.Errorf("group row = %+v, want the category values %+v", g, c)
			}
			// The responsible party is decided by the category and lives on
			// the group only.
			check("responsible", c.Responsible, s.responsble)
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
	if g2 := groupForBounce(bounceKindDelayed, &other); g2.GroupKey != g.GroupKey || g2.Label() != g.Label() {
		t.Errorf("group key/label depend on the SMTP code: %s/%s vs %s/%s", g.GroupKey, g.Label(), g2.GroupKey, g2.Label())
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
	if pm.TextCount() != 1 || !strings.Contains(pm.TextSections[0], "This is the mail system at host mx1.example.jp.") {
		t.Errorf("text body not taken from the notification part: %q", pm.TextSections)
	}
	if strings.Contains(pm.bodyForClassification(), "9月のお知らせをお送りします") {
		t.Error("text body must not include the embedded original message")
	}
	if pm.Headers["Message-Id"] == "" && pm.Headers["Message-ID"] == "" {
		t.Errorf("headers map lacks Message-Id: %v", pm.Headers)
	}

	// The To display name is kept for messages.to_name (MIME and fallback parser).
	// to_address keeps the bare addresses only; the display name goes to to_name.
	named := ParseMessage([]byte("From: Sender Name <a@example.com>\r\nTo: Newsletter Desk <newsletter@example.jp>, ops@example.jp\r\nSubject: x\r\n\r\nbody\r\n"))
	if named.ToName != "Newsletter Desk" || named.To != "newsletter@example.jp, ops@example.jp" {
		t.Errorf("to = %q / to_name = %q", named.To, named.ToName)
	}
	if named.FromAddress != "a@example.com" || named.FromName != "Sender Name" {
		t.Errorf("from = %q / from_name = %q", named.FromAddress, named.FromName)
	}
	// The same through the fallback header parser (broken MIME).
	broken := ParseMessage([]byte("From: Sender Name <a@example.com>\nTo: Newsletter Desk <newsletter@example.jp>\nSubject: x\nContent-Type: multipart/mixed; boundary=\n\n--\n"))
	if broken.To != "newsletter@example.jp" || broken.ToName != "Newsletter Desk" || broken.FromAddress != "a@example.com" || broken.FromName != "Sender Name" {
		t.Errorf("fallback: to = %q / to_name = %q / from = %q / from_name = %q", broken.To, broken.ToName, broken.FromAddress, broken.FromName)
	}
	if pm.ToName != "" {
		t.Errorf("to_name = %q, want empty for a bare address", pm.ToName)
	}

	// The malformed sample must still yield headers, a body and no panic.
	bad := ParseMessage(readSample(t, "malformed.eml"))
	if bad.ParseError == "" {
		t.Error("malformed message should record a parse error")
	}
	if bad.FromAddress != "MAILER-DAEMON@broken.example.net" || bad.TextCount() != 1 {
		t.Errorf("malformed fallback = from %q sections %q", bad.FromAddress, bad.TextSections)
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
}

func TestUnfoldAndClassifyText(t *testing.T) {
	if got := unfold("<a@b.example>: host mx[1.2.3.4] said: 550\n    5.1.1 user unknown\nnext line"); !strings.Contains(got, "said: 550 5.1.1 user unknown\nnext line") {
		t.Errorf("unfold = %q", got)
	}
	pm := &ParsedMessage{Subject: "Warning: could not send message for past 4 hours", FromAddress: "postmaster@x.example",
		TextSections: []string{"Will keep trying until message is 5 days old"}, Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindDelayed || c.Rule != "daemon_sender" {
		t.Errorf("delayed daemon mail classified as %+v", c)
	}
	pm = &ParsedMessage{Subject: "Out of Office: Re: hello", FromAddress: "someone@x.example", Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindAutoReply {
		t.Errorf("out of office classified as %+v", c)
	}
	pm = &ParsedMessage{Subject: "hello", FromAddress: "someone@x.example", FromName: "Mail Delivery Subsystem", Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindOther || c.Rule != "daemon_display_name" {
		t.Errorf("daemon display name classified as %+v", c)
	}
	pm = &ParsedMessage{Subject: "配信できませんでした", FromAddress: "someone@x.example", Headers: map[string]string{}}
	if c := Classify(pm); c.Kind != bounceKindFailed || c.Rule != "subject_pattern" {
		t.Errorf("japanese subject classified as %+v", c)
	}
}

// buildMailbox stores every sample through storeMessage (the fetch phase)
// into a fresh index, runs the grouping phase and returns the paths (used by
// the round-trip tests).
func buildMailbox(t *testing.T) (root, address string) {
	t.Helper()
	root = t.TempDir()
	address = "newsletter@example.jp"
	storeSamples(t, root, address, 1, samples...)
	if _, err := GroupMailbox(context.Background(), root, address, false, nil); err != nil {
		t.Fatalf("group: %v", err)
	}
	return root, address
}

// storeSamples runs the fetch-equivalent storeMessage for the given samples
// with UIDs starting at firstUID (no classification, classified = 0).
func storeSamples(t *testing.T, root, address string, firstUID uint32, list ...sample) {
	t.Helper()
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, s := range list {
		raw := readSample(t, s.file)
		src := Source{Folder: "INBOX", UIDValidity: 1700000000, UID: firstUID + uint32(i), ReceivedAt: time.Now().UTC(), FetchedAt: time.Now().UTC()}
		msg, _, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: true})
		if err != nil {
			t.Fatalf("store %s: %v", s.file, err)
		}
		if msg.Classified || msg.IsBounce || msg.GroupKey != "" {
			t.Fatalf("store %s classified the message: %+v", s.file, msg)
		}
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustListAllMessages(t *testing.T, db *sql.DB) []*models.Message {
	t.Helper()
	msgs, err := models.ListAllMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// countBounceDetails counts the messages that have a bounces row, read
// through the models API, and checks that the row agrees with the message.
func countBounceDetails(t *testing.T, db *sql.DB) int {
	t.Helper()
	n := 0
	for _, m := range mustListAllMessages(t, db) {
		if b, err := models.GetBounceByMessageID(db, m.ID); err == nil {
			if b.Recipient == "" && b.Diagnostic == "" && b.StatusCode == "" {
				t.Errorf("bounce details of %s are empty: %+v", m.MessageKey, b)
			}
			if b.ID != m.ID || b.GroupKey != m.GroupKey || !m.IsBounce || m.BounceKind == bounceKindAutoReply {
				t.Errorf("bounces row of %s disagrees with the message: %+v vs %+v", m.MessageKey, b, m)
			}
			n++
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	return n
}

// assertSectionFiles checks that the mailbox directory holds exactly the
// section files the index rows announce (text_count / html_count, numbered
// without gaps), the .eml of every row, and nothing else (no temporary
// file).
func assertSectionFiles(t *testing.T, root, address string, msgs []*models.Message, label string) {
	t.Helper()
	dir := MailboxDir(root, address)
	want := map[string]bool{}
	for _, m := range msgs {
		sub := MessageDir(dir, m.MessageKey)
		want[filepath.Join(sub, m.MessageKey+".eml")] = true
		for n := 1; n <= m.TextCount; n++ {
			want[filepath.Join(sub, sectionFileName(m.MessageKey, "txt", n))] = true
		}
		for n := 1; n <= m.HTMLCount; n++ {
			want[filepath.Join(sub, sectionFileName(m.MessageKey, "html", n))] = true
		}
	}
	err := walkMailboxFiles(dir, func(sub string, e os.DirEntry) {
		path := filepath.Join(sub, e.Name())
		if !want[path] {
			t.Errorf("%s: unexpected file %s (stale sections, temporary files and files outside their year/month directory must not exist)", label, path)
		}
		delete(want, path)
	})
	if err != nil {
		t.Fatal(err)
	}
	for name := range want {
		t.Errorf("%s: file %s missing", label, name)
	}
}

func TestStoreAndReindexRoundTrip(t *testing.T) {
	root, address := buildMailbox(t)
	dir := MailboxDir(root, address)

	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	assertSectionFiles(t, root, address, mustListAllMessages(t, db), "after fetch")
	var txts, htmls, wantHTML, wantTxt int
	for _, s := range samples {
		if s.hasHTML {
			wantHTML++
		}
		if !s.noText {
			wantTxt++
		}
	}
	for _, m := range mustListAllMessages(t, db) {
		txts += m.TextCount
		htmls += m.HTMLCount
	}
	if txts != wantTxt || htmls != wantHTML {
		t.Errorf("section counts: %d txt, %d html; want %d / %d", txts, htmls, wantTxt, wantHTML)
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
	bouncesBefore := countBounceDetails(t, db)
	if groupsBefore == 0 || bouncesBefore != wantBounces-1 { // the auto-reply has no bounce details
		t.Fatalf("groups=%d bounces=%d before reindex", groupsBefore, bouncesBefore)
	}
	var withGroup int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bounces WHERE group_key <> ''`).Scan(&withGroup); err != nil {
		t.Fatal(err)
	}
	if withGroup != wantBounces-1 {
		t.Errorf("%d bounces carry a group key, want %d", withGroup, wantBounces-1)
	}
	autoReplies, joined := 0, 0
	for _, m := range mustListAllMessages(t, db) {
		if m.GroupKey != "" {
			joined++
		}
		if m.BounceKind == bounceKindAutoReply {
			autoReplies++
			if m.GroupKey != "" {
				t.Errorf("auto reply %s has group key %q, want empty", m.MessageKey, m.GroupKey)
			}
			if _, err := models.GetBounceByMessageID(db, m.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("auto reply %s has a bounces row (err %v)", m.MessageKey, err)
			}
		}
		if !m.IsBounce && m.Rule != "" {
			t.Errorf("non-bounce %s records rule %q", m.MessageKey, m.Rule)
		}
		if m.IsBounce && m.Rule == "" {
			t.Errorf("bounce %s records no rule", m.MessageKey)
		}
	}
	if joined != wantBounces-1 {
		t.Errorf("%d messages read back with a group key (via the bounces join), want %d", joined, wantBounces-1)
	}
	if autoReplies != 1 {
		t.Errorf("%d auto replies indexed, want 1", autoReplies)
	}
	spamGroup, err := models.GetGroup(db, GroupKey(categoryIPBlocked, "203.0.113.5", "spamhaus.org"))
	if err != nil {
		t.Fatal(err)
	}
	if spamGroup.MessageCount != 2 || spamGroup.RecipientCount != 2 || spamGroup.RemoteIPCount != 2 ||
		!spamGroup.FirstSeen.Valid || !spamGroup.LastSeen.Valid || spamGroup.FirstSeen.Time.After(spamGroup.LastSeen.Time) ||
		spamGroup.Label() != "ip_blocked: 203.0.113.5 @ spamhaus.org" || spamGroup.StatusCode != "5.7.1" || spamGroup.DiagnosticTemplate == "" {
		t.Errorf("group counters / details not refreshed: %+v", spamGroup)
	}
	// The two Spamhaus listings of the same IP (different recipient domains)
	// form one actionable group; the two 5.1.1 notices to customer.example.com
	// (different recipients) form two recipient-side groups.
	spamhausGroups, unknownGroups := map[string]bool{}, map[string]bool{}
	for _, m := range mustListAllMessages(t, db) {
		b, err := models.GetBounceByMessageID(db, m.ID)
		if err != nil {
			continue
		}
		if b.StatusCode == "5.7.1" && strings.Contains(b.Diagnostic, "spamhaus") {
			spamhausGroups[m.GroupKey] = true
		}
		if b.RecipientDomain == "customer.example.com" && b.StatusCode == "5.1.1" {
			unknownGroups[m.GroupKey] = true
		}
	}
	if len(spamhausGroups) != 1 {
		t.Errorf("spamhaus notices spread over %d groups, want 1", len(spamhausGroups))
	}
	// taro.yamada (postfix), saburo (qmail), momo (blank plain + HTML) and
	// yuki (plain + blank HTML): one group per recipient address.
	if len(unknownGroups) != 4 {
		t.Errorf("user_unknown notices to customer.example.com in %d groups, want 4 (one per recipient)", len(unknownGroups))
	}
	var unclassified int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE classified = 0`).Scan(&unclassified); err != nil {
		t.Fatal(err)
	}
	if unclassified != 0 {
		t.Errorf("%d messages still unclassified after grouping", unclassified)
	}
	rowsBefore := snapshotMessages(t, db)
	db.Close()

	// Corrupt the section files: reindex must regenerate them from the .eml.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
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
	if countBounceDetails(t, db) != bouncesBefore || countRows(t, db, "groups") != groupsBefore {
		t.Errorf("bounces/groups differ after reindex: %d/%d", countBounceDetails(t, db), countRows(t, db, "groups"))
	}
	assertSectionFiles(t, root, address, mustListAllMessages(t, db), "after reindex")
	for key, row := range rowsBefore {
		sections, err := ReadBodySections(root, address, key, "txt", row.TextCount)
		if err != nil || len(sections) != row.TextCount {
			t.Errorf("%s: %d text sections read (err %v), want %d", key, len(sections), err, row.TextCount)
		}
		for n, sec := range sections {
			if sec == "stale" {
				t.Errorf("reindex did not regenerate %s", sectionFileName(key, "txt", n))
			}
		}
	}

	// A full grouping keeps the messages and rebuilds the classification.
	gres, err := GroupMailbox(context.Background(), root, address, true, nil)
	if err != nil {
		t.Fatalf("full grouping: %v", err)
	}
	if gres.Processed != len(samples) || gres.Bounces != wantBounces || gres.Groups != groupsBefore {
		t.Errorf("full grouping result = %+v, want %d/%d/%d", gres, len(samples), wantBounces, groupsBefore)
	}
	rowsReclassified := snapshotMessages(t, db)
	for key, before := range rowsBefore {
		if after := rowsReclassified[key]; after != before {
			t.Errorf("message %s changed by the full grouping:\n before %+v\n after  %+v", key, before, after)
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

// messageRow is the comparable subset of a messages row (everything reindex
// must reproduce, including the IMAP identity and the fetch facts carried
// over from the previous index).
type messageRow struct {
	UID, UIDValidity                                                  uint32
	Folder, MessageID, Subject, From, FromName, To, Kind, Rule, Group string
	IsBounce                                                          bool
	TextCount, HTMLCount                                              int
	ToName, BodySource                                                string
	Size                                                              int64
	Date, ReceivedAt, FetchedAt                                       string
}

func snapshotMessages(t *testing.T, db *sql.DB) map[string]messageRow {
	t.Helper()
	msgs, err := models.ListAllMessages(db)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]messageRow{}
	for _, m := range msgs {
		if m.Date.IsZero() {
			t.Errorf("message %s has a zero date", m.MessageKey)
		}
		received := ""
		if m.ReceivedAt.Valid {
			received = m.ReceivedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		out[m.MessageKey] = messageRow{
			UID: m.UID, UIDValidity: m.UIDValidity, Folder: m.Folder, MessageID: m.MessageID, Subject: m.Subject,
			From: m.FromAddress, FromName: m.FromName, To: m.ToAddress, Kind: m.BounceKind, Rule: m.Rule,
			Group: m.GroupKey, IsBounce: m.IsBounce, TextCount: m.TextCount, HTMLCount: m.HTMLCount, BodySource: m.BodySource,
			Size: m.Size, Date: m.Date.UTC().Format(time.RFC3339), ReceivedAt: received,
			FetchedAt: m.FetchedAt.UTC().Format(time.RFC3339Nano), ToName: m.ToName,
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

func TestFullGroupingKeepsReports(t *testing.T) {
	root, address := buildMailbox(t)
	kept, single, stateUpdated := seedReport(t, root, address)
	res, err := GroupMailbox(context.Background(), root, address, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing grew, so no actionable group is reported as touched.
	if len(res.GroupsTouched) != 0 {
		t.Errorf("full grouping touched %v although no group grew", res.GroupsTouched)
	}
	assertCarried(t, root, address, kept, single, stateUpdated, false, "full grouping")
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

// removeGroupFiles deletes the raw file and the section files of every
// message of a group so that the group vanishes from the next reindex.
func removeGroupFiles(t *testing.T, root, address, groupKey string) {
	t.Helper()
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	msgs, _, err := models.ListMessages(db, models.MessageFilter{GroupKey: groupKey})
	db.Close()
	if err != nil || len(msgs) == 0 {
		t.Fatalf("messages of group %s: %v (%d)", groupKey, err, len(msgs))
	}
	for _, m := range msgs {
		_ = os.Remove(MessageFilePath(root, address, m.MessageKey, "eml"))
		for _, ext := range []string{"txt", "html"} {
			for n := 1; n <= 3; n++ {
				_ = os.Remove(SectionFilePath(MailboxDir(root, address), m.MessageKey, ext, n))
			}
		}
	}
}

func TestReindexCarriesReportsOver(t *testing.T) {
	root, address := buildMailbox(t)
	kept, single, stateUpdated := seedReport(t, root, address)
	// Remove the raw files of the single-message group so that it vanishes.
	removeGroupFiles(t, root, address, single)
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
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := countRows(t, db, "agent_reports"); n != 0 {
		t.Errorf("agent_reports rows after fresh reindex = %d, want 0", n)
	}
}

// TestReindexKeepsReportIDs: a carried-over report keeps its id, because the
// run's workspace directory data/agent/<address>/<group_key>/<id>/ is named
// after it. The vanished group's report sits between the kept group's two
// reports, so a rebuilt table that simply renumbered would shift the second
// one.
func TestReindexKeepsReportIDs(t *testing.T) {
	root, address := buildMailbox(t)
	kept, single, _ := seedReport(t, root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	second := &models.AgentReport{GroupKey: kept, Provider: "codex", MessageCount: 2}
	if err := models.InsertAgentReport(db, second); err != nil {
		t.Fatal(err)
	}
	if err := models.CompleteAgentReport(db, second.ID, "second summary", responsibleSender, "low", "# second"); err != nil {
		t.Fatal(err)
	}
	before, err := models.ListAgentReports(db, kept)
	db.Close()
	if err != nil || len(before) != 2 || before[0].ID != second.ID || before[0].ID <= before[1].ID+1 {
		t.Fatalf("reports of the kept group before reindex: %+v, %v", before, err)
	}
	removeGroupFiles(t, root, address, single)
	if _, err := Reindex(context.Background(), root, address, nil); err != nil {
		t.Fatal(err)
	}
	db, err = models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := models.ListAgentReports(db, kept)
	if err != nil || len(after) != len(before) {
		t.Fatalf("reports of the kept group after reindex: %+v, %v", after, err)
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Summary != before[i].Summary || after[i].Status != before[i].Status ||
			!after[i].CreatedAt.Equal(before[i].CreatedAt) {
			t.Errorf("report %d: got %+v, want %+v", i, after[i], before[i])
		}
	}
	if latest, err := models.LatestCompletedAgentReport(db, kept); err != nil || latest.ID != second.ID || latest.Summary != "second summary" {
		t.Errorf("latest completed report = %+v, %v; want id %d", latest, err, second.ID)
	}
	if n := countRows(t, db, "agent_reports"); n != 2 {
		t.Errorf("agent_reports rows = %d, want 2", n)
	}
	// The next run gets a fresh id above every restored one.
	fresh := &models.AgentReport{GroupKey: kept, Provider: "codex", MessageCount: 2}
	if err := models.InsertAgentReport(db, fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.ID <= second.ID {
		t.Errorf("new report id %d not above the restored %d", fresh.ID, second.ID)
	}
	// A restore without an id is refused rather than renumbered.
	if err := models.RestoreAgentReport(db, &models.AgentReport{GroupKey: kept, Provider: "codex", Status: "completed"}); err == nil {
		t.Errorf("restore without an id accepted")
	}
}

// TestReportResponsibleUnknownKeepsRuleValue: re-applying the responsible
// party of the latest completed report after a reindex / full grouping
// replaces the rule-based value only with a definite answer, as the analysis
// itself does; "unknown", an empty or an unexpected value leaves it alone.
func TestReportResponsibleUnknownKeepsRuleValue(t *testing.T) {
	root, address := buildMailbox(t)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	groups, err := models.ListGroups(db, models.GroupFilter{})
	if err != nil || len(groups) < 4 {
		t.Fatalf("need at least four groups: %v (%d)", err, len(groups))
	}
	// One group per report value; every group keeps its rule-based value
	// except the one whose report names a definite other party.
	cases := map[string]string{} // group key -> report responsible
	want := map[string]string{}  // group key -> expected groups.responsible
	values := []string{responsibleUnknown, "", "bogus", ""}
	for i, g := range groups[:4] {
		value := values[i]
		if i == 3 {
			// A definite answer that differs from the rule-based value.
			value = responsibleDomain
			if g.Responsible == responsibleDomain {
				value = responsibleSender
			}
		}
		cases[g.GroupKey] = value
		want[g.GroupKey] = g.Responsible
		if i == 3 {
			want[g.GroupKey] = value
		}
		r := &models.AgentReport{GroupKey: g.GroupKey, Provider: "codex", MessageCount: g.MessageCount}
		if err := models.InsertAgentReport(db, r); err != nil {
			t.Fatal(err)
		}
		if err := models.CompleteAgentReport(db, r.ID, "summary", value, "low", "# report"); err != nil {
			t.Fatal(err)
		}
	}
	check := func(label string) {
		t.Helper()
		for key, responsible := range want {
			g, err := models.GetGroup(db, key)
			if err != nil {
				t.Fatalf("%s: group %s: %v", label, key, err)
			}
			if g.Responsible != responsible {
				t.Errorf("%s: group %s (report says %q): responsible = %q, want %q", label, key, cases[key], g.Responsible, responsible)
			}
		}
	}
	if err := reapplyReportResponsible(db); err != nil {
		t.Fatal(err)
	}
	check("reapply")
	db.Close()

	if _, err := GroupMailbox(context.Background(), root, address, true, nil); err != nil {
		t.Fatal(err)
	}
	db, err = models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	check("full grouping")
	db.Close()

	if _, err := Reindex(context.Background(), root, address, nil); err != nil {
		t.Fatal(err)
	}
	db, err = models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check("reindex")
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
	joined := strings.Join(lines, "\n")
	if len(lines) == 0 || !strings.Contains(joined, "outdated") {
		t.Errorf("no rebuild progress reported: %v", lines)
	}
	// The old index cannot be read through the models, so nothing is
	// carried over and the reason is reported.
	if !strings.Contains(joined, "not carried over") || !strings.Contains(joined, "outdated schema") {
		t.Errorf("carry-over skip not reported:\n%s", joined)
	}
}

// TestReindexWithoutSourcesAndCancel covers raw files the index never knew
// (no previous index at all): they get a synthetic identity, files that are
// not message originals are left alone, and a cancelled or failed rebuild
// leaves no build file behind.
func TestReindexWithoutSourcesAndCancel(t *testing.T) {
	root := t.TempDir()
	address := "ops@example.jp"
	dir := MailboxDir(root, address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Raw files copied in by hand: no index row with the IMAP identity.
	var keys []string
	for i, name := range []string{"postfix_dsn.eml", "exim_bounce.eml", "normal.eml"} {
		key := MessageKey(time.Date(2025, 9, 1+i, 0, 0, 0, 0, time.UTC), "INBOX", 1, uint32(i+1))
		if err := writeFileAtomic(rawFilePath(dir, key), readSample(t, name), 0o600); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	// Files that are not message keys are ignored.
	month := MessageDir(dir, keys[0])
	if err := os.WriteFile(filepath.Join(month, "README.eml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(month, "notes.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	var lines []string
	res, err := Reindex(context.Background(), root, address, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 3 || res.Bounces != 2 {
		t.Errorf("reindex = %+v", res)
	}
	for _, name := range []string{"README.eml", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(month, name)); err != nil {
			t.Errorf("unrelated file %s removed: %v", name, err)
		}
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
	if len(msgs) != 3 {
		t.Fatalf("%d messages indexed, want 3", len(msgs))
	}
	seen := map[uint32]bool{}
	for _, m := range msgs {
		if m.UID == 0 || seen[m.UID] || m.UIDValidity != 0 {
			t.Errorf("synthetic identity missing or duplicated: %+v", m)
		}
		if m.Folder != defaultFolder {
			t.Errorf("folder = %q, want %q: %+v", m.Folder, defaultFolder, m)
		}
		if !m.ReceivedAt.Valid || m.Size == 0 || m.FetchedAt.IsZero() {
			t.Errorf("synthetic row lacks received_at / size / fetched_at: %+v", m)
		}
		seen[m.UID] = true
	}
	// The next rebuild finds the rows in the previous index and carries the
	// same identity over (the raw file still records nothing).
	before := snapshotMessages(t, mustOpenIndex(t, root, address))
	if _, err := Reindex(context.Background(), root, address, nil); err != nil {
		t.Fatal(err)
	}
	after := snapshotMessages(t, mustOpenIndex(t, root, address))
	for key, row := range before {
		if after[key] != row {
			t.Errorf("second reindex changed %s:\n before %+v\n after  %+v", key, row, after[key])
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Reindex(ctx, root, address, nil); err != context.Canceled {
		t.Errorf("cancelled reindex err = %v", err)
	}
	if _, err := GroupMailbox(ctx, root, address, true, nil); err != context.Canceled {
		t.Errorf("cancelled grouping err = %v", err)
	}
	// A rebuild cancelled mid-run (after the build file was created) leaves
	// no build file behind and keeps the previous index.
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	_, err = Reindex(cctx, root, address, func(m string) {
		if strings.HasPrefix(m, "reindexing ") {
			ccancel()
		}
	})
	if err != context.Canceled {
		t.Errorf("reindex cancelled mid-run err = %v", err)
	}
	buildPath := MailboxIndexPath(root, address) + ".rebuild"
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(buildPath + suffix); !os.IsNotExist(err) {
			t.Errorf("build file %s left behind (err %v)", filepath.Base(buildPath+suffix), err)
		}
	}
	if _, err := os.Stat(MailboxIndexPath(root, address)); err != nil {
		t.Errorf("index missing after the cancelled rebuild: %v", err)
	}
	if total, _, err := models.CountMessages(mustOpenIndex(t, root, address)); err != nil || total != len(keys) {
		t.Errorf("previous index changed by the cancelled rebuild: %d messages (err %v)", total, err)
	}
}

// mustOpenIndex opens the index of a mailbox and closes it when the test ends.
func mustOpenIndex(t *testing.T, root, address string) *sql.DB {
	t.Helper()
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
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
	opts := storeOptions{writeEML: true, dedupeByMessageID: true}
	if _, _, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 1, UID: 1}, opts); err != nil {
		t.Fatal(err)
	}
	// The same message seen again after a UIDVALIDITY change has a new key
	// but the same Message-ID: it must be skipped.
	_, _, err = storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 2, UID: 7}, opts)
	if !errors.Is(err, errDuplicateMessage) {
		t.Fatalf("second store err = %v, want errDuplicateMessage", err)
	}
	if total, _, _ := models.CountMessages(db); total != 1 {
		t.Errorf("index holds %d messages, want 1", total)
	}
	// Reindex does not deduplicate (files on disk are the truth).
	if _, _, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 2, UID: 7}, storeOptions{}); err != nil {
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
	received := time.Date(2025, 9, 20, 1, 2, 3, 0, time.UTC)
	fetched := time.Date(2025, 9, 21, 4, 5, 6, 0, time.UTC)

	noDate := []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: no date\r\nMessage-ID: <nodate@example.com>\r\n\r\nbody\r\n")
	msg, pm, err := storeMessage(db, dir, noDate, Source{Folder: "INBOX", UIDValidity: 1, UID: 1, ReceivedAt: received, FetchedAt: fetched}, storeOptions{writeEML: true})
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Date.Equal(received) {
		t.Errorf("date = %v, want INTERNALDATE %v", msg.Date, received)
	}
	if !pm.Date.IsZero() {
		t.Errorf("parsed date should stay empty, got %v", pm.Date)
	}
	if _, ok := pm.Headers["Date"]; ok {
		t.Error("headers must not invent a Date")
	}

	badDate := []byte("From: a@example.com\r\nDate: yesterday-ish\r\nSubject: bad date\r\nMessage-ID: <baddate@example.com>\r\n\r\nbody\r\n")
	msg, pm, err = storeMessage(db, dir, badDate, Source{Folder: "INBOX", UIDValidity: 1, UID: 2, FetchedAt: fetched}, storeOptions{writeEML: true})
	if err != nil {
		t.Fatal(err)
	}
	if !msg.Date.Equal(fetched) {
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

// categoryCase is one realistic diagnostic with the category, unit and
// authority the rule table must derive for it.
type categoryCase struct {
	name       string
	kind       string
	status     string
	diag       string
	recipient  string
	from       string // original_from
	remoteIP   string
	reporting  string
	category   string
	unit       string
	authority  string
	actionable bool
	respons    string
}

var categoryCases = []categoryCase{
	{
		name: "gmail ip blocked", kind: bounceKindDelayed, status: "4.7.0",
		diag:      "421-4.7.0 [203.0.113.5 19] Our system has detected an unusual rate of 421-4.7.0 unsolicited mail originating from your IP address. To protect our 421-4.7.0 users from spam, mail sent from your IP address has been blocked. 421-4.7.0 Please visit https://support.google.com/mail/answer/81126 for more information.",
		recipient: "someone@gmail.com", from: "Newsletter <newsletter@example.jp>", reporting: "mx1.example.jp",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "gmail.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "spamhaus listing", kind: bounceKindFailed, status: "5.7.1",
		diag:      "550 5.7.1 Service unavailable; Client host [203.0.113.5] blocked using zen.spamhaus.org; https://www.spamhaus.org/query/ip/203.0.113.5",
		recipient: "a@customer.example.com", from: "newsletter@example.jp", remoteIP: "198.51.100.25", reporting: "mx1.example.jp",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "spamhaus.org", actionable: true, respons: responsibleSender,
	},
	{
		name: "barracuda listing", kind: bounceKindFailed, status: "5.7.1",
		diag:      "554 5.7.1 Service unavailable; Client host [203.0.113.5] blocked using b.barracudacentral.org; http://www.barracudanetworks.com/reputation/?pr=1&ip=203.0.113.5",
		recipient: "b@other.example.net", from: "newsletter@example.jp", reporting: "mx1.example.jp",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "barracudacentral.org", actionable: true, respons: responsibleSender,
	},
	{
		name: "outlook block list", kind: bounceKindFailed, status: "5.7.1",
		diag:      "550 5.7.1 Unfortunately, messages from [203.0.113.5] weren't sent. Please contact your Internet service provider since part of their network is on our block list (S3140). You can also refer your provider to http://mail.live.com/mail/troubleshooting.aspx#errors.",
		recipient: "c@outlook.com", from: "newsletter@example.jp",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "outlook.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "unknown rbl by url", kind: bounceKindFailed, status: "5.7.1",
		diag:      "550 5.7.1 Your IP 203.0.113.5 is listed; see http://rbl.example-list.net/lookup?ip=203.0.113.5",
		recipient: "d@customer.example.com", from: "newsletter@example.jp",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "example-list.net", actionable: true, respons: responsibleSender,
	},
	{
		name: "rate limited", kind: bounceKindDelayed, status: "4.7.0",
		diag:      "421 4.7.0 Too many messages from 203.0.113.5, try again later",
		recipient: "e@customer.example.com", from: "newsletter@example.jp",
		category: categoryRateLimited, unit: "203.0.113.5", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "rate limited without ip uses reporting mta", kind: bounceKindFailed, status: "4.7.0",
		diag:      "421 4.7.0 Temporarily rate limited due to too many connections",
		recipient: "e@customer.example.com", from: "newsletter@example.jp", reporting: "mx1.example.jp",
		category: categoryRateLimited, unit: "mx1.example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "dmarc failure", kind: bounceKindFailed, status: "5.7.26",
		diag:      "550 5.7.26 Unauthenticated email from example.jp is not accepted due to domain's DMARC policy. Please contact the administrator of example.jp domain if this was a legitimate mail.",
		recipient: "f@gmail.com", from: "Newsletter <newsletter@example.jp>",
		category: categoryAuthFailure, unit: "example.jp", authority: "gmail.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "spf failure by text", kind: bounceKindFailed, status: "5.7.1",
		diag:      "550 5.7.1 SPF check failed for newsletter@example.jp",
		recipient: "f@corp.example.co.jp", from: "newsletter@example.jp",
		category: categoryAuthFailure, unit: "example.jp", authority: "corp.example.co.jp", actionable: true, respons: responsibleSender,
	},
	{
		name: "sender address rejected", kind: bounceKindFailed, status: "5.7.1",
		diag:      "554 5.7.1 <newsletter@example.jp>: Sender address rejected: Access denied",
		recipient: "g@customer.example.com", from: "Newsletter <newsletter@example.jp>",
		category: categorySenderBlocked, unit: "newsletter@example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "sender rejected without original message", kind: bounceKindFailed, status: "5.7.1",
		diag:      "554 5.7.1 <newsletter@example.jp>: Sender address rejected: Access denied",
		recipient: "g@customer.example.com",
		category:  categorySenderBlocked, unit: "newsletter@example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "spam content", kind: bounceKindFailed, status: "5.7.1",
		diag:      "550 5.7.1 Message rejected as spam by Content Filtering.",
		recipient: "h@customer.example.com", from: "newsletter@example.jp",
		category: categoryContentRejected, unit: "newsletter@example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "gmail content spam", kind: bounceKindFailed, status: "5.7.1",
		diag:      "550-5.7.1 [203.0.113.5 12] Our system has detected that this message is 550-5.7.1 likely unsolicited mail. To reduce the amount of spam sent to Gmail, 550-5.7.1 this message has been blocked. Please visit 550 5.7.1 https://support.google.com/mail/?p=UnsolicitedMessageError for more information.",
		recipient: "h@gmail.com", from: "newsletter@example.jp",
		category: categoryContentRejected, unit: "newsletter@example.jp", authority: "gmail.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "too large", kind: bounceKindFailed, status: "5.2.3",
		diag:      "552 5.2.3 Message size exceeds fixed maximum message size (10485760)",
		recipient: "i@customer.example.com", from: "newsletter@example.jp",
		category: categoryMessageTooLarge, unit: "newsletter@example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "relay denied", kind: bounceKindFailed, status: "5.7.1",
		diag:      "554 5.7.1 <j@customer.example.com>: Relay access denied",
		recipient: "j@customer.example.com", from: "newsletter@example.jp", reporting: "mx1.example.jp",
		category: categoryServerConfig, unit: "mx1.example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "protocol error 5.5.x", kind: bounceKindFailed, status: "5.5.1",
		diag:      "503 5.5.1 Error: need MAIL command",
		recipient: "j@customer.example.com", from: "newsletter@example.jp", reporting: "mx1.example.jp",
		category: categoryServerConfig, unit: "mx1.example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "tls required", kind: bounceKindFailed, status: "",
		diag:      "530 Must issue a STARTTLS command first",
		recipient: "j@customer.example.com", from: "newsletter@example.jp", reporting: "mx1.example.jp",
		category: categoryServerConfig, unit: "mx1.example.jp", authority: "customer.example.com", actionable: true, respons: responsibleSender,
	},
	{
		name: "gmail over quota", kind: bounceKindFailed, status: "4.2.2",
		diag:      "452-4.2.2 The email account that you tried to reach is over quota. Please direct 452-4.2.2 the recipient to 452 4.2.2 https://support.google.com/mail/?p=OverQuotaTemp 41be03b00d2f7-7f0c4e1a2b3si7654321a12.34 - gsmtp",
		recipient: "K@gmail.com", from: "newsletter@example.jp",
		category: categoryMailboxFull, unit: "k@gmail.com", authority: "", actionable: false, respons: responsibleRecipient,
	},
	{
		name: "user unknown", kind: bounceKindFailed, status: "5.1.1",
		diag:      "550 5.1.1 <l@customer.example.com>: Recipient address rejected: User unknown in virtual mailbox table",
		recipient: "l@customer.example.com", from: "newsletter@example.jp",
		category: categoryUserUnknown, unit: "l@customer.example.com", authority: "", actionable: false, respons: responsibleRecipient,
	},
	{
		name: "user unknown by text only", kind: bounceKindFailed, status: "",
		diag:      "550 No such user here",
		recipient: "l2@customer.example.com", from: "newsletter@example.jp",
		category: categoryUserUnknown, unit: "l2@customer.example.com", authority: "", actionable: false, respons: responsibleRecipient,
	},
	{
		name: "status wins over blocked wording", kind: bounceKindFailed, status: "5.1.1",
		diag:      "550 5.1.1 Recipient address rejected: blocked",
		recipient: "l3@customer.example.com", from: "newsletter@example.jp",
		category: categoryUserUnknown, unit: "l3@customer.example.com", authority: "", actionable: false, respons: responsibleRecipient,
	},
	{
		name: "mailbox disabled", kind: bounceKindFailed, status: "5.2.1",
		diag:      "550 5.2.1 The email account that you tried to reach is disabled.",
		recipient: "m@gmail.com", from: "newsletter@example.jp",
		category: categoryMailboxDisabled, unit: "m@gmail.com", authority: "", actionable: false, respons: responsibleRecipient,
	},
	{
		name: "domain not found", kind: bounceKindFailed, status: "5.1.2",
		diag:      "550 5.1.2 <n@nowhere.example>: Host or domain name not found. Name service error for name=nowhere.example type=MX: Host not found",
		recipient: "n@nowhere.example", from: "newsletter@example.jp",
		category: categoryDomainNotFound, unit: "nowhere.example", authority: "", actionable: false, respons: responsibleDomain,
	},
	{
		name: "timeout delay", kind: bounceKindDelayed, status: "4.4.1",
		diag:      "connect to mx.slowmail.example.net[203.0.113.7]:25: Connection timed out",
		recipient: "o@slowmail.example.net", from: "newsletter@example.jp", remoteIP: "203.0.113.7",
		category: categoryDeliveryDelay, unit: "slowmail.example.net", authority: "", actionable: false, respons: responsibleDomain,
	},
	{
		name: "greylisted failure kind", kind: bounceKindFailed, status: "4.7.1",
		diag:      "451 4.7.1 Greylisting in action, please come back later",
		recipient: "o2@slowmail.example.net", from: "newsletter@example.jp",
		category: categoryDeliveryDelay, unit: "slowmail.example.net", authority: "", actionable: false, respons: responsibleDomain,
	},
	{
		name: "unknown 550", kind: bounceKindFailed, status: "",
		diag:      "550 Something odd happened here (id A1B2C3D4E5)",
		recipient: "p@customer.example.com", from: "newsletter@example.jp",
		category: categoryUnknownFailure, unit: "customer.example.com", authority: "550 something odd happened here (id <id>)", actionable: true, respons: responsibleUnknown,
	},
	{
		name: "unknown with status", kind: bounceKindFailed, status: "5.0.0",
		diag:      "550 5.0.0 Rejected",
		recipient: "p@customer.example.com", from: "newsletter@example.jp",
		category: categoryUnknownFailure, unit: "customer.example.com", authority: "5.0.0 550 5.0.0 rejected", actionable: true, respons: responsibleUnknown,
	},
	{
		name: "success dsn without diagnostic", kind: bounceKindOther, status: "",
		diag:      "",
		recipient: "q@customer.example.com", from: "newsletter@example.jp",
		category: categoryUnknownFailure, unit: "customer.example.com", authority: "", actionable: true, respons: responsibleUnknown,
	},
}

func (c categoryCase) bounce() *models.Bounce {
	return &models.Bounce{
		Recipient:          c.recipient,
		RecipientDomain:    domainOf(c.recipient),
		StatusCode:         c.status,
		Diagnostic:         c.diag,
		DiagnosticTemplate: DiagnosticTemplate(c.diag),
		RemoteIP:           c.remoteIP,
		ReportingMTA:       c.reporting,
		OriginalFrom:       c.from,
	}
}

func TestCategorize(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range categoryCases {
		t.Run(c.name, func(t *testing.T) {
			got := Categorize(c.bounce(), c.kind)
			if got.Category != c.category || got.UnitValue != c.unit || got.Authority != c.authority ||
				got.Actionable != c.actionable || got.Responsible != c.respons {
				t.Errorf("Categorize = %+v\nwant category=%s unit=%q authority=%q actionable=%v responsible=%s",
					got, c.category, c.unit, c.authority, c.actionable, c.respons)
			}
			if got.Reason == "" {
				t.Error("no rule name recorded")
			}
			seen[got.Category] = true
		})
	}
	for category := range categoryDefs {
		if !seen[category] {
			t.Errorf("no test case produces category %s", category)
		}
	}
	// Every category the rule table can produce is defined.
	for _, r := range categoryRules {
		if _, ok := categoryDefs[r.category]; !ok {
			t.Errorf("rule %s names an undefined category %s", r.name, r.category)
		}
	}
	if c := Categorize(nil, bounceKindFailed); c.Category != categoryUnknownFailure || c.UnitValue != unitUnknown {
		t.Errorf("nil bounce = %+v, want unknown_failure with unit %q", c, unitUnknown)
	}
}

// TestUnitFinalFallback checks that a unit_value is never empty: when the
// category's own candidates are missing, the recipient domain, then the
// remote MTA host, then "unknown" fill it.
func TestUnitFinalFallback(t *testing.T) {
	// content_rejected without sender, reporting MTA or diagnostic address.
	b := &models.Bounce{StatusCode: "5.7.1", Diagnostic: "550 5.7.1 Message rejected due to policy",
		Recipient: "x@broken.example.net", RecipientDomain: "broken.example.net", RemoteMTA: "mx.broken.example.net"}
	if c := Categorize(b, bounceKindFailed); c.Category != categoryContentRejected || c.UnitValue != "broken.example.net" {
		t.Errorf("recipient domain fallback: %+v", c)
	}
	b.Recipient, b.RecipientDomain = "", ""
	if c := Categorize(b, bounceKindFailed); c.UnitValue != "mx.broken.example.net" {
		t.Errorf("remote MTA fallback: %+v", c)
	}
	b.RemoteMTA = ""
	if c := Categorize(b, bounceKindFailed); c.UnitValue != unitUnknown {
		t.Errorf("unknown fallback: %+v", c)
	}
	// Every unit kind ends in the same fallback.
	empty := &models.Bounce{}
	for kind := unitSendingIP; kind <= unitRecipientDomain; kind++ {
		f := newCategoryFacts(empty, bounceKindFailed)
		if got := f.unit(kind); got != unitUnknown {
			t.Errorf("unit kind %d on an empty bounce = %q, want %q", kind, got, unitUnknown)
		}
	}
	g := groupForBounce(bounceKindFailed, empty)
	if g == nil || g.UnitValue != unitUnknown || g.Label() != "unknown_failure: unknown" {
		t.Errorf("empty bounce group = %+v", g)
	}
}

func TestGroupIdentityByUnit(t *testing.T) {
	find := func(name string) categoryCase {
		for _, c := range categoryCases {
			if c.name == name {
				return c
			}
		}
		t.Fatalf("no case %q", name)
		return categoryCase{}
	}
	// Two Gmail over-quota bounces to different recipients: two groups.
	quotaA := find("gmail over quota")
	quotaB := quotaA
	quotaB.recipient = "someone-else@gmail.com"
	ga, gb := groupForBounce(bounceKindFailed, quotaA.bounce()), groupForBounce(bounceKindFailed, quotaB.bounce())
	if ga.GroupKey == gb.GroupKey {
		t.Errorf("over-quota bounces to different recipients share group %s", ga.GroupKey)
	}
	if ga.Actionable || gb.Actionable || ga.Category != categoryMailboxFull {
		t.Errorf("over-quota groups = %+v / %+v", ga, gb)
	}
	// Two Spamhaus listings of the same IP from different recipient domains:
	// one group; a Barracuda listing of the same IP: another group.
	spamA := find("spamhaus listing")
	spamB := spamA
	spamB.recipient = "other@partner.example.org"
	spamB.remoteIP = "198.51.100.77"
	spamB.diag = "550 5.7.1 Service unavailable; Client host [203.0.113.5] blocked using zen.spamhaus.org; https://www.spamhaus.org/query/ip/203.0.113.5 (in reply to RCPT TO command)"
	sa, sb := groupForBounce(bounceKindFailed, spamA.bounce()), groupForBounce(bounceKindFailed, spamB.bounce())
	if sa.GroupKey != sb.GroupKey {
		t.Errorf("spamhaus listings of one IP form two groups:\n%+v\n%+v", sa, sb)
	}
	if !sa.Actionable || sa.UnitValue != "203.0.113.5" || sa.Authority != "spamhaus.org" || sa.Label() != "ip_blocked: 203.0.113.5 @ spamhaus.org" {
		t.Errorf("spamhaus group = %+v", sa)
	}
	if bc := groupForBounce(bounceKindFailed, find("barracuda listing").bounce()); bc.GroupKey == sa.GroupKey {
		t.Error("a different blacklist must give a different group")
	}
	// Auto-replies and non-bounces have no group.
	if groupForBounce(bounceKindAutoReply, spamA.bounce()) != nil || groupForBounce("", spamA.bounce()) != nil {
		t.Error("auto-reply / non-bounce got a group")
	}
}

func TestExtractors(t *testing.T) {
	ipCases := []struct{ text, remote, want string }{
		{"421-4.7.0 [203.0.113.5      19] our system has detected", "", "203.0.113.5"},
		{"client host [203.0.113.5] blocked using zen.spamhaus.org", "198.51.100.25", "203.0.113.5"},
		{"too many messages from 203.0.113.5, try again later", "", "203.0.113.5"},
		{"your ip address 2001:db8::25 is listed", "", "2001:db8::25"},
		{"connect to mx.example[203.0.113.7]:25: connection timed out at 09:15:30", "203.0.113.7", ""},
		{"550 5.7.1 rejected on 2025-09-02 09:15:30", "", ""},
		{"", "", ""},
	}
	for _, c := range ipCases {
		if got := extractSendingIP(c.text, c.remote); got != c.want {
			t.Errorf("extractSendingIP(%q) = %q, want %q", c.text, got, c.want)
		}
	}
	domainCases := map[string]string{
		"www.spamhaus.org": "spamhaus.org", "mx.corp.example.co.jp": "example.co.jp", "example.jp": "example.jp",
		"localhost": "localhost", "a.b.c.example.com": "example.com", "": "",
	}
	for in, want := range domainCases {
		if got := registrableDomain(in); got != want {
			t.Errorf("registrableDomain(%q) = %q, want %q", in, got, want)
		}
	}
	if got := addressIn("Newsletter <Newsletter@Example.JP>"); got != "newsletter@example.jp" {
		t.Errorf("addressIn = %q", got)
	}
	if got := listingAuthority("blocked; see https://support.google.com/mail/answer/81126", "gmail.com"); got != "" {
		t.Errorf("help page taken as authority: %q", got)
	}
	if got := listingAuthority("is listed at http://rbl.example-list.net/lookup", "customer.example.com", "example.jp"); got != "example-list.net" {
		t.Errorf("listing authority = %q", got)
	}
}

// TestGroupIncremental checks the fetch / group split without IMAP: stored
// messages stay unclassified until GroupMailbox runs, an incremental run
// only processes the new rows and reports only the actionable groups that
// grew.
func TestGroupIncremental(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	var first, later []sample
	for _, s := range samples {
		switch s.file {
		case "spamhaus_block_b.eml", "gmail_overquota.eml":
			later = append(later, s)
		default:
			first = append(first, s)
		}
	}
	storeSamples(t, root, address, 1, first...)

	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := models.CountUnclassifiedMessages(db); n != len(first) {
		t.Errorf("%d unclassified messages after storing, want %d", n, len(first))
	}
	if _, bounces, _ := models.CountMessages(db); bounces != 0 {
		t.Errorf("fetch phase marked %d bounces", bounces)
	}
	if countRows(t, db, "groups") != 0 || countBounceDetails(t, db) != 0 {
		t.Error("fetch phase created groups or bounce details")
	}
	db.Close()

	var lines []string
	res, err := GroupMailbox(context.Background(), root, address, false, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("group run 1: %v", err)
	}
	wantBounces := 0
	for _, s := range first {
		if s.isBounce {
			wantBounces++
		}
	}
	if res.Processed != len(first) || res.Bounces != wantBounces || res.Groups == 0 {
		t.Errorf("group run 1 = %+v, want %d processed / %d bounces", res, len(first), wantBounces)
	}
	spamKey := GroupKey(categoryIPBlocked, "203.0.113.5", "spamhaus.org")
	contentKey := GroupKey(categoryContentRejected, "broken.example.net", "broken.example.net")
	if len(res.GroupsTouched) != 2 || res.GroupsTouched[0] != spamKey || res.GroupsTouched[1] != contentKey {
		t.Errorf("group run 1 touched %v, want the actionable groups [%s %s] only", res.GroupsTouched, spamKey, contentKey)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "grouping ") {
		t.Errorf("no progress reported: %v", lines)
	}
	groupsAfter1 := res.Groups

	// Nothing new: nothing processed, nothing touched.
	res, err = GroupMailbox(context.Background(), root, address, false, nil)
	if err != nil {
		t.Fatalf("group run 2: %v", err)
	}
	if res.Processed != 0 || res.Bounces != 0 || len(res.GroupsTouched) != 0 || res.Groups != groupsAfter1 {
		t.Errorf("group run 2 = %+v, want nothing processed", res)
	}

	// Two more messages: the second Spamhaus listing joins the existing
	// actionable group (touched), the over-quota bounce creates a
	// recipient-side group (not touched).
	storeSamples(t, root, address, uint32(len(first)+1), later...)
	res, err = GroupMailbox(context.Background(), root, address, false, nil)
	if err != nil {
		t.Fatalf("group run 3: %v", err)
	}
	if res.Processed != 2 || res.Bounces != 2 || res.Groups != groupsAfter1+1 {
		t.Errorf("group run 3 = %+v, want 2 processed / %d groups", res, groupsAfter1+1)
	}
	if len(res.GroupsTouched) != 1 || res.GroupsTouched[0] != spamKey {
		t.Errorf("group run 3 touched %v, want [%s]", res.GroupsTouched, spamKey)
	}
	db, err = models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g, err := models.GetGroup(db, spamKey)
	if err != nil || g.MessageCount != 2 || g.RecipientCount != 2 || !g.NeedsAnalysis || !g.Actionable {
		t.Errorf("spamhaus group = %+v (err %v), want 2 messages / 2 recipients / needs analysis", g, err)
	}
	quota, err := models.GetGroup(db, GroupKey(categoryMailboxFull, "kyuro@gmail.com", ""))
	if err != nil || quota.Actionable || quota.MessageCount != 1 || quota.Responsible != responsibleRecipient {
		t.Errorf("over-quota group = %+v (err %v)", quota, err)
	}
	if n, _ := models.CountUnclassifiedMessages(db); n != 0 {
		t.Errorf("%d messages left unclassified", n)
	}
	only := true
	actionable, err := models.CountGroups(db, &only)
	if err != nil {
		t.Fatal(err)
	}
	if actionable.Open != 2 {
		t.Errorf("actionable open groups = %d, want 2 (spamhaus, content)", actionable.Open)
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><head><title>x</title><style>p { color: red; }</style></head><body>
<!-- comment with 550 5.0.0 -->
<script>var a = "550 5.0.0";</script>
<p>Your message to <b>goro@gmail.com</b> couldn&#39;t be delivered.</p>
<div>Remote&nbsp;Server returned &#39;550 5.1.1 not found&#39;</div><br>
<table><tr><td>a</td><td>b &amp; c &lt;d&gt;</td></tr></table>
<p>   </p>
<p>last</p></body></html>`
	got := htmlToText(in)
	want := "Your message to goro@gmail.com couldn't be delivered.\n\nRemote Server returned '550 5.1.1 not found'\n\na b & c <d>\n\nlast"
	if got != want {
		t.Errorf("htmlToText =\n%q\nwant\n%q", got, want)
	}
	for _, blank := range []string{"", "   ", "<html><body></body></html>", "<html><head><style>p{}</style></head><body>\n\n</body></html>", "<p>&nbsp;</p>"} {
		if got := htmlToText(blank); got != "" {
			t.Errorf("htmlToText(%q) = %q, want empty", blank, got)
		}
	}
}

// TestBodySelectionLayouts covers the five body layouts of design 5.2 with
// inline messages: plain only, HTML only, plain + HTML, blank plain + HTML,
// plain + blank HTML.
func TestBodySelectionLayouts(t *testing.T) {
	const b = "=_layout"
	part := func(ct, body string) string {
		return "--" + b + "\r\nContent-Type: " + ct + "\r\n\r\n" + body + "\r\n"
	}
	multi := func(parts ...string) []byte {
		return []byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: layout\r\nMIME-Version: 1.0\r\n" +
			"Content-Type: multipart/alternative; boundary=\"" + b + "\"\r\n\r\n" + strings.Join(parts, "") + "--" + b + "--\r\n")
	}
	cases := []struct {
		name                 string
		raw                  []byte
		textCount, htmlCount int
		source, effectiveHas string
	}{
		{"plain only", []byte("From: a@example.com\r\nSubject: p\r\n\r\nplain body 550 5.1.1\r\n"), 1, 0, bodySourceText, "plain body"},
		{"html only", []byte("From: a@example.com\r\nSubject: h\r\nContent-Type: text/html\r\n\r\n<p>html <b>body</b></p>\r\n"), 0, 1, bodySourceHTML, "html body"},
		{"plain and html", multi(part("text/plain", "plain wins"), part("text/html", "<p>html loses</p>")), 1, 1, bodySourceText, "plain wins"},
		{"blank plain and html", multi(part("text/plain", "  \r\n\t\r\n"), part("text/html", "<div>html used</div>")), 0, 1, bodySourceHTML, "html used"},
		{"plain and blank html", multi(part("text/plain", "text used"), part("text/html", "<html><body> &nbsp; </body></html>")), 1, 0, bodySourceText, "text used"},
		{"nothing", multi(part("text/plain", "\r\n"), part("text/html", "<p></p>")), 0, 0, "", ""},
	}
	root := t.TempDir()
	address := "layout@example.jp"
	dir := MailboxDir(root, address)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pm := ParseMessage(c.raw)
			if pm.TextCount() != c.textCount || pm.HTMLCount() != c.htmlCount || pm.BodySource != c.source {
				t.Errorf("text_count=%d html_count=%d body_source=%q, want %d / %d / %q", pm.TextCount(), pm.HTMLCount(), pm.BodySource, c.textCount, c.htmlCount, c.source)
			}
			if body := pm.bodyForClassification(); !strings.Contains(body, c.effectiveHas) || (c.effectiveHas == "" && body != "") {
				t.Errorf("effective body %q does not contain %q", body, c.effectiveHas)
			}
			// The sections survive the file round trip: one file per section
			// with content, none for a blank part.
			key := MessageKey(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "INBOX", 1, uint32(i+1))
			if err := writeBodySections(dir, key, pm); err != nil {
				t.Fatal(err)
			}
			for ext, sections := range map[string][]string{"txt": pm.TextSections, "html": pm.HTMLSections} {
				back, err := ReadBodySections(root, address, key, ext, len(sections))
				if err != nil || strings.Join(back, "\x00") != strings.Join(sections, "\x00") {
					t.Errorf("%s sections after the round trip = %q (err %v), want %q", ext, back, err, sections)
				}
				if _, err := os.Stat(SectionFilePath(dir, key, ext, len(sections)+1)); !os.IsNotExist(err) {
					t.Errorf("%s: a file beyond the %d sections exists (err %v)", ext, len(sections), err)
				}
			}
		})
	}
}

// TestWriteBodySections checks that every section becomes its own numbered
// file (-1, -2, ...), that a message without sections writes nothing, and
// that a key must be valid before any file is touched.
func TestWriteBodySections(t *testing.T) {
	dir := t.TempDir()
	key := MessageKey(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "INBOX", 1, 1)
	exists := func(ext string, n int) bool {
		_, err := os.Stat(SectionFilePath(dir, key, ext, n))
		return err == nil
	}
	two := ParseMessage(readSample(t, "original_first_bounce.eml"))
	if two.TextCount() != 2 {
		t.Fatalf("original_first_bounce has %d text sections, want 2", two.TextCount())
	}
	if err := writeBodySections(dir, key, two); err != nil {
		t.Fatal(err)
	}
	if !exists("txt", 1) || !exists("txt", 2) || exists("txt", 3) || exists("html", 1) {
		t.Fatalf("files after two sections: txt1=%v txt2=%v txt3=%v html1=%v", exists("txt", 1), exists("txt", 2), exists("txt", 3), exists("html", 1))
	}
	for i, want := range two.TextSections {
		if data, err := os.ReadFile(SectionFilePath(dir, key, "txt", i+1)); err != nil || string(data) != want {
			t.Errorf("section %d = %q (err %v), want %q", i+1, data, err, want)
		}
	}
	one := ParseMessage(readSample(t, "normal.eml"))
	if one.TextCount() != 1 || one.HTMLCount() != 1 {
		t.Fatalf("normal.eml has %d text / %d html sections, want 1 / 1", one.TextCount(), one.HTMLCount())
	}
	other := MessageKey(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "INBOX", 1, 2)
	if err := writeBodySections(dir, other, one); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"txt", "html"} {
		if _, err := os.Stat(SectionFilePath(dir, other, ext, 1)); err != nil {
			t.Errorf("%s section of the second message missing: %v", ext, err)
		}
	}
	empty := MessageKey(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), "INBOX", 1, 3)
	if err := writeBodySections(dir, empty, &ParsedMessage{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SectionFilePath(dir, empty, "txt", 1)); !os.IsNotExist(err) {
		t.Error("a message without sections must write no file")
	}
	if err := writeBodySections(dir, "../escape", one); err != ErrInvalidMessageKey {
		t.Errorf("invalid key err = %v, want ErrInvalidMessageKey", err)
	}
	files := 0
	if err := walkMailboxFiles(dir, func(string, os.DirEntry) { files++ }); err != nil {
		t.Fatal(err)
	}
	if files != 4 {
		t.Errorf("directory holds %d files, want 4 (two sections, one text, one html)", files)
	}
}

// TestCharsetDecodingFiles proves the file outputs for non-UTF-8 input: the
// .txt / .html files hold the decoded UTF-8 text, the index row holds the
// decoded headers and the .eml is byte-identical to the input.
func TestCharsetDecodingFiles(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cases := []struct {
		file, subject, from, fromName, to, toName string
		txtHas, htmlHas                           string
		hasText, hasHTML                          bool
		isBounce                                  bool
		recipient                                 string
	}{
		{
			file: "iso2022jp_bounce.eml", subject: "配信不能: 9月のお知らせ", from: "MAILER-DAEMON@mx1.example.jp",
			fromName: "メール配信システム", to: "newsletter@example.jp", toName: "配信担当",
			txtHas: "以下の宛先にメッセージを配信できませんでした。", hasText: true, isBounce: true,
			recipient: "taro@customer.example.com",
		},
		{
			file: "sjis_html_bounce.eml", subject: "配信できませんでした", from: "postmaster@mx2.example.jp",
			fromName: "メール配信システム", to: "newsletter@example.jp",
			htmlHas: "次の宛先へ配信できませんでした: <b>hanako@customer.example.com</b>", hasHTML: true, isBounce: true,
			recipient: "hanako@customer.example.com",
		},
		{
			file: "base64_utf8.eml", subject: "Re: 9月のキャンペーン", from: "taro@client.example.com", fromName: "山田 太郎",
			to: "newsletter@example.jp", txtHas: "価格表を送ってください。", hasText: true,
		},
	}
	for i, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw := readSample(t, c.file)
			msg, pm, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 1, UID: uint32(i + 1)}, storeOptions{writeEML: true})
			if err != nil {
				t.Fatal(err)
			}
			if pm.ParseError != "" {
				t.Errorf("parse error: %s", pm.ParseError)
			}
			if pm.Subject != c.subject || pm.FromAddress != c.from || pm.FromName != c.fromName || pm.To != c.to || pm.ToName != c.toName {
				t.Errorf("headers = subject %q from %q (%q) to %q (%q)", pm.Subject, pm.FromAddress, pm.FromName, pm.To, pm.ToName)
			}
			if (msg.TextCount > 0) != c.hasText || (msg.HTMLCount > 0) != c.hasHTML {
				t.Errorf("text_count=%d html_count=%d, want text %v / html %v", msg.TextCount, msg.HTMLCount, c.hasText, c.hasHTML)
			}
			eml, err := ReadMessageFile(root, address, msg.MessageKey, "eml")
			if err != nil || !bytes.Equal(eml, raw) {
				t.Errorf(".eml differs from the input (err %v, %d vs %d bytes)", err, len(eml), len(raw))
			}
			if c.txtHas != "" {
				txt, err := readFirstSection(root, address, msg.MessageKey, "txt")
				if err != nil || !utf8.Valid(txt) || !strings.Contains(string(txt), c.txtHas) {
					t.Errorf(".txt (err %v) = %q, want UTF-8 containing %q", err, txt, c.txtHas)
				}
			}
			if c.htmlHas != "" {
				h, err := readFirstSection(root, address, msg.MessageKey, "html")
				if err != nil || !utf8.Valid(h) || !strings.Contains(string(h), c.htmlHas) {
					t.Errorf(".html (err %v) = %q, want UTF-8 containing %q", err, h, c.htmlHas)
				}
			}
			row, err := models.GetMessageByKey(db, msg.MessageKey)
			if err != nil {
				t.Fatal(err)
			}
			if row.Subject != c.subject || row.FromName != c.fromName || row.ToName != c.toName || row.ToAddress != c.to {
				t.Errorf("index row headers = subject %q from_name %q to %q (%q)", row.Subject, row.FromName, row.ToAddress, row.ToName)
			}
			cls := Classify(pm)
			if cls.IsBounce != c.isBounce {
				t.Errorf("classified as %+v, want bounce=%v", cls, c.isBounce)
			}
			if c.isBounce {
				if b := ExtractBounce(pm, cls.Kind, address); b.Recipient != c.recipient || b.StatusCode != "5.1.1" {
					t.Errorf("extracted recipient %q status %q from the decoded body", b.Recipient, b.StatusCode)
				}
			}
		})
	}

	// An unknown charset must not panic: the bytes are decoded by detection
	// (Shift_JIS here) and text_count follows the (non-blank) content.
	sjis := []byte{0x82, 0xb1, 0x82, 0xea, 0x82, 0xcd, 0x83, 0x65, 0x83, 0x58, 0x83, 0x67} // "kore wa tesuto" (this is a test) in Shift_JIS
	unknown := append([]byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: unknown charset\r\nMessage-ID: <unk@example.com>\r\n"+
		"Content-Type: text/plain; charset=\"x-unknown-999\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n"), sjis...)
	unknown = append(unknown, "\r\n"...)
	msg, pm, err := storeMessage(db, dir, unknown, Source{Folder: "INBOX", UIDValidity: 1, UID: 99}, storeOptions{writeEML: true})
	if err != nil {
		t.Fatal(err)
	}
	if msg.TextCount != 1 || msg.BodySource != bodySourceText {
		t.Errorf("unknown charset: text_count=%d body_source=%q", msg.TextCount, msg.BodySource)
	}
	if txt, err := readFirstSection(root, address, msg.MessageKey, "txt"); err != nil || strings.TrimSpace(string(txt)) != "これはテスト" {
		t.Errorf("unknown charset: .txt (err %v) = %q, want the detected Shift_JIS text", err, txt)
	}
	if pm.Subject != "unknown charset" {
		t.Errorf("unknown charset: subject = %q", pm.Subject)
	}
	// Bytes no Japanese encoding accepts are kept raw (nothing is lost).
	garbage := []byte{0x81, '\n', 0x81, '\n', 'x', '\n'}
	rawMsg := append([]byte("From: a@example.com\r\nSubject: garbage\r\nMessage-ID: <garbage@example.com>\r\nContent-Type: text/plain; charset=\"x-unknown-999\"\r\n\r\n"), garbage...)
	msg, _, err = storeMessage(db, dir, rawMsg, Source{Folder: "INBOX", UIDValidity: 1, UID: 100}, storeOptions{writeEML: true})
	if err != nil {
		t.Fatal(err)
	}
	if txt, err := readFirstSection(root, address, msg.MessageKey, "txt"); err != nil || !bytes.Equal(txt, garbage) || msg.TextCount != 1 {
		t.Errorf("garbage body: .txt (err %v) = % x, want the raw bytes % x (text_count=%d)", err, txt, garbage, msg.TextCount)
	}
	blank := []byte("From: a@example.com\r\nSubject: blank unknown\r\nContent-Type: text/plain; charset=\"x-unknown-999\"\r\n\r\n   \r\n\r\n")
	if pm := ParseMessage(blank); pm.TextCount() != 0 || pm.BodySource != "" {
		t.Errorf("blank body with unknown charset: text_count=%d body_source=%q", pm.TextCount(), pm.BodySource)
	}
}

// encodeText converts UTF-8 text to the given encoding for test messages.
func encodeText(t *testing.T, enc encoding.Encoding, text string) []byte {
	t.Helper()
	out, err := enc.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCharsetAliasesAndDetection covers charset aliases, missing and
// mislabeled charsets, BOMs and raw 8-bit headers for bodies and subjects.
func TestCharsetAliasesAndDetection(t *testing.T) {
	const jp = "配信できませんでした: 宛先が見つかりません"
	const jpSubject = "配信不能通知"
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	msg := func(subject, contentType, cte string, body []byte) []byte {
		head := "From: a@example.com\r\nTo: b@example.com\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\n" +
			"Content-Type: " + contentType + "\r\n"
		if cte != "" {
			head += "Content-Transfer-Encoding: " + cte + "\r\n"
		}
		return append([]byte(head+"\r\n"), body...)
	}
	utf16 := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM)
	cases := []struct {
		name        string
		raw         []byte
		wantSubject string
		wantBody    string // contained in the effective body
		html        bool
	}{
		{"iso-2022-jp alias", msg("=?csISO2022JP?B?"+b64(encodeText(t, japanese.ISO2022JP, jpSubject))+"?=",
			`text/plain; charset="iso-2022-jp-ms"`, "7bit", encodeText(t, japanese.ISO2022JP, jp)), jpSubject, jp, false},
		{"shift_jis cp932 label", msg("=?cp932?B?"+b64(encodeText(t, japanese.ShiftJIS, jpSubject))+"?=",
			`text/plain; charset=cp932`, "8bit", encodeText(t, japanese.ShiftJIS, jp)), jpSubject, jp, false},
		{"shift_jis windows-31j label", msg("=?Windows-31J?B?"+b64(encodeText(t, japanese.ShiftJIS, jpSubject))+"?=",
			`text/plain; charset="Windows-31J"`, "8bit", encodeText(t, japanese.ShiftJIS, jp)), jpSubject, jp, false},
		{"shift_jis no label, raw 8-bit subject", msg(string(encodeText(t, japanese.ShiftJIS, jpSubject)),
			`text/plain`, "8bit", encodeText(t, japanese.ShiftJIS, jp)), jpSubject, jp, false},
		{"euc-jp alias", msg("=?eucjp?B?"+b64(encodeText(t, japanese.EUCJP, jpSubject))+"?=",
			`text/plain; charset="x-euc-jp"`, "8bit", encodeText(t, japanese.EUCJP, jp)), jpSubject, jp, false},
		{"euc-jp no label", msg("=?x-euc-jp?B?"+b64(encodeText(t, japanese.EUCJP, jpSubject))+"?=",
			`text/plain`, "8bit", encodeText(t, japanese.EUCJP, jp)), jpSubject, jp, false},
		{"iso-2022-jp no label", msg("=?ISO-2022-JP?B?"+b64(encodeText(t, japanese.ISO2022JP, jpSubject))+"?=",
			`text/plain`, "7bit", encodeText(t, japanese.ISO2022JP, jp)), jpSubject, jp, false},
		{"windows-1252 alias", msg("=?cp1252?Q?caf=E9_=80?=", `text/plain; charset=cp1252`, "8bit",
			encodeText(t, charmap.Windows1252, "café €")), "café €", "café €", false},
		{"latin1 alias", msg("=?latin1?Q?na=EFve?=", `text/plain; charset=latin1`, "8bit",
			encodeText(t, charmap.ISO8859_1, "naïve")), "naïve", "naïve", false},
		{"utf-8 with BOM, no label", msg("=?utf8?B?"+b64([]byte(jpSubject))+"?=", `text/plain`, "8bit",
			append([]byte{0xef, 0xbb, 0xbf}, jp...)), jpSubject, jp, false},
		{"utf-16 with BOM, labeled", msg("=?UTF-16?B?"+b64(encodeText(t, utf16, jpSubject))+"?=", `text/plain; charset="utf-16"`, "base64",
			[]byte(b64(encodeText(t, utf16, jp)))), jpSubject, jp, false},
		{"utf-16 with BOM, no label", msg("plain", `text/plain`, "base64",
			[]byte(b64(encodeText(t, utf16, jp)))), "plain", jp, false},
		{"mislabeled us-ascii shift_jis", msg("=?us-ascii?B?"+b64(encodeText(t, japanese.ShiftJIS, jpSubject))+"?=",
			`text/plain; charset=us-ascii`, "8bit", encodeText(t, japanese.ShiftJIS, jp)), jpSubject, jp, false},
		{"html meta charset without MIME charset", msg("html", `text/html`, "8bit",
			encodeText(t, japanese.ShiftJIS, `<html><head><meta http-equiv="Content-Type" content="text/html; charset=Shift_JIS"></head><body><p>`+jp+`</p></body></html>`)),
			"html", jp, true},
		{"html meta charset tag", msg("html", `text/html`, "8bit",
			encodeText(t, japanese.EUCJP, `<html><head><meta charset="euc-jp"></head><body><p>`+jp+`</p></body></html>`)),
			"html", jp, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pm := ParseMessage(c.raw)
			if pm.Subject != c.wantSubject {
				t.Errorf("subject = %q, want %q", pm.Subject, c.wantSubject)
			}
			body := pm.bodyForClassification()
			if !strings.Contains(body, c.wantBody) {
				t.Errorf("effective body = %q, want it to contain %q", body, c.wantBody)
			}
			if c.html && (pm.HTMLCount() != 1 || pm.BodySource != bodySourceHTML) {
				t.Errorf("html case: html_count=%d body_source=%q", pm.HTMLCount(), pm.BodySource)
			}
			if !c.html && (pm.TextCount() != 1 || !strings.Contains(pm.TextSections[0], c.wantBody)) {
				t.Errorf("text case: text_count=%d sections=%q", pm.TextCount(), pm.TextSections)
			}
			for _, sec := range append(append([]string{}, pm.TextSections...), pm.HTMLSections...) {
				if !utf8.ValidString(sec) {
					t.Errorf("decoded section is not clean UTF-8: %q", sec)
				}
			}
			if strings.ContainsRune(body, utf8.RuneError) {
				t.Errorf("effective body carries replacement runes: %q", body)
			}
		})
	}

	// Display names in From / To follow the same rules (alias and raw 8-bit).
	raw := append([]byte("From: =?cp932?B?"+b64(encodeText(t, japanese.ShiftJIS, "山田 太郎"))+"?= <a@example.com>\r\nTo: "),
		encodeText(t, japanese.ShiftJIS, "配信担当")...)
	raw = append(raw, " <b@example.com>\r\nSubject: x\r\n\r\nbody\r\n"...)
	pm := ParseMessage(raw)
	if pm.FromName != "山田 太郎" || pm.FromAddress != "a@example.com" || pm.ToName != "配信担当" || pm.To != "b@example.com" {
		t.Errorf("names: from %q (%q) to %q (%q)", pm.FromAddress, pm.FromName, pm.To, pm.ToName)
	}

	// The decoder helpers themselves.
	if got := decodeBytes("SHIFT-JIS", encodeText(t, japanese.ShiftJIS, jp)); string(got) != jp {
		t.Errorf("decodeBytes(shift-jis) = %q", got)
	}
	if got, ok := detectDecode(encodeText(t, japanese.EUCJP, "日本語のテキストです。半角ｶﾅも少し。")); !ok || string(got) != "日本語のテキストです。半角ｶﾅも少し。" {
		t.Errorf("detectDecode(euc-jp) = %q (%v)", got, ok)
	}
	if got, ok := detectDecode(encodeText(t, japanese.ShiftJIS, "日本語のテキストです。半角ｶﾅも少し。")); !ok || string(got) != "日本語のテキストです。半角ｶﾅも少し。" {
		t.Errorf("detectDecode(shift_jis) = %q (%v)", got, ok)
	}
	if _, ok := detectDecode([]byte{0x81, '\n', 0x81, '\n'}); ok {
		t.Error("garbage bytes were accepted as a Japanese encoding")
	}
	if got := decodeHeaderText(string(encodeText(t, japanese.ISO2022JP, jpSubject))); got != jpSubject {
		t.Errorf("decodeHeaderText(iso-2022-jp) = %q", got)
	}
}

// TestBothBodiesClassifyAndExtract covers the two-body rule of design 5.2:
// attachments never become the body, skeleton HTML counts as no HTML, and a
// bounce wording found only in the HTML body makes the message a bounce
// (body_source = html) with the details extracted from the HTML text.
func TestBothBodiesClassifyAndExtract(t *testing.T) {
	// multipart/mixed with an inline text body, a text/plain attachment and a PDF.
	pm := ParseMessage(readSample(t, "attach_mixed.eml"))
	if pm.TextCount() != 1 || pm.HTMLCount() != 0 || pm.BodySource != bodySourceText {
		t.Errorf("attach_mixed: text_count=%d html_count=%d body_source=%q", pm.TextCount(), pm.HTMLCount(), pm.BodySource)
	}
	if body := pm.bodyForClassification(); !strings.Contains(body, "Please find the monthly report attached.") || strings.Contains(body, "status=bounced") {
		t.Errorf("attach_mixed: text body = %q (attachment must not leak in)", body)
	}
	if c := Classify(pm); c.IsBounce || c.BodySource != bodySourceText {
		t.Errorf("attach_mixed classified as %+v", c)
	}

	// Skeleton HTML next to a text body: no HTML body at all.
	pm = ParseMessage(readSample(t, "html_skeleton.eml"))
	if pm.TextCount() != 1 || pm.HTMLCount() != 0 || pm.BodySource != bodySourceText {
		t.Errorf("html_skeleton: text_count=%d html_count=%d sections=%q body_source=%q", pm.TextCount(), pm.HTMLCount(), pm.HTMLSections, pm.BodySource)
	}
	if c := Classify(pm); c.IsBounce {
		t.Errorf("html_skeleton classified as %+v", c)
	}

	// Plain text without bounce wording, HTML with the NDR wording.
	pm = ParseMessage(readSample(t, "html_only_bounce_wording.eml"))
	if pm.TextCount() != 1 || pm.HTMLCount() != 1 || pm.BodySource != bodySourceText {
		t.Errorf("html_only_bounce_wording after parse: text_count=%d html_count=%d body_source=%q", pm.TextCount(), pm.HTMLCount(), pm.BodySource)
	}
	c := Classify(pm)
	if !c.IsBounce || c.Kind != bounceKindFailed || c.Rule != "body_pattern" || c.BodySource != bodySourceHTML {
		t.Fatalf("html_only_bounce_wording classified as %+v, want failed / body_pattern / html", c)
	}
	b := ExtractBounce(pm, c.Kind, "newsletter@example.jp")
	if b.Recipient != "sachi@customer.example.com" || b.StatusCode != "5.1.1" || b.SMTPCode != "550" ||
		!strings.Contains(b.Diagnostic, "RESOLVER.ADR.RecipNotFound") {
		t.Errorf("html_only_bounce_wording extracted %+v", b)
	}
	// A text body that matches keeps body_source = text even with an HTML body.
	pm = ParseMessage(readSample(t, "gmail_bounce.eml"))
	if c := Classify(pm); !c.IsBounce || c.BodySource != bodySourceText {
		t.Errorf("gmail_bounce classified as %+v, want body_source text", c)
	}
	// Extraction completes empty fields from the HTML text.
	pm = &ParsedMessage{FromAddress: "MAILER-DAEMON@x.example", Subject: "Undelivered Mail Returned to Sender",
		TextSections: []string{"Your message could not be delivered."},
		HTMLSections: []string{"<p>&lt;kei@customer.example.com&gt;: host mx.customer.example.com[198.51.100.25] said: 550 5.1.1 User unknown</p>"},
		Headers:      map[string]string{}}
	pm.finishBodies()
	b = ExtractBounce(pm, bounceKindFailed, "newsletter@example.jp")
	if b.Recipient != "kei@customer.example.com" || b.StatusCode != "5.1.1" || b.RemoteMTA != "mx.customer.example.com" || b.RemoteIP != "198.51.100.25" {
		t.Errorf("fields not completed from the HTML text: %+v", b)
	}

	// Through the phases: the row records the body actually used and the
	// section files match the counts.
	root := t.TempDir()
	address := "newsletter@example.jp"
	storeSamples(t, root, address, 1,
		sample{file: "html_only_bounce_wording.eml"}, sample{file: "attach_mixed.eml"}, sample{file: "html_skeleton.eml"}, sample{file: "gmail_bounce.eml"})
	if _, err := GroupMailbox(context.Background(), root, address, false, nil); err != nil {
		t.Fatal(err)
	}
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := map[string]struct {
		bounce bool
		source string
	}{
		"Notification #4521":                     {true, bodySourceHTML},
		"Monthly report attached":                {false, bodySourceText},
		"Thanks for your order":                  {false, bodySourceText},
		"Delivery Status Notification (Failure)": {true, bodySourceText},
	}
	msgs := mustListAllMessages(t, db)
	for _, m := range msgs {
		w, ok := want[m.Subject]
		if !ok {
			t.Errorf("unexpected message %q", m.Subject)
			continue
		}
		if m.IsBounce != w.bounce || m.BodySource != w.source {
			t.Errorf("%q: is_bounce=%v body_source=%q, want %v / %q", m.Subject, m.IsBounce, m.BodySource, w.bounce, w.source)
		}
	}
	assertSectionFiles(t, root, address, msgs, "both bodies")
}

// TestMultipleSections covers messages with several inline text/plain or
// text/html parts (design 5.2 / 3): every part with content becomes one
// section in MIME order, whichever comes first, written as <key>.<ext>,
// <key>-1.<ext>, ... and counted in text_count / html_count; the joined
// body keeps the order, so the bounce wording is never lost, and the
// addresses of the original message's text are not taken as the recipient.
func TestMultipleSections(t *testing.T) {
	cases := []struct {
		file          string
		kind, rule    string
		recipient     string
		status, smtp  string
		remoteMTA     string
		textSections  []string // one substring per expected text section, in order
		htmlSections  []string // same for the HTML sections
		notRecipients []string
	}{
		{
			file: "original_first_bounce.eml", kind: bounceKindFailed, rule: "daemon_sender",
			recipient: "taro@customer.example.com", status: "5.1.1", smtp: "550", remoteMTA: "mx.customer.example.com",
			textSections:  []string{"Here is the September newsletter", "This is the mail system at host mx1.example.jp."},
			notRecipients: []string{"support@example.jp", "partner-desk@partner.example.org", "newsletter@example.jp"},
		},
		{
			file: "bounce_then_original_part.eml", kind: bounceKindFailed, rule: "daemon_sender",
			recipient: "jiro@nowhere.example.org", status: "5.1.1", smtp: "550", remoteMTA: "mx.nowhere.example.org",
			textSections:  []string{"The following address(es) failed:", "Reply to digest-owner@example.jp"},
			notRecipients: []string{"archive@partner.example.org", "digest-owner@example.jp"},
		},
		{
			file: "two_html_parts.eml", kind: bounceKindFailed, rule: "daemon_sender",
			recipient: "kuro.suzuki@corp.example.co.jp", status: "5.1.1", smtp: "550",
			htmlSections:  []string{"Dear customer", "RESOLVER.ADR.RecipNotFound"},
			notRecipients: []string{"support@example.jp"},
		},
	}
	root := t.TempDir()
	address := "newsletter@example.jp"
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw := readSample(t, c.file)
			msg, pm, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 1, UID: uint32(i + 1)}, storeOptions{writeEML: true})
			if err != nil {
				t.Fatal(err)
			}
			if pm.ParseError != "" {
				t.Errorf("parse error: %s", pm.ParseError)
			}
			if msg.TextCount != len(c.textSections) || msg.HTMLCount != len(c.htmlSections) ||
				pm.TextCount() != msg.TextCount || pm.HTMLCount() != msg.HTMLCount {
				t.Errorf("text_count=%d html_count=%d (parsed %d / %d), want %d / %d", msg.TextCount, msg.HTMLCount,
					pm.TextCount(), pm.HTMLCount(), len(c.textSections), len(c.htmlSections))
			}
			cls := Classify(pm)
			if !cls.IsBounce || cls.Kind != c.kind || cls.Rule != c.rule {
				t.Fatalf("classified as %+v, want %s / %s", cls, c.kind, c.rule)
			}
			b := ExtractBounce(pm, cls.Kind, address)
			if b.Recipient != c.recipient || b.StatusCode != c.status || b.SMTPCode != c.smtp || (c.remoteMTA != "" && b.RemoteMTA != c.remoteMTA) {
				t.Errorf("extracted %+v, want recipient %s status %s smtp %s mta %s", b, c.recipient, c.status, c.smtp, c.remoteMTA)
			}
			for _, bad := range c.notRecipients {
				if b.Recipient == bad {
					t.Errorf("address of the original message taken as recipient: %s", bad)
				}
			}
			// One file per section, numbered in MIME order, none beyond the count.
			checkFiles := func(ext string, want []string) {
				for i, w := range want {
					n := i + 1
					data, err := os.ReadFile(SectionFilePath(dir, msg.MessageKey, ext, n))
					if err != nil {
						t.Errorf("%s: %v", sectionFileName(msg.MessageKey, ext, n), err)
						continue
					}
					if !strings.Contains(string(data), w) {
						t.Errorf("%s lacks %q:\n%s", sectionFileName(msg.MessageKey, ext, n), w, data)
					}
					for m, other := range want {
						if m != i && strings.Contains(string(data), other) {
							t.Errorf("%s also holds the text of section %d (%q)", sectionFileName(msg.MessageKey, ext, n), m+1, other)
						}
					}
				}
				if _, err := os.Stat(SectionFilePath(dir, msg.MessageKey, ext, len(want)+1)); !os.IsNotExist(err) {
					t.Errorf("%s exists beyond the %d sections (err %v)", sectionFileName(msg.MessageKey, ext, len(want)+1), len(want), err)
				}
				sections, err := ReadBodySections(root, address, msg.MessageKey, ext, len(want))
				if err != nil || len(sections) != len(want) {
					t.Errorf("ReadBodySections(%s) = %d sections (err %v), want %d", ext, len(sections), err, len(want))
				}
				// The joined body the rules see keeps the MIME order.
				joined := pm.bodyForClassification()
				if ext == "html" && pm.TextCount() > 0 {
					joined = pm.secondaryBody()
				}
				pos := -1
				for _, w := range want {
					i := strings.Index(joined, w)
					if i < pos {
						t.Errorf("joined %s body has %q out of order:\n%s", ext, w, joined)
					}
					pos = i
				}
			}
			checkFiles("txt", c.textSections)
			checkFiles("html", c.htmlSections)
		})
	}

	// Sections are joined with one blank line; blank sections add nothing
	// and the per-kind size limit stops the collection.
	if got := joinSections([]string{"first\n\n\n", "\n\nsecond\n"}, "\n\n"); got != "first\n\nsecond\n" {
		t.Errorf("joinSections = %q", got)
	}
	if got := joinSections([]string{"first\n", "  \n"}, "\n\n"); got != "first\n" {
		t.Errorf("joinSections(blank) = %q", got)
	}
	if got := joinSections([]string{"", "only"}, "\n"); got != "only" {
		t.Errorf("joinSections(empty) = %q", got)
	}
	if got := joinSections(nil, "\n"); got != "" {
		t.Errorf("joinSections(nil) = %q", got)
	}
	// There is no size cap: every section is kept whole however large.
	const b = "=_many"
	const sectionSize = 3 * 1024 * 1024
	var parts strings.Builder
	parts.WriteString("From: a@example.com\r\nSubject: many\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"" + b + "\"\r\n\r\n")
	for i := 0; i < 3; i++ {
		parts.WriteString("--" + b + "\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("x", sectionSize) + "\r\n")
	}
	parts.WriteString("--" + b + "--\r\n")
	pm := ParseMessage([]byte(parts.String()))
	if pm.TextCount() != 3 {
		t.Fatalf("%d sections kept, want 3", pm.TextCount())
	}
	for i, s := range pm.TextSections {
		if len(strings.TrimSpace(s)) != sectionSize {
			t.Errorf("section %d holds %d bytes, want the whole %d", i+1, len(strings.TrimSpace(s)), sectionSize)
		}
	}
}

// readFirstSection reads the first body section file (<key>-1.<ext>) of a
// message; a missing file yields os.ErrNotExist.
func readFirstSection(root, address, key, ext string) ([]byte, error) {
	path := SectionFilePath(MailboxDir(root, address), key, ext, 1)
	if path == "" {
		return nil, ErrInvalidMessageKey
	}
	return os.ReadFile(path)
}
