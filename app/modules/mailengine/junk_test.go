package mailengine

import (
	"bytes"
	"context"
	"os"
	"strings"
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
				"Subject: Notice: 7 unread messages\r\n", "The mailbox has 7 messages waiting."),
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
				"Subject: Verify the account\r\n", "Please verify."),
			rulePhishingDisplayName,
		},
		{
			"From of the monitored domain that failed DMARC (P2)",
			plainMessage("Authentication-Results: mx.service-memory.jp; dkim=none; spf=softfail; dmarc=fail (p=reject) header.from=service-memory.jp\r\n"+
				"From: Admin <admin@service-memory.jp>\r\nTo: noreply@service-memory.jp\r\nSubject: Password expiry\r\n", "Renew now."),
			rulePhishingForgedFrom,
		},
		{
			// With a mail delivery system display name and a bounce subject
			// the same mail is a fake notice (F1, evaluated first;
			// TestClassifyFakeNotices).
			"link to another organization carrying the address (P3)",
			plainMessage("From: Account Center <mediacom@donation.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
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
				"Subject: Re: registration guide for service-memory.jp\r\n", "Thanks, see https://partner.example.fr/profile"),
			"",
		},
		{
			"a service linking to a subdomain of itself with the address is not junk",
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
	// A DSN of the monitored server (From of the monitored domain, no DKIM,
	// DMARC failed at the receiving server) stays a bounce: the DSN rule is
	// evaluated before the junk rules.
	sourceDSN := bytes.Replace(dsnMessage("Undelivered Mail Returned to Sender", "Action: failed\r\nStatus: 5.1.1\r\n"),
		[]byte("From: MAILER-DAEMON@mx1.example.jp (Mail Delivery System)\r\n"),
		[]byte("Authentication-Results: mx.service-memory.jp; dkim=none; spf=none; dmarc=fail (p=reject) header.from=service-memory.jp\r\n"+
			"From: MAILER-DAEMON@mail.service-memory.jp (Mail Delivery System)\r\n"), 1)
	if cls := Classify(ParseMessage(sourceDSN), mailbox); cls.Rule != ruleDSNReport || cls.Kind != bounceKindFailed {
		t.Errorf("DSN of the monitored server = %+v, want a failed dsn_report", cls)
	}
	if !phishingForgedFrom(ParseMessage(sourceDSN), newMailboxIdentity(mailbox)) {
		t.Error("the DSN test mail of the monitored server does not exercise the forged-From rule")
	}
	// A spam flag never overrides a notice: a DMARC report flagged as spam
	// by the receiving server stays a report.
	flagged := append([]byte("X-Spam-Flag: YES\r\n"), dmarcMail("application/gzip", "r.xml.gz", gzipped(t, dmarcXML(recPassed)))...)
	if cls := Classify(ParseMessage(flagged), "dmarc@example.jp"); cls.Kind != bounceKindReport {
		t.Errorf("flagged DMARC report = %+v, want a report", cls)
	}
}

