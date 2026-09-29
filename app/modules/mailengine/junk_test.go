package mailengine

import (
	"bytes"
	"context"
	"testing"
	"time"

	"mailcare/app/models"
)

func TestNewMailboxIdentity(t *testing.T) {
	for _, c := range []struct{ address, domain, name string }{
		{"noreply@kakehashi-memory.example.jp", "example.jp", "example"},
		{"Admin@Mail.Example.co.jp", "example.co.jp", "example"},
		{"admin@abc.jp", "abc.jp", ""}, // a name shorter than minMailboxNameLen is not looked for
		{"broken", "", ""},
	} {
		id := newMailboxIdentity(c.address)
		if id.domain != c.domain || id.name != c.name {
			t.Errorf("%s: identity = %+v, want domain %q name %q", c.address, id, c.domain, c.name)
		}
	}
}

func TestClassifyJunk(t *testing.T) {
	const mailbox = "noreply@service-memory.jp"
	cases := []struct {
		name string
		raw  []byte
		rule string // "" = not junk
	}{
		{
			"display name claims the monitored domain (P1)",
			plainMessage("From: service-memory.jp Mail Administrator <mediacom@donation.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Notice: 7 unread messages\r\n", "Your mailbox has 7 messages waiting."),
			rulePhishingDisplayName,
		},
		{
			"display name claims the name part of the monitored domain (P1)",
			plainMessage("From: Service-memory mail system <mediacom@donation.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Delivery report\r\n", "Some mails could not be delivered."),
			rulePhishingDisplayName,
		},
		{
			"display name carries another organization's address (P1)",
			plainMessage("From: \"support@bank.example.com\" <x@attacker.example.net>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Verify your account\r\n", "Please verify."),
			rulePhishingDisplayName,
		},
		{
			"From of the monitored domain that failed DMARC (P2)",
			plainMessage("Authentication-Results: mx.service-memory.jp; dkim=none; spf=softfail; dmarc=fail (p=reject) header.from=service-memory.jp\r\n"+
				"From: Admin <admin@service-memory.jp>\r\nTo: noreply@service-memory.jp\r\nSubject: Password expiry\r\n", "Renew now."),
			rulePhishingForgedFrom,
		},
		{
			"link to another organization carrying the address (P3)",
			plainMessage("From: Mail Delivery System <mediacom@donation.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Delivery report\r\n", "Details: https://bucket.s3.amazonaws.com/login.htm?uid=noreply%40service-memory.jp"),
			rulePhishingLink,
		},
		{
			"spam verdict header (J4)",
			plainMessage("X-Spam: Yes\r\nFrom: Sarah <sarah@promo.example.info>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: thoughts?\r\n", "Great offer."),
			ruleSpamFlag,
		},
		{
			"a genuine reply from another organization is not junk",
			plainMessage("From: Camille <camille@partner.example.fr>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Re: registration guide for service-memory.jp\r\n", "Thank you, see https://partner.example.fr/profile"),
			"",
		},
		{
			"a service linking to its own subdomain with the address is not junk",
			plainMessage("From: News <news@mailer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Newsletter\r\n", "Unsubscribe: https://click.example.com/u?email=noreply@service-memory.jp"),
			"",
		},
		{
			"a company name that looks like a domain is not junk",
			plainMessage("From: Acme Co.Ltd <info@acme.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Invoice\r\n", "Please find the invoice."),
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cls := Classify(ParseMessage(c.raw), mailbox)
			if c.rule == "" {
				if cls.Kind == bounceKindJunk {
					t.Fatalf("classified as junk: %+v", cls)
				}
				return
			}
			if cls.IsBounce || cls.Kind != bounceKindJunk || cls.Rule != c.rule {
				t.Fatalf("classification = %+v, want junk by %s", cls, c.rule)
			}
		})
	}
	// The monitored server's own DSN (From of the monitored domain, no DKIM,
	// DMARC failed at the receiving server) stays a bounce: the DSN rule is
	// evaluated before the junk rules.
	ownDSN := bytes.Replace(dsnMessage("Undelivered Mail Returned to Sender", "Action: failed\r\nStatus: 5.1.1\r\n"),
		[]byte("From: MAILER-DAEMON@mx1.example.jp (Mail Delivery System)\r\n"),
		[]byte("Authentication-Results: mx.service-memory.jp; dkim=none; spf=none; dmarc=fail (p=reject) header.from=service-memory.jp\r\n"+
			"From: MAILER-DAEMON@mail.service-memory.jp (Mail Delivery System)\r\n"), 1)
	if cls := Classify(ParseMessage(ownDSN), mailbox); cls.Rule != ruleDSNReport || cls.Kind != bounceKindFailed {
		t.Errorf("own DSN = %+v, want a failed dsn_report", cls)
	}
	if !phishingForgedFrom(ParseMessage(ownDSN), newMailboxIdentity(mailbox)) {
		t.Error("the own DSN test mail does not exercise the forged-From rule")
	}
	// A spam flag never overrides a notice: a DMARC report flagged as spam
	// by the receiving server stays a report.
	flagged := append([]byte("X-Spam-Flag: YES\r\n"), dmarcMail("application/gzip", "r.xml.gz", gzipped(t, dmarcXML(recPassed)))...)
	if cls := Classify(ParseMessage(flagged), "dmarc@example.jp"); cls.Kind != bounceKindReport {
		t.Errorf("flagged DMARC report = %+v, want a report", cls)
	}
}

