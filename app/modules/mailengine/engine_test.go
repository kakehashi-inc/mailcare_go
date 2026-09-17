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
	noText     bool   // the text/plain part is missing or blank (has_text = false)
	bodySource string // "" in the table means "text" unless noText, then "html" when hasHTML
	category   string // expected group category ("" = not grouped)
	unit       string
	authority  string
	actionable bool
}

var samples = []sample{
	{
		file: "postfix_dsn.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "taro.yamada@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteMTA: "mx.customer.example.com", origSubj: "【重要】9月のお知らせ", origMsgID: "news-20250902-0001@example.jp",
		responsble: responsibleRecipient, diagHas: "User unknown in virtual mailbox table",
		category: categoryUserUnknown, unit: "taro.yamada@customer.example.com",
	},
	{
		file: "postfix_delayed.eml", isBounce: true, kind: bounceKindDelayed, reason: "dsn_report",
		subject: "Delayed Mail (still being retried)", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "hanako@slowmail.example.net", domain: "slowmail.example.net", status: "4.4.1", smtp: "",
		remoteIP: "203.0.113.7", remoteMTA: "mx.slowmail.example.net", origSubj: "Weekly digest", origMsgID: "digest-0003@example.jp",
		responsble: responsibleDomain, diagHas: "Connection timed out",
		category: categoryDeliveryDelay, unit: "slowmail.example.net",
	},
	{
		file: "exim_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "daemon_sender",
		subject: "Mail delivery failed: returning message to sender", fromAddr: "Mailer-Daemon@mail.example.org",
		recipient: "jiro@nowhere.example.org", domain: "nowhere.example.org", status: "5.1.1", smtp: "550",
		remoteIP: "203.0.113.55", remoteMTA: "mx.nowhere.example.org",
		responsble: responsibleRecipient, diagHas: "does not exist",
		category: categoryUserUnknown, unit: "jiro@nowhere.example.org",
	},
	{
		file: "qmail_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "daemon_sender",
		subject: "failure notice", fromAddr: "MAILER-DAEMON@qmail.example.net",
		recipient: "saburo@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteIP: "198.51.100.25", responsble: responsibleRecipient, diagHas: "User unknown",
		category: categoryUserUnknown, unit: "saburo@customer.example.com",
	},
	{
		file: "office365_dsn.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Undeliverable: Service maintenance notice", fromAddr: "MicrosoftExchange329e71ec88ae4615bbc36ab6ce41109e@corp.example.co.jp",
		recipient: "shiro@corp.example.co.jp", domain: "corp.example.co.jp", status: "5.1.10", smtp: "550",
		origSubj: "Service maintenance notice", origMsgID: "maint-0006@example.jp",
		responsble: responsibleRecipient, diagHas: "RecipientNotFound", hasHTML: true,
		category: categoryUserUnknown, unit: "shiro@corp.example.co.jp",
	},
	{
		file: "gmail_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Delivery Status Notification (Failure)", fromAddr: "mailer-daemon@googlemail.com",
		recipient: "goro@gmail.com", domain: "gmail.com", status: "5.1.1", smtp: "550",
		remoteMTA: "gmail-smtp-in.l.google.com", origSubj: "Welcome aboard", origMsgID: "gm-0007@example.jp",
		responsble: responsibleRecipient, diagHas: "does not exist", hasHTML: true,
		category: categoryUserUnknown, unit: "goro@gmail.com",
	},
	{
		file: "gmail_overquota.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Delivery Status Notification (Failure)", fromAddr: "mailer-daemon@googlemail.com",
		recipient: "kyuro@gmail.com", domain: "gmail.com", status: "4.2.2", smtp: "452",
		remoteMTA: "gmail-smtp-in.l.google.com", origSubj: "Welcome aboard", origMsgID: "gm-0009@example.jp",
		responsble: responsibleRecipient, diagHas: "over quota",
		category: categoryMailboxFull, unit: "kyuro@gmail.com",
	},
	{
		file: "sendmail_bounce.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Returned mail: see transcript for details", fromAddr: "MAILER-DAEMON@relay.example.edu",
		recipient: "rokuro@dept.example.edu", domain: "dept.example.edu", status: "5.1.1", smtp: "550",
		remoteMTA: "mail.dept.example.edu", origSubj: "Campus event", origMsgID: "campus-0008@example.jp",
		responsble: responsibleRecipient, diagHas: "User unknown",
		category: categoryUserUnknown, unit: "rokuro@dept.example.edu",
	},
	{
		file: "spamhaus_block_a.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "ichiro@customer.example.com", domain: "customer.example.com", status: "5.7.1", smtp: "550",
		remoteMTA: "mx.customer.example.com", origSubj: "September campaign", origMsgID: "news-20250903-0011@example.jp",
		responsble: responsibleSender, diagHas: "blocked using zen.spamhaus.org",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "spamhaus.org", actionable: true,
	},
	{
		file: "spamhaus_block_b.eml", isBounce: true, kind: bounceKindFailed, reason: "dsn_report",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "hanako@partner.example.org", domain: "partner.example.org", status: "5.7.1", smtp: "550",
		remoteMTA: "mx.partner.example.org", origSubj: "September campaign", origMsgID: "news-20250904-0012@example.jp",
		responsble: responsibleSender, diagHas: "blocked using zen.spamhaus.org",
		category: categoryIPBlocked, unit: "203.0.113.5", authority: "spamhaus.org", actionable: true,
	},
	{
		// HTML-only Exchange NDR without a delivery-status part: detection
		// and extraction work from the HTML rendered as text.
		file: "exchange_html_only.eml", isBounce: true, kind: bounceKindFailed, reason: "daemon_sender",
		subject: "Undeliverable: Service maintenance notice", fromAddr: "postmaster@corp.example.co.jp",
		recipient: "kuro.suzuki@corp.example.co.jp", domain: "corp.example.co.jp", status: "5.1.1", smtp: "550",
		responsble: responsibleRecipient, diagHas: "RESOLVER.ADR.RecipNotFound", hasHTML: true, noText: true,
		category: categoryUserUnknown, unit: "kuro.suzuki@corp.example.co.jp",
	},
	{
		// A blank text/plain part next to an HTML bounce: the HTML is used.
		file: "blank_plain_html.eml", isBounce: true, kind: bounceKindFailed, reason: "daemon_sender",
		subject: "Mail delivery failed: returning message to sender", fromAddr: "MAILER-DAEMON@relay.example.net",
		recipient: "momo@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteIP: "198.51.100.25", remoteMTA: "mx.customer.example.com",
		responsble: responsibleRecipient, diagHas: "User unknown", hasHTML: true, noText: true,
		category: categoryUserUnknown, unit: "momo@customer.example.com",
	},
	{
		// A blank HTML part next to a text bounce: the text is used, has_html is false.
		file: "plain_blank_html.eml", isBounce: true, kind: bounceKindFailed, reason: "daemon_sender",
		subject: "Undelivered Mail Returned to Sender", fromAddr: "MAILER-DAEMON@mx1.example.jp",
		recipient: "yuki@customer.example.com", domain: "customer.example.com", status: "5.1.1", smtp: "550",
		remoteIP: "198.51.100.25", remoteMTA: "mx.customer.example.com",
		responsble: responsibleRecipient, diagHas: "User unknown in virtual mailbox table",
		category: categoryUserUnknown, unit: "yuki@customer.example.com",
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
		// No original message in the broken sample: the sender address falls
		// back to the reporting MTA, which the sample lacks too, so the unit
		// stays empty and the group is per recipient domain / category.
		category: categoryContentRejected, unit: "", authority: "broken.example.net", actionable: true,
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
			if pm.HasHTML != s.hasHTML || pm.HasText == s.noText || pm.BodySource != wantSource {
				t.Errorf("has_text = %v, has_html = %v, body_source = %q; want %v / %v / %q",
					pm.HasText, pm.HasHTML, pm.BodySource, !s.noText, s.hasHTML, wantSource)
			}
			if (pm.TextBody != "") != pm.HasText || (pm.HTMLBody != "") != pm.HasHTML {
				t.Errorf("bodies kept for blank parts: text=%q html=%q", pm.TextBody, pm.HTMLBody)
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
			if b.Responsible != "" {
				t.Errorf("ExtractBounce set responsible %q; the category decides it", b.Responsible)
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
			// The bounce row carries the same responsible party as its group.
			check("responsible", b.Responsible, s.responsble)
			if b.Responsible != c.Responsible || (g != nil && g.Responsible != b.Responsible) {
				t.Errorf("bounce responsible %q differs from the category's %q", b.Responsible, c.Responsible)
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
	if !strings.Contains(pm.TextBody, "This is the mail system at host mx1.example.jp.") {
		t.Errorf("text body not taken from the notification part: %q", pm.TextBody)
	}
	if strings.Contains(pm.TextBody, "9月のお知らせをお送りします") {
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

// countBounceDetails counts the messages that carry bounce details (the
// JSON-backed bounce of the message row), read through the models API.
func countBounceDetails(t *testing.T, db *sql.DB) int {
	t.Helper()
	n := 0
	for _, m := range mustListAllMessages(t, db) {
		if b, err := models.GetBounceByMessageID(db, m.ID); err == nil {
			if b.OriginalRecipient == "" && b.Diagnostic == "" && b.StatusCode == "" {
				t.Errorf("bounce details of %s are empty: %+v", m.MessageKey, b)
			}
			n++
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	return n
}

func TestStoreAndReindexRoundTrip(t *testing.T) {
	root, address := buildMailbox(t)
	dir := MailboxDir(root, address)

	entries, _ := os.ReadDir(dir)
	var emls, jsons, htmls, txts, wantHTML, wantTxt int
	for _, s := range samples {
		if s.hasHTML {
			wantHTML++
		}
		if !s.noText {
			wantTxt++
		}
	}
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".eml":
			emls++
		case ".json":
			jsons++
		case ".html":
			htmls++
		case ".txt":
			txts++
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
	if emls != len(samples) || jsons != len(samples) || htmls != wantHTML || txts != wantTxt {
		t.Fatalf("files: %d eml, %d json, %d html, %d txt; want %d / %d / %d / %d", emls, jsons, htmls, txts,
			len(samples), len(samples), wantHTML, wantTxt)
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
	bouncesBefore := countBounceDetails(t, db)
	if groupsBefore == 0 || bouncesBefore != wantBounces-1 { // the auto-reply has no bounce details
		t.Fatalf("groups=%d bounces=%d before reindex", groupsBefore, bouncesBefore)
	}
	var withGroup int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE group_key <> ''`).Scan(&withGroup); err != nil {
		t.Fatal(err)
	}
	if withGroup != wantBounces-1 {
		t.Errorf("%d messages carry a group key, want %d", withGroup, wantBounces-1)
	}
	autoReplies := 0
	for _, m := range mustListAllMessages(t, db) {
		if m.BounceKind == bounceKindAutoReply {
			autoReplies++
			if m.GroupKey != "" {
				t.Errorf("auto reply %s has group key %q, want empty", m.MessageKey, m.GroupKey)
			}
		}
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
	if countBounceDetails(t, db) != bouncesBefore || countRows(t, db, "groups") != groupsBefore {
		t.Errorf("bounces/groups differ after reindex: %d/%d", countBounceDetails(t, db), countRows(t, db, "groups"))
	}
	for key, row := range rowsBefore {
		txt, err := ReadMessageFile(root, address, key, "txt")
		switch {
		case !row.HasText:
			if !os.IsNotExist(err) {
				t.Errorf("%s has no text part but a .txt file (err %v)", key, err)
			}
		case err != nil:
			t.Errorf("read txt %s: %v", key, err)
		case string(txt) == "stale":
			t.Errorf("reindex did not regenerate %s.txt", key)
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

// messageRow is the comparable subset of a messages row.
type messageRow struct {
	UID, UIDValidity                                                    uint32
	Folder, MessageID, Subject, From, FromName, To, Kind, Reason, Group string
	IsBounce, HasText, HasHTML                                          bool
	ToName, BodySource                                                  string
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
		if m.Date.IsZero() {
			t.Errorf("message %s has a zero date", m.MessageKey)
		}
		out[m.MessageKey] = messageRow{
			UID: m.UID, UIDValidity: m.UIDValidity, Folder: m.Folder, MessageID: m.MessageID, Subject: m.Subject,
			From: m.FromAddress, FromName: m.FromName, To: m.ToAddress, Kind: m.BounceKind, Reason: m.ClassifyReason,
			Group: m.GroupKey, IsBounce: m.IsBounce, HasText: m.HasText, HasHTML: m.HasHTML, BodySource: m.BodySource,
			Date: m.Date.UTC().Format(time.RFC3339), ToName: m.ToName,
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
	if _, err := GroupMailbox(ctx, root, address, true, nil); err != context.Canceled {
		t.Errorf("cancelled grouping err = %v", err)
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
	data, err := ReadMessageFile(root, address, msg.MessageKey, "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\"date\":") {
		t.Errorf("json must omit the missing header date:\n%s", data)
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
		OriginalRecipient:  c.recipient,
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
	if Categorize(nil, bounceKindFailed).Category != categoryUnknownFailure {
		t.Error("nil bounce must be an unknown failure")
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
	contentKey := GroupKey(categoryContentRejected, "", "broken.example.net")
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
		name                    string
		raw                     []byte
		hasText, hasHTML        bool
		source, effectiveHas    string
		wantText, wantHTMLEmpty bool
	}{
		{"plain only", []byte("From: a@example.com\r\nSubject: p\r\n\r\nplain body 550 5.1.1\r\n"), true, false, bodySourceText, "plain body", true, true},
		{"html only", []byte("From: a@example.com\r\nSubject: h\r\nContent-Type: text/html\r\n\r\n<p>html <b>body</b></p>\r\n"), false, true, bodySourceHTML, "html body", false, false},
		{"plain and html", multi(part("text/plain", "plain wins"), part("text/html", "<p>html loses</p>")), true, true, bodySourceText, "plain wins", true, false},
		{"blank plain and html", multi(part("text/plain", "  \r\n\t\r\n"), part("text/html", "<div>html used</div>")), false, true, bodySourceHTML, "html used", false, false},
		{"plain and blank html", multi(part("text/plain", "text used"), part("text/html", "<html><body> &nbsp; </body></html>")), true, false, bodySourceText, "text used", true, true},
		{"nothing", multi(part("text/plain", "\r\n"), part("text/html", "<p></p>")), false, false, "", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pm := ParseMessage(c.raw)
			if pm.HasText != c.hasText || pm.HasHTML != c.hasHTML || pm.BodySource != c.source {
				t.Errorf("has_text=%v has_html=%v body_source=%q, want %v / %v / %q", pm.HasText, pm.HasHTML, pm.BodySource, c.hasText, c.hasHTML, c.source)
			}
			if body := pm.bodyForClassification(); !strings.Contains(body, c.effectiveHas) || (c.effectiveHas == "" && body != "") {
				t.Errorf("effective body %q does not contain %q", body, c.effectiveHas)
			}
			if (pm.TextBody != "") != c.wantText || (pm.HTMLBody == "") != c.wantHTMLEmpty {
				t.Errorf("text=%q html=%q kept for blank parts", pm.TextBody, pm.HTMLBody)
			}
			// The flags survive the .json / .txt / .html round trip.
			dir := t.TempDir()
			if err := writeDerivedFiles(dir, "k", pm); err != nil {
				t.Fatal(err)
			}
			back, err := loadParsedMessage(dir, "k")
			if err != nil {
				t.Fatal(err)
			}
			if back.HasText != pm.HasText || back.HasHTML != pm.HasHTML || back.BodySource != pm.BodySource || back.bodyForClassification() != pm.bodyForClassification() {
				t.Errorf("round trip changed the body selection: %+v vs %+v", back, pm)
			}
			for ext, want := range map[string]bool{"k.txt": pm.HasText, "k.html": pm.HasHTML} {
				_, err := os.Stat(filepath.Join(dir, ext))
				if (err == nil) != want {
					t.Errorf("%s exists=%v, want %v", ext, err == nil, want)
				}
			}
		})
	}
}