// TestJunkIsDeletedAndCountedApart: junk is no notice (is_bounce = 0, not
// grouped), is counted apart from the target messages, shows under a
// separate kind and is a server deletion candidate like the targets.
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
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountJunkMessages(db); n != 1 {
		t.Errorf("junk messages = %d, want 1", n)
	}
	if n, _ := CountTargetMessages(db); n != 1 {
		t.Errorf("target messages = %d, want the DSN only", n)
	}
	counts, err := models.CountMessagesByKind(db, models.MessageFilter{TargetRules: TargetRules()})
	if err != nil || counts != (models.MessageKindCounts{All: 3, Target: 1, Junk: 1, Other: 1}) {
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

// multipartMessage builds a multipart message of the given subtype
// ("alternative", "mixed") from parts that each start with their
// headers.
func multipartMessage(headers, subtype string, parts ...string) []byte {
	const b = "=_part"
	var sb strings.Builder
	sb.WriteString(headers + "Date: Tue, 2 Sep 2025 09:15:30 +0900\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/" + subtype + "; boundary=\"" + b + "\"\r\n\r\n")
	for _, p := range parts {
		sb.WriteString("--" + b + "\r\n" + p + "\r\n")
	}
	sb.WriteString("--" + b + "--\r\n")
	return []byte(sb.String())
}

func textPart(body string) string {
	return "Content-Type: text/plain; charset=utf-8\r\n\r\n" + body
}

func htmlPart(body string) string {
	return "Content-Type: text/html; charset=utf-8\r\n\r\n" + body
}

// Parts of the fake-notice tests: the returned original message, attached
// whole or as its headers only.
const (
	returnedMessagePart = "Content-Type: message/rfc822\r\n\r\n" +
		"From: noreply@service-memory.jp\r\nTo: taro@customer.example.com\r\nSubject: Invoice\r\n" +
		"Message-ID: <inv-1@service-memory.jp>\r\n\r\nPlease find the invoice.\r\n"
	returnedHeadersPart = "Content-Type: text/rfc822-headers\r\n\r\n" +
		"From: noreply@service-memory.jp\r\nTo: taro@customer.example.com\r\nSubject: Invoice\r\n" +
		"Message-ID: <inv-1@service-memory.jp>\r\n"
)

// TestClassifyFakeNotices covers the fake notices (design document "mail
// classification" 2.5): a mail that looks like a non-delivery report but
// has neither a delivery-status part nor the returned message is junk when
// it links to another organization (F1) or failed DMARC (F2), and the
// genuine reports, automatic replies and the reports of the monitored server
// keep their classification.
func TestClassifyFakeNotices(t *testing.T) {
	const (
		mailbox = "noreply@service-memory.jp"
		fake    = "From: Mail Delivery System <mailer-daemon@evil.example>\r\nTo: noreply@service-memory.jp\r\n" +
			"Subject: Undelivered Mail Returned to Sender\r\n"
		dmarcFail = "Authentication-Results: mx.service-memory.jp; spf=pass smtp.mailfrom=evil.example; dmarc=fail (p=none) header.from=evil.example\r\n"
		onHold    = "3 incoming messages for noreply@service-memory.jp are on hold."
		release   = " Release them: https://release.mailportal.example.net/inbox?id=4711"
		rejected  = "The message to taro@customer.example.com was rejected: 550 5.1.1 User unknown."
		// wording is the report wording of rule 6, which makes a mail look
		// like a notice whoever sends it.
		wording = "Delivery has failed to these recipients or groups: taro@customer.example.com"
		// releaseLine is release alone on a line: a link on the line
		// of rejected, which quotes an SMTP response, is not counted by F1.
		releaseLine = "\r\nRelease them: https://release.mailportal.example.net/inbox?id=4711"
	)
	sourceDSN := append([]byte("Authentication-Results: mx.example.jp; dkim=none; spf=none; dmarc=fail (p=reject) header.from=mx1.example.jp\r\n"),
		readSample(t, "postfix_dsn.eml")...)
	qmailDMARCFail := append([]byte("Authentication-Results: mx.example.jp; dkim=none; spf=pass; dmarc=fail (p=quarantine) header.from=qmail.example.net\r\n"),
		readSample(t, "qmail_bounce.eml")...)
	japaneseDMARCFail := append([]byte("Authentication-Results: mx.service-memory.jp; dkim=none; spf=pass; dmarc=fail (p=quarantine) header.from=mx1.example.jp\r\n"),
		readSample(t, "iso2022jp_bounce.eml")...)
	qmailCopyLink := bytes.Replace(readSample(t, "qmail_bounce.eml"), []byte("Please find the invoice attached.\r\n"),
		[]byte("Please find the invoice attached.\r\nhttps://partner.example.org/invoice\r\n"), 1)
	helpDSN := append([]byte(dmarcFail), bytes.Replace(dsnMessage("Undelivered Mail Returned to Sender", "Action: failed\r\nStatus: 5.1.1\r\n"),
		[]byte("This is the mail system at host mx1.example.jp.\r\n"),
		[]byte("This is the mail system at host mx1.example.jp.\r\nHelp: https://support.example.net/bounces\r\n"), 1)...)
	cases := []struct {
		name    string
		mailbox string // "" = mailbox
		raw     []byte
		kind    string
		rule    string // "" = ordinary mail
		// control, when set, is the same mail without what spares it, which
		// F1 must take: the case reaches the rule and is spared only by what
		// it tests.
		control []byte
	}{
		// Fake notices.
		{
			name: "link to another organization (F1)",
			raw:  plainMessage(fake, onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "link and failed DMARC: F1 is evaluated first",
			raw:  plainMessage(dmarcFail+fake, onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "failed DMARC without a link (F2)",
			raw:  plainMessage(dmarcFail+fake, onHold+" Sign in to the mailbox to release them."),
			kind: bounceKindJunk, rule: ruleFakeNoticeAuth,
		},
		{
			name: "link only in an HTML href (F1)",
			raw: multipartMessage(fake, "alternative", textPart(onHold),
				htmlPart(`<p>`+onHold+` <a href="https://release.mailportal.example.net/inbox?id=4711&amp;u=1">Release</a></p>`)),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "null Return-Path with a link (F1)",
			raw:  plainMessage("Return-Path: <>\r\n"+fake, onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "null Return-Path that failed DMARC (F2)",
			raw:  plainMessage("Return-Path: <>\r\n"+dmarcFail+fake, onHold),
			kind: bounceKindJunk, rule: ruleFakeNoticeAuth,
		},
		{
			name: "daemon Return-Path only (F1)",
			raw: plainMessage("Return-Path: <MAILER-DAEMON@evil.example>\r\nFrom: Mail Notice <notice@evil.example>\r\n"+
				"To: noreply@service-memory.jp\r\nSubject: Message quarantine report\r\n", onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "report wording only (F1)",
			raw: plainMessage("From: IT Desk <desk@evil.example>\r\nTo: noreply@service-memory.jp\r\nSubject: Message notice\r\n",
				"Delivery has failed to these recipients or groups: noreply@service-memory.jp."+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "mail delivery system display name, bounce subject and a link with the address (F1 before P3)",
			raw: plainMessage("From: Mail Delivery System <mediacom@donation.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n", "Details: https://bucket.s3.amazonaws.com/login.htm?uid=noreply%40service-memory.jp"),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			// The display name alone does not make a notice: P3 takes it.
			name: "mail delivery system display name only and a link with the address (P3)",
			raw: plainMessage("From: Mail Delivery System <mediacom@donation.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Delivery report\r\n", "Details: https://bucket.s3.amazonaws.com/login.htm?uid=noreply%40service-memory.jp"),
			kind: bounceKindJunk, rule: rulePhishingLink,
		},
		{
			name: "Auto-Submitted: no does not spare a fake notice (F1)",
			raw:  plainMessage("Auto-Submitted: no\r\n"+fake, onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "link above an inline-copy marker still counts (F1)",
			raw: plainMessage(fake, onHold+release+"\r\n\r\n--- Below this line is a copy of the message.\r\n\r\n"+
				"From: noreply@service-memory.jp\r\nSubject: Invoice\r\n\r\nPlease find the invoice."),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			// haishin funou no messeeji ga 3 tsuu arimasu. meeru no haishin ni
			// shippai shimashita: 3 undeliverable messages, delivery failed.
			// Lure words, not the wording of a genuine report.
			name: "Japanese lure words that failed DMARC (F2)",
			raw: plainMessage(dmarcFail+fake, "\u914d\u4fe1\u4e0d\u80fd\u306e\u30e1\u30c3\u30bb\u30fc\u30b8\u304c3\u901a\u3042\u308a\u307e\u3059\u3002"+
				"\u30e1\u30fc\u30eb\u306e\u914d\u4fe1\u306b\u5931\u6557\u3057\u307e\u3057\u305f\u3002"),
			kind: bounceKindJunk, rule: ruleFakeNoticeAuth,
		},
		{
			// A forward prefix does not spare a mail whose Return-Path is a
			// daemon address.
			name: "forward prefix with a daemon Return-Path (F1)",
			raw: plainMessage("Return-Path: <MAILER-DAEMON@evil.example>\r\nFrom: Mail Notice <notice@evil.example>\r\n"+
				"To: noreply@service-memory.jp\r\nSubject: Fwd: Message quarantine report\r\n", onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		// Genuine reports and other mail keep their classification.
		{
			// Only the topmost Authentication-Results (the receiving server's)
			// counts: a dmarc=fail the sender wrote below it does not.
			name: "failed DMARC only below the topmost Authentication-Results",
			raw: plainMessage("Authentication-Results: mx.service-memory.jp; spf=pass smtp.mailfrom=evil.example; dmarc=pass header.from=evil.example\r\n"+
				dmarcFail+fake, onHold+" Sign in to the mailbox to release them."),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			// A DMARC aggregate report sent from a daemon address with a link
			// to another organization stays a report (1' before F1).
			name: "DMARC aggregate report from a daemon address with a link",
			raw: []byte(strings.NewReplacer("From: noreply-dmarc@receiver.example.net\r\n", "From: postmaster@receiver.example.net\r\n",
				"This is an aggregate report.\r\n", "This is an aggregate report. About DMARC: https://dmarc.example.org/overview\r\n").
				Replace(string(dmarcMail("application/gzip", "r.xml.gz", gzipped(t, dmarcXML(recSPFMissing)))))),
			kind: bounceKindReport, rule: ruleDMARCReport,
			control: plainMessage("From: postmaster@receiver.example.net\r\nTo: noreply@service-memory.jp\r\nSubject: Report domain\r\n",
				"This is an aggregate report. About DMARC: https://dmarc.example.org/overview"),
		},
		{
			name: "DSN with a help link to another organization",
			raw:  helpDSN,
			kind: bounceKindFailed, rule: ruleDSNReport,
		},
		{
			name:    "a DSN of the monitored server that failed DMARC",
			mailbox: "newsletter@example.jp", raw: sourceDSN,
			kind: bounceKindFailed, rule: ruleDSNReport,
		},
		{
			name: "delivery-status part without recipients and a help link",
			raw: multipartMessage("From: MAILER-DAEMON@mx.hosting.example.net\r\nTo: noreply@service-memory.jp\r\nSubject: Mail delivery failed\r\n", "mixed",
				textPart(rejected+" Help: https://help.example.org/bounces"),
				"Content-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.hosting.example.net\r\n"),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name: "returned message attached and a help link",
			raw: multipartMessage("From: MAILER-DAEMON@mx.hosting.example.net\r\nTo: noreply@service-memory.jp\r\nSubject: Mail delivery failed\r\n", "mixed",
				textPart(rejected+" Help: https://help.example.org/bounces"), returnedMessagePart),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name: "returned headers attached, failed DMARC",
			raw: multipartMessage(dmarcFail+"From: MAILER-DAEMON@mx.hosting.example.net\r\nTo: noreply@service-memory.jp\r\nSubject: Mail delivery failed\r\n", "mixed",
				textPart(rejected), returnedHeadersPart),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name:    "qmail report",
			mailbox: "newsletter@example.jp", raw: readSample(t, "qmail_bounce.eml"),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name:    "qmail report that failed DMARC",
			mailbox: "newsletter@example.jp", raw: qmailDMARCFail,
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name: "qmail report whose inline copy links to another organization",
			raw:  qmailCopyLink,
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name: "Japanese report that failed DMARC",
			raw:  japaneseDMARCFail,
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			// The qmail sentences spare a report from F2 but do not widen
			// rule 6 (a server deletion target).
			name: "qmail wording relayed by an ordinary sender",
			raw: plainMessage("From: Relay Notifications <notify@relay.example.net>\r\nTo: noreply@service-memory.jp\r\nSubject: Notification #4522\r\n",
				"Hi. This is the qmail-send program at qmail.example.net.\r\n"+
					"I'm afraid I wasn't able to deliver your message to the following addresses."),
		},
		{
			// Both passes give the same answer: the wording of the HTML part
			// spares the report on the first pass already.
			name: "report wording only in the HTML part, failed DMARC",
			raw: multipartMessage(dmarcFail+"From: postmaster@corp.example.co.jp\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undeliverable: September campaign\r\n", "alternative",
				textPart("The message to shiro@corp.example.co.jp could not be delivered."),
				htmlPart("<p>Delivery has failed to these recipients or groups:</p><p>shiro@corp.example.co.jp</p>")),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name: "only a link to the From address's organization",
			raw: plainMessage("From: Mail Delivery System <mailer-daemon@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n", rejected+"\r\nHelp: https://www.hosting.example.net/help/bounces"),
			kind: bounceKindFailed, rule: ruleDaemonSender,
			control: plainMessage("From: Mail Delivery System <mailer-daemon@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n", rejected+"\r\nHelp: https://www.hosting-help.example.org/help/bounces"),
		},
		{
			name: "only a link to the monitored domain",
			raw: plainMessage("From: Mail Delivery System <mailer-daemon@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n", rejected+"\r\nResend from https://www.service-memory.jp/outbox"),
			kind: bounceKindFailed, rule: ruleDaemonSender,
			control: plainMessage("From: Mail Delivery System <mailer-daemon@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n", rejected+"\r\nResend from https://www.service-memory.example.org/outbox"),
		},
		{
			name: "only a link to the Return-Path's organization",
			raw: plainMessage("Return-Path: <bounces@relay.example.org>\r\nFrom: Mail Delivery System <mailer-daemon@mx.hosting.example.net>\r\n"+
				"To: noreply@service-memory.jp\r\nSubject: Undelivered Mail Returned to Sender\r\n", rejected+"\r\nStatus: https://status.example.org/"),
			kind: bounceKindFailed, rule: ruleDaemonSender,
			control: plainMessage("Return-Path: <bounces@relay.example.org>\r\nFrom: Mail Delivery System <mailer-daemon@mx.hosting.example.net>\r\n"+
				"To: noreply@service-memory.jp\r\nSubject: Undelivered Mail Returned to Sender\r\n", rejected+"\r\nStatus: https://status.example.com/"),
		},
		{
			// It looks like a notice by the quoted report wording; the
			// Auto-Submitted header (and the out-of-office subject) spare it.
			name: "automatic reply quoting an undeliverable subject, with a link",
			raw: plainMessage("From: Hanako <hanako@partner.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Automatic reply: Undeliverable: September campaign\r\nAuto-Submitted: auto-replied\r\n",
				"Out of the office.\r\n> "+wording+"\r\nSchedule: https://calendar.other.example.org/hanako"),
			kind: bounceKindAutoReply, rule: ruleAutoReply,
			control: plainMessage("From: Hanako <hanako@partner.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undeliverable: September campaign\r\n",
				"Out of the office.\r\n> "+wording+"\r\nSchedule: https://calendar.other.example.org/hanako"),
		},
		{
			// A person forwarding a report: the support link of the quoted
			// report does not make it a fake notice (rule 4 as before F1).
			name: "person's forward of a report with a support link in the quote",
			raw: plainMessage("From: Taro Yamada <taro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Fwd: Undelivered Mail Returned to Sender\r\n",
				"FYI, see below.\r\n\r\n---------- Forwarded message ---------\r\n"+
					"From: Mail Delivery System <MAILER-DAEMON@mx.hosting.example.net>\r\n"+
					"Subject: Undelivered Mail Returned to Sender\r\n\r\n"+
					"This is the mail system at host mx.hosting.example.net.\r\n"+
					"For help see https://support.hosting.example.net/bounces"),
			kind: bounceKindFailed, rule: ruleSubjectPattern,
			control: plainMessage("From: Taro Yamada <taro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n",
				"FYI, see below.\r\n\r\n---------- Forwarded message ---------\r\n"+
					"From: Mail Delivery System <MAILER-DAEMON@mx.hosting.example.net>\r\n"+
					"Subject: Undelivered Mail Returned to Sender\r\n\r\n"+
					"This is the mail system at host mx.hosting.example.net.\r\n"+
					"For help see https://support.hosting.example.net/bounces"),
		},
		{
			// An automatic reply without Auto-Submitted that quotes a report
			// (subject and wording), with a link in the signature (rule 3'' as
			// before F1).
			name: "automatic reply without Auto-Submitted quoting an undeliverable subject, with a link",
			raw: plainMessage("From: Hanako <hanako@partner.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Automatic reply: Undeliverable: order 123\r\n",
				"Out of the office until Monday.\r\n> "+wording+"\r\n--\r\nPartner Group https://www.partner-group.example.org/"),
			kind: bounceKindAutoReply, rule: ruleAutoReplySubject,
			control: plainMessage("From: Hanako <hanako@partner.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undeliverable: order 123\r\n",
				"Out of the office until Monday.\r\n> "+wording+"\r\n--\r\nPartner Group https://www.partner-group.example.org/"),
		},
		{
			name: "reply with a full-width colon, failed DMARC",
			raw: plainMessage(dmarcFail+"From: Jiro <jiro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: RE\uff1aRe: Undelivered Mail Returned to Sender\r\n", "Is this genuine?\r\n"+wording+"\r\n"+rejected+releaseLine),
			kind: bounceKindFailed, rule: ruleSubjectPattern,
			control: plainMessage(dmarcFail+"From: Jiro <jiro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n", "Is this genuine?\r\n"+wording+"\r\n"+rejected+releaseLine),
		},
		{
			// tensou: (forward:) haishin funou (undeliverable)
			name: "Japanese forward prefix",
			raw: plainMessage("From: Jiro <jiro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: \u8ee2\u9001: \u914d\u4fe1\u4e0d\u80fd\r\n", wording+"\r\n"+rejected+releaseLine),
			kind: bounceKindFailed, rule: ruleSubjectPattern,
			control: plainMessage("From: Jiro <jiro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: \u914d\u4fe1\u4e0d\u80fd\r\n", wording+"\r\n"+rejected+releaseLine),
		},
		{
			// henshin: (reply:) haishin funou (undeliverable)
			name: "Japanese reply prefix in brackets",
			raw: plainMessage("From: Jiro <jiro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: \u3010\u8fd4\u4fe1\u3011\u914d\u4fe1\u4e0d\u80fd\r\n", wording+"\r\n"+rejected+releaseLine),
			kind: bounceKindFailed, rule: ruleSubjectPattern,
			control: plainMessage("From: Jiro <jiro@customer.example.com>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: \u914d\u4fe1\u4e0d\u80fd\r\n", wording+"\r\n"+rejected+releaseLine),
		},
		{
			// The subject exclusions are for people's mail: a forged notice
			// from a daemon address gains nothing from a forward prefix.
			name: "forward prefix from a daemon address",
			raw:  plainMessage("From: Mail Delivery System <mailer-daemon@evil.example>\r\nTo: noreply@service-memory.jp\r\nSubject: FWD: FW: Undelivered Mail Returned to Sender\r\n", onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			// Only a prefix counts: "Returned mail" starts with "Re" but is no reply.
			name: "subject starting with Returned (F1)",
			raw: plainMessage("From: Mail Delivery System <mailer-daemon@evil.example>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Returned mail: see transcript for details\r\n", onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			name: "reply wording later in the subject (F1)",
			raw: plainMessage("From: Mail Delivery System <mailer-daemon@evil.example>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undeliverable: Re: Fwd: invoice\r\n", onHold+release),
			kind: bounceKindJunk, rule: ruleFakeNoticeLink,
		},
		{
			// Only an absent Auto-Submitted or "no" lets the fake-notice rules
			// apply; a marked mail is left to the rules after them.
			name: "fake notice marked auto-generated",
			raw:  plainMessage("Auto-Submitted: auto-generated\r\n"+dmarcFail+fake, onHold+release),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name: "From of the monitored domain that failed DMARC, no structure",
			raw: plainMessage("Authentication-Results: mx.service-memory.jp; dkim=none; spf=pass; dmarc=fail (p=reject) header.from=service-memory.jp\r\n"+
				"From: Mail Delivery System <MAILER-DAEMON@mail.service-memory.jp>\r\nTo: noreply@service-memory.jp\r\n"+
				"Subject: Undelivered Mail Returned to Sender\r\n", rejected),
			kind: bounceKindFailed, rule: ruleDaemonSender,
		},
		{
			name: "ordinary mail that failed DMARC, with a link",
			raw: plainMessage(dmarcFail+"From: Camille <camille@partner.example.fr>\r\nTo: noreply@service-memory.jp\r\nSubject: New catalogue\r\n",
				"See https://catalogue.other.example.org/2025"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mb := c.mailbox
			if mb == "" {
				mb = mailbox
			}
			cls := Classify(ParseMessage(c.raw), mb)
			if cls.Kind != c.kind || cls.Rule != c.rule || cls.IsBounce != (c.kind != "" && c.kind != bounceKindJunk) {
				t.Fatalf("classification = %+v, want kind %q by rule %q", cls, c.kind, c.rule)
			}
			if c.control != nil {
				if got := Classify(ParseMessage(c.control), mb); got.Rule != ruleFakeNoticeLink {
					t.Errorf("control = %+v: the case does not reach F1", got)
				}
			}
		})
	}
}

// TestGenuineNoticeWording: the wording that spares a report from F2 is
// rule 6 plus whole sentences of qmail and Japanese reports, never the
// single words a fake notice uses as its lure; rule 6 itself is not widened.
func TestGenuineNoticeWording(t *testing.T) {
	for s, want := range map[string]bool{
		"Delivery has failed to these recipients or groups:":                           true, // rule 6
		"Hi. This is the qmail-send program at qmail.example.net.":                     true,
		"I'm afraid I wasn't able to deliver your message to the following addresses.": true,
		// ika no atesaki ni messeeji wo haishin dekimasen deshita (iso2022jp_bounce.eml)
		"\u4ee5\u4e0b\u306e\u5b9b\u5148\u306b\u30e1\u30c3\u30bb\u30fc\u30b8\u3092\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093\u3067\u3057\u305f\u3002": true,
		// tsugi no atesaki e haishin dekimasen deshita (sjis_html_bounce.eml)
		"\u6b21\u306e\u5b9b\u5148\u3078\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093\u3067\u3057\u305f": true,
		// tsugi no jushinsha mataha guruupu e no haishin ni shippai shimashita (Exchange)
		"\u6b21\u306e\u53d7\u4fe1\u8005\u307e\u305f\u306f\u30b0\u30eb\u30fc\u30d7\u3078\u306e\u914d\u4fe1\u306b\u5931\u6557\u3057\u307e\u3057\u305f:": true,
		// haishin funou no messeeji ga 3 tsuu arimasu (3 undeliverable messages)
		"\u914d\u4fe1\u4e0d\u80fd\u306e\u30e1\u30c3\u30bb\u30fc\u30b8\u304c3\u901a\u3042\u308a\u307e\u3059": false,
		// meeru no haishin ni shippai shimashita (the mail could not be delivered)
		"\u30e1\u30fc\u30eb\u306e\u914d\u4fe1\u306b\u5931\u6557\u3057\u307e\u3057\u305f": false,
		// futatsu meeru wo kakunin shite kudasai (check the undelivered mail)
		"\u4e0d\u9054\u30e1\u30fc\u30eb\u3092\u78ba\u8a8d\u3057\u3066\u304f\u3060\u3055\u3044": false,
		"Messages could not be delivered. Release them now.":                                   false,
	} {
		if got := genuineNoticeBodyRe.MatchString(s); got != want {
			t.Errorf("genuineNoticeBodyRe(%q) = %v, want %v", s, got, want)
		}
	}
	if bounceBodyRe.MatchString("Hi. This is the qmail-send program at qmail.example.net.") {
		t.Error("rule 6 matches the qmail preamble")
	}
}

// TestFakeNoticeLinkStopsAtInlineCopy: the links of the returned message
// quoted below an inline-copy marker are not the report's, whatever the
// MTA's marker; a link above the marker still counts.
func TestFakeNoticeLinkStopsAtInlineCopy(t *testing.T) {
	const headers = "From: MAILER-DAEMON@mx.hosting.example.net\r\nTo: noreply@service-memory.jp\r\nSubject: Mail delivery failed\r\n"
	const rejected = "The message to taro@customer.example.com was rejected: 550 5.1.1 User unknown."
	const original = "From: noreply@service-memory.jp\r\nSubject: Invoice\r\n\r\nPay online: https://partner.example.org/invoice"
	for _, marker := range []string{
		"--- Below this line is a copy of the message.",
		"------ This is a copy of the message, including all the headers. ------",
		"----- Original message follows -----",
		"-----Original Message-----",
		"ORIGINAL MESSAGE FOLLOWS",
	} {
		quoted := rejected + "\r\n\r\n" + marker + "\r\n\r\n" + original
		if cls := Classify(ParseMessage(plainMessage(headers, quoted)), "noreply@service-memory.jp"); cls.Rule != ruleDaemonSender || cls.Kind != bounceKindFailed {
			t.Errorf("%q: classification = %+v, want a failed daemon_sender", marker, cls)
		}
		html := "<p>" + rejected + "</p><p>" + marker + "</p><pre>" + original + "</pre>"
		raw := multipartMessage(headers, "alternative", textPart(quoted), htmlPart(html))
		if cls := Classify(ParseMessage(raw), "noreply@service-memory.jp"); cls.Rule != ruleDaemonSender {
			t.Errorf("%q (HTML): classification = %+v, want daemon_sender", marker, cls)
		}
		above := rejected + "\r\nHelp: https://help.other.example.org/\r\n\r\n" + marker + "\r\n\r\n" + original
		if cls := Classify(ParseMessage(plainMessage(headers, above)), "noreply@service-memory.jp"); cls.Rule != ruleFakeNoticeLink {
			t.Errorf("%q, link above: classification = %+v, want fake_notice_link", marker, cls)
		}
		// The same link on the line that quotes the SMTP response does not
		// count (TestFakeNoticeLinkSkipsQuotedResponses).
		onResponse := rejected + " Help: https://help.other.example.org/\r\n\r\n" + marker + "\r\n\r\n" + original
		if cls := Classify(ParseMessage(plainMessage(headers, onResponse)), "noreply@service-memory.jp"); cls.Rule != ruleDaemonSender {
			t.Errorf("%q, link on the response line: classification = %+v, want daemon_sender", marker, cls)
		}
	}
}

// htmlMessage builds a message whose only body is an HTML part.
func htmlMessage(headers, body string) []byte {
	return []byte(headers + "Date: Tue, 2 Sep 2025 09:15:30 +0900\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" + body + "\r\n")
}

// TestFakeNoticeLinkSkipsQuotedResponses: F1 does not count a URL on a line
// of a text report that quotes the response of a remote server (a blocklist
// names its lookup page there), in the forms of qmail, Exim, Sendmail and
// Postfix; a link on any other line still counts, and a URL never makes the
// line it is on look like a response.
func TestFakeNoticeLinkSkipsQuotedResponses(t *testing.T) {
	const (
		mailbox = "noreply@service-memory.jp"
		daemon  = "From: Mail Delivery System <MAILER-DAEMON@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\nSubject: Mail delivery failed\r\n"
		fake    = "From: Mail Delivery System <mailer-daemon@evil.example>\r\nTo: noreply@service-memory.jp\r\n" +
			"Subject: Undelivered Mail Returned to Sender\r\n"
		failed = "The message to taro@customer.example.com could not be delivered.\r\n"
	)
	qmail := plainMessage("Return-Path: <>\r\nFrom: MAILER-DAEMON@qmail.example.net\r\nTo: noreply@service-memory.jp\r\nSubject: failure notice\r\n",
		"Hi. This is the qmail-send program at qmail.example.net.\r\n"+
			"I'm afraid I wasn't able to deliver your message to the following addresses.\r\n"+
			"This is a permanent error; I've given up. Sorry it didn't work out.\r\n\r\n"+
			"<taro@customer.example.com>:\r\n198.51.100.25 does not like recipient.\r\n"+
			"Remote host said: 554 5.7.1 Service unavailable; Client host [203.0.113.5] blocked using zen.spamhaus.org; https://www.spamhaus.org/query/ip/203.0.113.5\r\n"+
			"Giving up on 198.51.100.25.\r\n\r\n"+
			"--- Below this line is a copy of the message.\r\n\r\n"+
			"From: noreply@service-memory.jp\r\nTo: taro@customer.example.com\r\nSubject: Invoice\r\n\r\nPlease find the invoice.")
	exim := plainMessage("From: Mail Delivery System <Mailer-Daemon@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\n"+
		"Subject: Mail delivery failed: returning message to sender\r\n",
		"This message was created automatically by mail delivery software.\r\n\r\n"+
			"A message that you sent could not be delivered to one or more of its\r\n"+
			"recipients. This is a permanent error. The following address(es) failed:\r\n\r\n"+
			"  hanako@mail.example.com\r\n"+
			"    host mx.mail.example.com [198.51.100.27]\r\n"+
			"    SMTP error from remote mail server after end of data:\r\n"+
			"    550-5.7.1 [203.0.113.5] Our system has detected that this message is\r\n"+
			"    550-5.7.1 likely unsolicited mail. Please visit\r\n"+
			"    550 5.7.1  https://support.mail.example.com/?p=UnsolicitedMessageError\r\n")
	sendmail := plainMessage("From: Mail Delivery Subsystem <MAILER-DAEMON@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\n"+
		"Subject: Returned mail: see transcript for details\r\n",
		"The original message was received at Tue, 2 Sep 2025 09:15:00 +0900\r\n"+
			"from app01.service-memory.jp [203.0.113.20]\r\n\r\n"+
			"   ----- The following addresses had permanent fatal errors -----\r\n"+
			"<taro@customer.example.com>\r\n"+
			"    (reason: 554 5.7.1 Client host [203.0.113.5] blocked using bl.spamcop.net; https://www.spamcop.net/bl.shtml?203.0.113.5)\r\n\r\n"+
			"   ----- Transcript of session follows -----\r\n"+
			"... while talking to mx.customer.example.com.:\r\n"+
			">>> DATA\r\n"+
			"<<< 554-Service unavailable; see https://www.spamcop.net/bl.shtml?203.0.113.5\r\n"+
			"<<< 554 Transaction failed, https://help.customer.example.com/554\r\n"+
			"554 5.0.0 Service unavailable\r\n")
	for _, c := range []struct {
		name string
		raw  []byte
		rule string
	}{
		{"qmail, blocklist page in the response", qmail, ruleDaemonSender},
		{"exim, response on reply code lines", exim, ruleDaemonSender},
		{"sendmail, reason and transcript", sendmail, ruleDaemonSender},
		{"postfix said line", plainMessage(daemon, "<taro@customer.example.com>: host mx.customer.example.com[198.51.100.25] said: "+
			"550 Rejected, see https://postmaster.customer.example.com/550 (in reply to RCPT TO command)"), ruleDaemonSender},
		{"remote server response", plainMessage(daemon, failed+"Remote server response: Mailbox unavailable https://help.customer.example.com/"), ruleDaemonSender},
		{"remote server reply", plainMessage(daemon, failed+"Last reply: Mailbox unavailable https://help.customer.example.com/"), ruleDaemonSender},
		{"smtp error from remote", plainMessage(daemon, failed+"SMTP error from remote server: https://help.customer.example.com/"), ruleDaemonSender},
		{"enhanced status code alone", plainMessage(daemon, failed+"Status: 5.7.1 see https://www.spamhaus.org/query/ip/203.0.113.5"), ruleDaemonSender},
		{"reply code at the start of the line", plainMessage(daemon, failed+"  554 Rejected https://help.customer.example.com/"), ruleDaemonSender},
		// The accepted evasion: a link on a line that looks like a response.
		{"link on a line with a reply code", plainMessage(fake, "The mail was rejected: 550 5.1.1. Release it: https://release.mailportal.example.net/inbox"), ruleDaemonSender},
		// A link on any other line counts.
		{"phishing line", plainMessage(fake, "3 messages are on hold.\r\nClick here to retry: https://login.mailportal.example.net/x"), ruleFakeNoticeLink},
		{"link below a response line", plainMessage(fake, "Remote host said: 550 5.1.1 User unknown\r\nRelease: https://login.mailportal.example.net/x"), ruleFakeNoticeLink},
		{"the URL does not make the line it is on a response", plainMessage(fake, "Release them: https://release.mailportal.example.net/5.1.1/554-5.7.1/said:"), ruleFakeNoticeLink},
		{"reply code later in the line is no reply code", plainMessage(fake, "Order 550 is waiting: https://release.mailportal.example.net/inbox"), ruleFakeNoticeLink},
	} {
		t.Run(c.name, func(t *testing.T) {
			cls := Classify(ParseMessage(c.raw), mailbox)
			if cls.Rule != c.rule {
				t.Fatalf("classification = %+v, want rule %s", cls, c.rule)
			}
			if c.rule == ruleDaemonSender && cls.Kind != bounceKindFailed {
				t.Errorf("kind = %s, want failed", cls.Kind)
			}
		})
	}
	// The qmail report is an alert again: its bounce is an IP block.
	pm := ParseMessage(qmail)
	cls := Classify(pm, mailbox)
	b := ExtractBounce(pm, cls.Kind, mailbox)
	if g := groupForBounce(cls.Kind, b); g == nil || g.Category != categoryIPBlocked || !g.Actionable {
		t.Errorf("qmail group = %+v (bounce %+v)", g, b)
	}
}

// TestFakeNoticeLinkHTML: in an HTML section F1 counts only the href of the
// <a> and <area> tags (decoded, absolute http(s) or network-path), never the
// rest of the markup or a URL written as text.
func TestFakeNoticeLinkHTML(t *testing.T) {
	const (
		mailbox = "noreply@service-memory.jp"
		headers = "From: Mail Delivery System <MAILER-DAEMON@mx.hosting.example.net>\r\nTo: noreply@service-memory.jp\r\nSubject: Mail delivery failed\r\n"
		failed  = "<p>The message to taro@customer.example.com could not be delivered.</p>"
		page    = "<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Transitional//EN\" \"http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd\">\r\n" +
			"<html xmlns=\"http://www.w3.org/1999/xhtml\"><head><title>Mail delivery failed</title></head><body>" + failed
	)
	for _, c := range []struct {
		name, body string
		rule       string
	}{
		{"document type and namespaces", page + "</body></html>", ruleDaemonSender},
		{"images, scripts, style sheets and CSS", page + `<img src="https://cdn.other.example/logo.png">` +
			`<script src="https://scripts.other.example/a.js"></script><link rel="stylesheet" href="https://css.other.example/s.css">` +
			`<div style="background:url(https://img.other.example/bg.png)">x</div><base href="https://base.other.example/"></body></html>`, ruleDaemonSender},
		{"anchor to another organization", failed + `<p><a href="https://release.evil.example/inbox">Release</a></p>`, ruleFakeNoticeLink},
		{"anchor in single quotes, upper case", failed + `<P><A CLASS=x HREF='HTTPS://release.evil.example/inbox'>Release</A></P>`, ruleFakeNoticeLink},
		{"anchor unquoted", failed + `<a href=https://release.evil.example/inbox>Release</a>`, ruleFakeNoticeLink},
		{"anchor with an encoded query", failed + `<a href="https://release.evil.example/inbox?id=1&amp;u=2">Release</a>`, ruleFakeNoticeLink},
		{"anchor with an encoded scheme", failed + `<a href="&#104;ttps://release.evil.example/inbox">Release</a>`, ruleFakeNoticeLink},
		{"anchor with white space", failed + "<a href=\" https://release.\n\tevil.example/inbox \">Release</a>", ruleFakeNoticeLink},
		{"network-path reference", failed + `<a href="//release.evil.example/inbox">Release</a>`, ruleFakeNoticeLink},
		{"area of an image map", failed + `<map name="m"><area shape="rect" coords="0,0,1,1" href="https://release.evil.example/inbox"></map>`, ruleFakeNoticeLink},
		{"relative, fragment, mailto and javascript links", failed + `<a href="/help">Help</a> <a href="help.html">More</a> <a href="#top">Top</a>` +
			`<a href="mailto:help@evil.example">Mail</a> <a href="javascript:void(0)">Run</a>`, ruleDaemonSender},
		{"data-href is no href", failed + `<a data-href="https://release.evil.example/inbox">Release</a>`, ruleDaemonSender},
		{"commented-out anchor", failed + `<!-- <a href="https://release.evil.example/inbox">Release</a> -->`, ruleDaemonSender},
		{"URL written as text", failed + `<p>Release them at https://release.evil.example/inbox</p>`, ruleDaemonSender},
		{"anchor to the From address's organization", failed + `<a href="https://www.hosting.example.net/help">Help</a>`, ruleDaemonSender},
		{"anchor below an inline copy", failed + `<p>--- Below this line is a copy of the message.</p><a href="https://partner.example.org/invoice">Pay</a>`, ruleDaemonSender},
	} {
		t.Run(c.name, func(t *testing.T) {
			cls := Classify(ParseMessage(htmlMessage(headers, c.body)), mailbox)
			if cls.Rule != c.rule {
				t.Fatalf("classification = %+v, want rule %s", cls, c.rule)
			}
		})
	}
	// The link is read as the browser would follow it.
	pm := ParseMessage(htmlMessage(headers, failed+`<a href="https://release.evil.example/inbox?id=1&amp;u=2">Release</a>`))
	if links := noticeLinks(pm); len(links) != 1 || links[0].url != "https://release.evil.example/inbox?id=1&u=2" || links[0].domain != "evil.example" {
		t.Errorf("noticeLinks = %+v", links)
	}
}

// TestPhishingLinkReadsEverySection: P3 keeps reading every URL of the
// sections, the raw HTML markup and the lines that quote a response
// included (F1 counts fewer, noticeLinks).
func TestPhishingLinkReadsEverySection(t *testing.T) {
	const (
		mailbox = "noreply@service-memory.jp"
		headers = "From: Account Center <mediacom@donation.example.com>\r\nTo: noreply@service-memory.jp\r\nSubject: Notice\r\n"
	)
	for name, raw := range map[string][]byte{
		"response line": plainMessage(headers, "Remote host said: 550 5.1.1 https://login.evil.example/?u=noreply@service-memory.jp"),
		"image source":  htmlMessage(headers, `<p>Notice</p><img src="https://login.evil.example/t.gif?u=noreply%40service-memory.jp">`),
	} {
		if cls := Classify(ParseMessage(raw), mailbox); cls.Rule != rulePhishingLink {
			t.Errorf("%s: classification = %+v, want phishing_link", name, cls)
		}
	}
}

// TestLooksLikeNotice: a mail presents itself as a non-delivery report (F1,
// F2 and the backscatter rule) by a daemon address, the report wording, or a
// mail delivery system display name together with a bounce subject; the
// display name or the subject alone does not count.
func TestLooksLikeNotice(t *testing.T) {
	for _, c := range []struct {
		name, headers, body string
		html                bool
		want                bool
	}{
		{"daemon From", "From: MAILER-DAEMON@mx.example.net\r\nSubject: Hello\r\n", "Hello.", false, true},
		{"daemon Return-Path", "Return-Path: <postmaster@mx.example.net>\r\nFrom: Notice <notice@mx.example.net>\r\nSubject: Hello\r\n", "Hello.", false, true},
		{"report wording in the text", "From: IT Desk <desk@example.net>\r\nSubject: Hello\r\n", "Delivery has failed to these recipients or groups:", false, true},
		{"report wording in the HTML", "From: IT Desk <desk@example.net>\r\nSubject: Hello\r\n", "<p>Delivery has failed to these recipients or groups:</p>", true, true},
		{"display name and bounce subject", "From: Mail Delivery System <noreply@example.net>\r\nSubject: Undelivered Mail Returned to Sender\r\n", "Hello.", false, true},
		{"display name only", "From: Mail Delivery System <noreply@example.net>\r\nSubject: Hello\r\n", "Hello.", false, false},
		{"bounce subject only", "From: Parcel Service <info@example.net>\r\nSubject: Parcel delivery has failed\r\n", "Hello.", false, false},
		{"nothing", "From: Camille <camille@example.net>\r\nSubject: Hello\r\n", "Hello.", false, false},
	} {
		raw := plainMessage(c.headers, c.body)
		if c.html {
			raw = htmlMessage(c.headers, c.body)
		}
		if got := looksLikeNotice(ParseMessage(raw)); got != c.want {
			t.Errorf("%s: looksLikeNotice = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestFakeNoticeLookAlike: the announcements and the carrier mails that
// carry a link to the site of the sender keep their former rule; a mail that only
// borrows the display name of a mail delivery system is not junk (an
// accepted gap), while one that adds a bounce subject is a fake notice.
func TestFakeNoticeLookAlike(t *testing.T) {
	const mailbox = "noreply@service-memory.jp"
	for _, c := range []struct {
		name string
		raw  []byte
		kind string
		rule string
	}{
		{"maintenance announcement of a mail administrator",
			plainMessage("From: Mail Administrator <admin@mail.hosting-net.example>\r\nTo: noreply@service-memory.jp\r\nSubject: Scheduled maintenance of the mail servers\r\n",
				"The mail servers will be maintained on Sunday.\r\nDetails: https://www.hosting-co.example/news/2025/maintenance"),
			bounceKindOther, ruleDaemonDisplayName},
		{"carrier mail with a tracking link",
			plainMessage("From: Parcel Service <notify@parcel-carrier.example>\r\nTo: noreply@service-memory.jp\r\nSubject: Parcel delivery has failed\r\n",
				"The parcel could not be handed over today.\r\nTrack it: https://track.parcel-tracking.example/p/123"),
			bounceKindFailed, ruleSubjectPattern},
		{"display name and bounce subject, text link",
			plainMessage("From: Mail Delivery System <noreply@evil.example>\r\nTo: noreply@service-memory.jp\r\nSubject: Undelivered Mail Returned to Sender\r\n",
				"3 messages are on hold.\r\nRelease them: https://release.mailportal.example.net/inbox"),
			bounceKindJunk, ruleFakeNoticeLink},
		{"display name and bounce subject, HTML anchor",
			htmlMessage("From: Mail Delivery System <noreply@evil.example>\r\nTo: noreply@service-memory.jp\r\nSubject: Undelivered Mail Returned to Sender\r\n",
				`<p>3 messages are on hold.</p><p><a href="https://release.mailportal.example.net/inbox">Release them</a></p>`),
			bounceKindJunk, ruleFakeNoticeLink},
		{"display name only, link without the address",
			plainMessage("From: Mail Delivery System <noreply@evil.example>\r\nTo: noreply@service-memory.jp\r\nSubject: Mailbox storage\r\n",
				"The mailbox is almost at its limit.\r\nUpgrade now: https://upgrade.evil.example/storage"),
			bounceKindOther, ruleDaemonDisplayName},
	} {
		t.Run(c.name, func(t *testing.T) {
			cls := Classify(ParseMessage(c.raw), mailbox)
			if cls.Kind != c.kind || cls.Rule != c.rule {
				t.Fatalf("classification = %+v, want kind %q by rule %q", cls, c.kind, c.rule)
			}
		})
	}
}

// TestFakeNoticeSparesSamples: none of the sample notices and mails is taken
// for a fake notice, whether or not the monitored address is known.
func TestFakeNoticeSparesSamples(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".eml") {
			continue
		}
		pm := ParseMessage(readSample(t, e.Name()))
		for _, mailbox := range []string{"", "newsletter@example.jp"} {
			if cls := Classify(pm, mailbox); cls.Rule == ruleFakeNoticeLink || cls.Rule == ruleFakeNoticeAuth {
				t.Errorf("%s (mailbox %q) = %+v, want no fake notice", e.Name(), mailbox, cls)
			}
		}
	}
}

// TestFakeNoticesAreJunk: the fake notices are junk like the phishing
// rules: no notice (is_bounce = 0, no bounces row, not grouped), counted
// apart from the target messages, listed under the junk kind and server
// deletion candidates.
func TestFakeNoticesAreJunk(t *testing.T) {
	root := t.TempDir()
	address := "noreply@service-memory.jp"
	dir := MailboxDir(root, address)
	db := mustOpenIndex(t, root, address)
	old := time.Now().AddDate(0, 0, -90).UTC()
	fake := "From: Mail Delivery System <mailer-daemon@evil.example>\r\nTo: noreply@service-memory.jp\r\nSubject: Undelivered Mail Returned to Sender\r\n"
	for i, raw := range [][]byte{
		plainMessage(fake, "Messages on hold. Release them: https://release.mailportal.example.net/inbox"),
		plainMessage("Authentication-Results: mx.service-memory.jp; dmarc=fail header.from=evil.example\r\n"+fake, "Messages on hold."),
		plainMessage("From: Camille <camille@partner.example.fr>\r\nTo: noreply@service-memory.jp\r\nSubject: Re: hello\r\n", "Thanks."),
		readSample(t, "qmail_bounce.eml"),
	} {
		if _, _, err := storeMessage(db, dir, raw, Source{Folder: "INBOX", UIDValidity: 1, UID: uint32(i + 1), ReceivedAt: old}, storeOptions{writeEML: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE messages SET date = ?`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountJunkMessages(db); n != 2 {
		t.Errorf("junk messages = %d, want the two fake notices", n)
	}
	if n, _ := CountTargetMessages(db); n != 1 {
		t.Errorf("target messages = %d, want the qmail report only", n)
	}
	counts, err := models.CountMessagesByKind(db, models.MessageFilter{TargetRules: TargetRules()})
	if err != nil || counts != (models.MessageKindCounts{All: 4, Target: 1, Junk: 2, Other: 1}) {
		t.Errorf("kind counts = %+v (err %v)", counts, err)
	}
	junk, _, err := models.ListMessages(db, models.MessageFilter{Kind: models.MessageKindJunk})
	if err != nil || len(junk) != 2 {
		t.Fatalf("junk list = %+v (err %v)", junk, err)
	}
	rules := map[string]bool{}
	for _, m := range junk {
		if m.IsBounce || m.GroupKey != "" {
			t.Errorf("%s: is_bounce %v, group %q; want neither", m.MessageKey, m.IsBounce, m.GroupKey)
		}
		rules[m.Rule] = true
	}
	if !rules[ruleFakeNoticeLink] || !rules[ruleFakeNoticeAuth] {
		t.Errorf("junk rules = %v, want both fake-notice rules", rules)
	}
	if n := countRows(t, db, "bounces"); n != 1 {
		t.Errorf("%d bounces rows, want 1 (the qmail report)", n)
	}
	due, err := models.ListServerDeletionCandidates(db, "INBOX", time.Now().AddDate(0, 0, -30), serverDeletionRules)
	if err != nil || len(due) != 3 {
		t.Errorf("deletion candidates = %+v (err %v), want the fake notices and the qmail report", due, err)
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