// TestJunkIsDeletedAndCountedApart: junk is no notice (is_bounce = 0, not
// grouped), is counted apart from the target messages, shows under its own
// kind and is a server deletion candidate like the targets.
func TestJunkIsDeletedAndCountedApart(t *testing.T) {
	root := t.TempDir()
	address := "noreply@service-memory.jp"
	dir := MailboxDir(root, address)
	db := mustOpenIndex(t, root, address)
	old := time.Now().AddDate(0, 0, -90).UTC()
	for i, raw := range [][]byte{
		plainMessage("X-Spam: Yes\r\nFrom: Sarah <sarah@promo.example.info>\r\nTo: noreply@service-memory.jp\r\nSubject: offer\r\n", "Buy now."),
		plainMessage("From: Camille <camille@partner.example.fr>\r\nTo: noreply@service-memory.jp\r\nSubject: Re: hello\r\n", "Thanks."),
		dsnMessage("Undelivered Mail Returned to Sender", "Action: failed\r\nStatus: 5.1.1\r\n"),
	} {
		if _, _, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 1, UID: uint32(i + 1), ReceivedAt: old}, storeOptions{writeEML: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE messages SET date = ?`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupMailbox(context.Background(), root, address, false, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountJunkMessages(db); n != 1 {
		t.Errorf("junk messages = %d, want 1", n)
	}
	if n, _ := CountTargetMessages(db); n != 1 {
		t.Errorf("target messages = %d, want the DSN only", n)
	}
	counts, err := models.CountMessagesByKind(db, models.MessageFilter{})
	if err != nil || counts != (models.MessageKindCounts{All: 3, Bounce: 1, Junk: 1, Other: 1}) {
		t.Errorf("kind counts = %+v (err %v)", counts, err)
	}
	junk, _, err := models.ListMessages(db, models.MessageFilter{Kind: models.MessageKindJunk})
	if err != nil || len(junk) != 1 || junk[0].IsBounce || junk[0].Rule != ruleSpamFlag {
		t.Errorf("junk list = %+v (err %v)", junk, err)
	}
	due, err := models.ListServerDeletionCandidates(db, "INBOX", time.Now().AddDate(0, 0, -30), serverDeletionRules)
	if err != nil || len(due) != 2 {
		t.Errorf("deletion candidates = %+v (err %v), want the junk and the DSN", due, err)
	}
}

func TestContainsWord(t *testing.T) {
	for _, c := range []struct {
		s, word string
		want    bool
	}{
		{"service-memory メールシステム", "service-memory", true},
		{"service-memory.jp mail administrator", "service-memory.jp", true},
		{"examples team", "example", false},
		{"counterexample", "example", false},
		{"example-support", "example", true},
		{"", "example", false},
	} {
		if got := containsWord(c.s, c.word); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v, want %v", c.s, c.word, got, c.want)
		}
	}
}

// TestMayBeDMARCReport: the XML members of an Office document are rejected
// by their root element before they are read whole.
func TestMayBeDMARCReport(t *testing.T) {
	for head, want := range map[string]bool{
		`<?xml version="1.0"?><feedback><report_metadata/>`:                                true,
		"\xef\xbb\xbf  <feedback xmlns=\"urn:ietf:params:xml:ns:dmarc-2.0\">":              true,
		`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="x"></Types>`: false,
		"PK binary":                          false,
		`<?xml version="1.0"?><!-- long -->`: true, // root not within the head: read whole
		"":                                   true,
	} {
		if got := mayBeDMARCReport([]byte(head)); got != want {
			t.Errorf("mayBeDMARCReport(%q) = %v, want %v", head, got, want)
		}
	}
}
