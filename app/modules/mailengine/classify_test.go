package mailengine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mailcare/app/models"
)

// dsnMessage builds a multipart/report delivery status notification from a
// daemon sender with the given subject and per-recipient fields.
func dsnMessage(subject, recipientFields string) []byte {
	const b = "=_dsn"
	return []byte("From: MAILER-DAEMON@mx1.example.jp (Mail Delivery System)\r\n" +
		"To: newsletter@example.jp\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Tue, 2 Sep 2025 09:15:30 +0900\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"" + b + "\"\r\n\r\n" +
		"--" + b + "\r\nContent-Type: text/plain; charset=us-ascii\r\n\r\n" +
		"This is the mail system at host mx1.example.jp.\r\n\r\n" +
		"Your message was successfully delivered to the destination(s) listed below.\r\n\r\n" +
		"<taro@customer.example.com>: delivery via mx.customer.example.com[192.0.2.10]:25: 250 2.0.0 Ok: queued as 4XYZ\r\n" +
		"\r\n--" + b + "\r\nContent-Type: message/delivery-status\r\n\r\n" +
		"Reporting-MTA: dns; mx1.example.jp\r\n\r\n" +
		"Final-Recipient: rfc822; taro@customer.example.com\r\n" +
		"Original-Recipient: rfc822;taro@customer.example.com\r\n" +
		recipientFields +
		"Remote-MTA: dns; mx.customer.example.com\r\n" +
		"Diagnostic-Code: smtp; 250 2.0.0 Ok: queued as 4XYZ\r\n" +
		"\r\n--" + b + "--\r\n")
}

// plainMessage builds a single-part text message.
func plainMessage(headers, body string) []byte {
	return []byte(headers + "Date: Tue, 2 Sep 2025 09:15:30 +0900\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

// TestClassifyAutoRepliesAndOther covers the holes of the audit: an
// automatic reply quoting a bounce subject is an auto-reply, a normal mail
// from a "Postmaster Team" display name and a success DSN are daemon mail
// without failure evidence ("other"), and none of them is grouped, while a
// daemon mail that only says so in Japanese is still a failure.
func TestClassifyAutoRepliesAndOther(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		kind string
		rule string
	}{
		{
			"automatic reply quoting an undeliverable subject",
			plainMessage("From: Hanako <hanako@partner.example.com>\r\nTo: newsletter@example.jp\r\n"+
				"Subject: Automatic reply: Undeliverable: September campaign\r\nAuto-Submitted: auto-replied\r\n",
				"I am out of the office until next week."),
			bounceKindAutoReply, "auto_reply",
		},
		{
			"auto-generated header with a bounce-like subject from a person",
			plainMessage("From: Ticket System <helpdesk@partner.example.com>\r\nTo: newsletter@example.jp\r\n"+
				"Subject: Re: Mail delivery failed: returning message to sender\r\nAuto-Submitted: auto-generated (ticket)\r\n",
				"Your request has been received as ticket #4711."),
			bounceKindAutoReply, "auto_reply",
		},
		{
			"ordinary mail from a Postmaster Team display name",
			plainMessage("From: Postmaster Team <support@hosting.example.net>\r\nTo: newsletter@example.jp\r\n"+
				"Subject: Scheduled maintenance of the mail platform\r\n",
				"Dear customer, the mail platform will be upgraded on Saturday. No action is required."),
			bounceKindOther, "daemon_display_name",
		},
		{
			"daemon sender announcing something that is not a failure",
			plainMessage("From: postmaster@hosting.example.net\r\nTo: newsletter@example.jp\r\n"+
				"Subject: New spam filter settings\r\n",
				"We have updated the spam filter. Nothing changes for you."),
			bounceKindOther, "daemon_sender",
		},
		{
			"success DSN (Action delivered)",
			dsnMessage("Successful Mail Delivery Report", "Action: delivered\r\nStatus: 2.0.0\r\n"),
			bounceKindOther, "dsn_report",
		},
		{
			"success DSN told by the status class only",
			dsnMessage("Successful Mail Delivery Report", "Status: 2.0.0 (delivered)\r\n"),
			bounceKindOther, "dsn_report",
		},
		{
			"daemon sender with a Japanese failure subject and body",
			plainMessage("From: MAILER-DAEMON@mx1.example.jp\r\nTo: newsletter@example.jp\r\n"+
				"Subject: =?UTF-8?B?6YWN5L+h5LiN6IO95pma55+l?=\r\n",
				// tsugi no atesaki ni haishin dekimasen deshita: could not deliver to the following recipient
				"\u6b21\u306e\u5b9b\u5148\u306b\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093\u3067\u3057\u305f: <taro@customer.example.com>"),
			bounceKindFailed, "daemon_sender",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pm := ParseMessage(c.raw)
			cls := Classify(pm)
			if !cls.IsBounce || cls.Kind != c.kind || cls.Rule != c.rule {
				t.Fatalf("classification = %+v, want kind %s by rule %s", cls, c.kind, c.rule)
			}
			if isGroupedKind(cls.Kind) != (c.kind == bounceKindFailed) {
				t.Errorf("isGroupedKind(%s) = %v", cls.Kind, isGroupedKind(cls.Kind))
			}
		})
	}
	// A message that is not a bounce at all stays out.
	pm := ParseMessage(plainMessage("From: hachiro@client.example.com\r\nTo: newsletter@example.jp\r\nSubject: Question\r\n",
		"Could you send me the price list?"))
	if cls := Classify(pm); cls.IsBounce {
		t.Errorf("ordinary mail classified as %+v", cls)
	}
}

// TestDSNFieldNormalization checks that Action and Status values with
// comments and mixed case are reduced to their canonical form, and that
// anything else yields "".
func TestDSNFieldNormalization(t *testing.T) {
	for in, want := range map[string]string{
		"failed":                         "failed",
		"Failed (permanent failure)":     "failed",
		"  DELAYED  ":                    "delayed",
		"delivered (to the mailbox)":     "delivered",
		"relayed":                        "relayed",
		"expanded":                       "expanded",
		"failed; something":              "",
		"unknown":                        "",
		"(only a comment)":               "",
		"":                               "",
		"failed (nested (comment) here)": "",
	} {
		if got := normalizeDSNAction(in); got != want {
			t.Errorf("normalizeDSNAction(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"5.1.1":                           "5.1.1",
		"5.1.1 (bad destination mailbox)": "5.1.1",
		"#5.1.1":                          "5.1.1",
		"4.4.1 (connection timed out)":    "4.4.1",
		"2.0.0":                           "2.0.0",
		"smtp; 550 5.7.1 blocked":         "5.7.1",
		"5.1.1.1":                         "",
		"3.1.1":                           "",
		"unknown":                         "",
		"":                                "",
	} {
		if got := normalizeDSNStatus(in); got != want {
			t.Errorf("normalizeDSNStatus(%q) = %q, want %q", in, got, want)
		}
	}
	// Through the parser and the extractor: a commented failed block gives a
	// failed notice with the plain values in the bounces row.
	raw := dsnMessage("Undelivered Mail Returned to Sender",
		"Action: failed (permanent failure)\r\nStatus: 5.1.1 (Bad destination mailbox address)\r\n")
	pm := ParseMessage(raw)
	if pm.DeliveryStatus == nil || len(pm.DeliveryStatus.Recipients) != 1 {
		t.Fatalf("delivery status not parsed: %+v", pm.DeliveryStatus)
	}
	r := pm.DeliveryStatus.Recipients[0]
	if r.Action != "failed" || r.Status != "5.1.1" {
		t.Errorf("recipient block = %+v, want action failed / status 5.1.1", r)
	}
	cls := Classify(pm)
	if cls.Kind != bounceKindFailed || cls.Rule != "dsn_report" {
		t.Fatalf("classification = %+v", cls)
	}
	b := ExtractBounce(pm, cls.Kind, "newsletter@example.jp")
	if b.Action != "failed" || b.StatusCode != "5.1.1" || b.Recipient != "taro@customer.example.com" {
		t.Errorf("bounce = action %q status %q recipient %q", b.Action, b.StatusCode, b.Recipient)
	}
}

// TestOtherKindIsNotGrouped runs the grouping phase over the audit examples
// and checks that only the failed notice gets a bounces row and a group:
// auto-replies, "other" daemon mail and success DSNs are recorded in the
// messages row (is_bounce = 1) but stay out of bounces and groups.
func TestOtherKindIsNotGrouped(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	dir := MailboxDir(root, address)
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	raws := [][]byte{
		plainMessage("From: Hanako <hanako@partner.example.com>\r\nTo: newsletter@example.jp\r\n"+
			"Subject: Automatic reply: Undeliverable: September campaign\r\nAuto-Submitted: auto-replied\r\n", "Out of office."),
		plainMessage("From: Postmaster Team <support@hosting.example.net>\r\nTo: newsletter@example.jp\r\n"+
			"Subject: Scheduled maintenance\r\n", "No action is required."),
		dsnMessage("Successful Mail Delivery Report", "Action: delivered\r\nStatus: 2.0.0\r\n"),
		readSample(t, "spamhaus_block_a.eml"),
	}
	for i, raw := range raws {
		src := Source{Folder: "INBOX", UIDValidity: 1, UID: uint32(i + 1), ReceivedAt: time.Now().UTC()}
		if _, _, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: true}); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	db.Close()
	res, err := GroupMailbox(context.Background(), root, address, false, nil)
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if res.Processed != 4 || res.Bounces != 4 || res.Groups != 1 || len(res.GroupsTouched) != 1 {
		t.Errorf("result = %+v, want 4 processed / 4 bounces / 1 group / 1 touched", res)
	}
	db = mustOpenIndex(t, root, address)
	defer db.Close()
	if n := countRows(t, db, "bounces"); n != 1 {
		t.Errorf("%d bounces rows, want 1 (only the failed notice)", n)
	}
	for _, m := range mustListAllMessages(t, db) {
		if !m.Classified || !m.IsBounce {
			t.Errorf("%s: classified=%v is_bounce=%v", m.MessageKey, m.Classified, m.IsBounce)
		}
		_, err := models.GetBounceByMessageID(db, m.ID)
		hasRow := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		if want := m.BounceKind == bounceKindFailed; hasRow != want || (m.GroupKey != "") != want {
			t.Errorf("%s (%s): bounces row %v, group %q; want row and group only for failed", m.MessageKey, m.BounceKind, hasRow, m.GroupKey)
		}
	}
	// The dashboard counts every daemon mail, the group only the failure.
	if _, bounces, _ := models.CountMessages(db); bounces != 4 {
		t.Errorf("bounce count = %d, want 4", bounces)
	}
	g, err := models.GetGroup(db, GroupKey(categoryIPBlocked, "203.0.113.5", "spamhaus.org"))
	if err != nil || g.MessageCount != 1 || !g.NeedsAnalysis {
		t.Errorf("spamhaus group = %+v (err %v)", g, err)
	}
	// Reclassifying gives the same picture (the "other" rows keep no stale
	// bounces row).
	res, err = GroupMailbox(context.Background(), root, address, true, nil)
	if err != nil || res.Groups != 1 || countRows(t, db, "bounces") != 1 {
		t.Errorf("reclassify = %+v (err %v), bounces rows %d", res, err, countRows(t, db, "bounces"))
	}
}
