package mailengine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

// dsnBlock is one per-recipient block of multiNotice.
type dsnBlock struct {
	recipient, action, status, diagnostic string
}

// multiNotice builds a delivery status notification with one
// per-recipient block per entry (an empty action or status is left out).
func multiNotice(subject string, blocks ...dsnBlock) []byte {
	const b = "=_multi"
	var body, status strings.Builder
	for _, bl := range blocks {
		if bl.diagnostic != "" {
			body.WriteString("<" + bl.recipient + ">: " + bl.diagnostic + "\r\n")
		}
		status.WriteString("\r\nFinal-Recipient: rfc822; " + bl.recipient + "\r\n")
		if bl.action != "" {
			status.WriteString("Action: " + bl.action + "\r\n")
		}
		if bl.status != "" {
			status.WriteString("Status: " + bl.status + "\r\n")
		}
		if bl.diagnostic != "" {
			status.WriteString("Diagnostic-Code: smtp; " + bl.diagnostic + "\r\n")
		}
	}
	return []byte("Return-Path: <>\r\n" +
		"From: MAILER-DAEMON@mx1.example.jp (Mail Delivery System)\r\n" +
		"To: newsletter@example.jp\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Tue, 2 Sep 2025 09:15:30 +0900\r\n" +
		"Message-ID: <multi@mx1.example.jp>\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"" + b + "\"\r\n\r\n" +
		"--" + b + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		"This is the mail system at host mx1.example.jp.\r\n\r\n" + body.String() +
		"\r\n--" + b + "\r\nContent-Type: message/delivery-status\r\n\r\n" +
		"Reporting-MTA: dns; mx1.example.jp\r\n" + status.String() +
		"\r\n--" + b + "\r\nContent-Type: text/rfc822-headers\r\n\r\n" +
		"From: Newsletter <newsletter@example.jp>\r\nTo: undisclosed-recipients:;\r\nSubject: Weekly digest\r\n" +
		"Date: Tue, 2 Sep 2025 09:10:00 +0900\r\nMessage-ID: <digest-multi@example.jp>\r\n" +
		"\r\n--" + b + "--\r\n")
}

// multiBlocks are the blocks of a notice that reports two unknown users of
// one domain, a refused sender at another, a delay, a block judged by its
// status alone, the monitored address itself and three blocks that report
// no failure.
var multiBlocks = []dsnBlock{
	{"a1@customer.example.com", "failed", "5.1.1", "550 5.1.1 <a1@customer.example.com>: Recipient address rejected: User unknown"},
	{"ok@partner.example.org", "delivered", "2.0.0", "250 2.0.0 Ok: queued"},
	{"a2@customer.example.com", "failed", "5.1.1", "550 5.1.1 <a2@customer.example.com>: Recipient address rejected: User unknown"},
	{"b1@other.example.net", "failed", "5.2.2", "552 5.2.2 Mailbox full"},
	{"relay@partner.example.org", "relayed", "2.0.0", ""},
	{"slow@slowmail.example.net", "delayed", "4.4.1", "connect to mx.slowmail.example.net[203.0.113.7]:25: Connection timed out"},
	{"list@partner.example.org", "expanded", "2.0.0", ""},
	{"c1@third.example.net", "", "5.1.1", "550 5.1.1 User unknown"},
	{"newsletter@example.jp", "failed", "5.1.1", "550 5.1.1 User unknown"},
}

// TestExtractBouncesPerRecipient: every block that reports a failure or a
// delay gives a row, in block order, with the recipient, status and
// diagnostic of that block (the first one completed from the body as
// before) and the action of that block (a block without one by its
// status); delivered, relayed and expanded blocks and the excluded address
// give none.
func TestExtractBouncesPerRecipient(t *testing.T) {
	pm := ParseMessage(multiNotice("Undelivered Mail Returned to Sender", multiBlocks...))
	cls := Classify(pm, "newsletter@example.jp")
	if !cls.IsBounce || cls.Kind != bounceKindFailed {
		t.Fatalf("classification %+v, want a failed notice", cls)
	}
	rows := ExtractBounces(pm, cls.Kind, pm.FromAddress, "newsletter@example.jp")
	want := []struct{ recipient, action, status string }{
		{"a1@customer.example.com", "failed", "5.1.1"},
		{"a2@customer.example.com", "failed", "5.1.1"},
		{"b1@other.example.net", "failed", "5.2.2"},
		{"slow@slowmail.example.net", "delayed", "4.4.1"},
		{"c1@third.example.net", "failed", "5.1.1"},
	}
	if len(rows) != len(want) {
		var got []string
		for _, r := range rows {
			got = append(got, r.Recipient)
		}
		t.Fatalf("rows %v, want %d", got, len(want))
	}
	for i, w := range want {
		r := rows[i]
		if r.Recipient != w.recipient || r.Action != w.action || r.StatusCode != w.status || r.ReportingMTA != "mx1.example.jp" ||
			r.OriginalSubject != "Weekly digest" || r.DiagnosticSource != DiagnosticSourceDSN || r.PatternKey == "" {
			t.Errorf("row %d = %+v, want %+v", i, r, w)
		}
		if got := BounceKind(r, cls.Kind); got != w.action {
			t.Errorf("row %d judged as %s, want %s", i, got, w.action)
		}
	}
	// One block: one row, as ExtractBounce gives it.
	single := ParseMessage(multiNotice("Undelivered Mail Returned to Sender", multiBlocks[0], multiBlocks[1]))
	if rows := ExtractBounces(single, bounceKindFailed); len(rows) != 1 || *rows[0] != *ExtractBounce(single, bounceKindFailed) {
		t.Errorf("single failed block: %+v", rows)
	}
	// No delivery-status part: the one row of the body.
	plain := ParseMessage([]byte("From: MAILER-DAEMON@mx1.example.jp\r\nTo: newsletter@example.jp\r\nSubject: Undelivered Mail\r\n\r\n" +
		"<a1@customer.example.com>: 550 5.1.1 User unknown\r\n"))
	if rows := ExtractBounces(plain, bounceKindFailed); len(rows) != 1 || rows[0].Recipient != "a1@customer.example.com" {
		t.Errorf("no delivery-status part: %+v", rows)
	}
}

// TestGroupMailboxPerRecipient: a notice that reports several failed
// recipients is recorded with a bounces row per failed recipient, each
// filed into the group of the cause of that recipient; every group counts the notice once
// and each of its recipients, lists them all, finds the notice among its
// mails, and the retention recounts every group of the notice.
func TestGroupMailboxPerRecipient(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	msg, _ := storeRaw(t, root, address, multiNotice("Undelivered Mail Returned to Sender", multiBlocks...), 1, time.Now().UTC())
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	db := mustOpenIndex(t, root, address)
	rows, err := models.ListBouncesByMessage(db, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("%d bounces rows, want 5", len(rows))
	}
	for i, r := range rows {
		if r.MessageID != msg.ID || r.RecipientIndex != i || r.GroupKey == "" {
			t.Errorf("row %d: %+v", i, r)
		}
	}
	userUnknown := GroupKey(categoryUserUnknown, "a1@customer.example.com", "")
	if rows[0].GroupKey != userUnknown {
		t.Errorf("first row group %s, want %s", rows[0].GroupKey, userUnknown)
	}
	wantGroups := map[string]string{
		"a1@customer.example.com":   GroupKey(categoryUserUnknown, "a1@customer.example.com", ""),
		"a2@customer.example.com":   GroupKey(categoryUserUnknown, "a2@customer.example.com", ""),
		"b1@other.example.net":      GroupKey(categoryMailboxFull, "b1@other.example.net", ""),
		"slow@slowmail.example.net": GroupKey(categoryDeliveryDelay, "slowmail.example.net", ""),
		"c1@third.example.net":      GroupKey(categoryUserUnknown, "c1@third.example.net", ""),
	}
	for _, r := range rows {
		if want := wantGroups[r.Recipient]; r.GroupKey != want {
			t.Errorf("%s filed into %s, want %s", r.Recipient, r.GroupKey, want)
		}
		g, err := models.GetGroup(db, r.GroupKey)
		if err != nil {
			t.Fatalf("group of %s: %v", r.Recipient, err)
		}
		if g.MessageCount != 1 || g.RecipientCount != 1 {
			t.Errorf("group of %s: %d messages, %d recipients; want 1 and 1", r.Recipient, g.MessageCount, g.RecipientCount)
		}
		stats, err := models.GroupStats(db, r.GroupKey)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(stats.Recipients, []string{r.Recipient}) {
			t.Errorf("group of %s lists %v", r.Recipient, stats.Recipients)
		}
		mails, _, err := models.ListMessages(db, models.MessageFilter{GroupKey: r.GroupKey})
		if err != nil {
			t.Fatal(err)
		}
		if len(mails) != 1 || mails[0].ID != msg.ID || mails[0].GroupKey != userUnknown {
			t.Errorf("mails of the group of %s: %+v", r.Recipient, mails)
		}
	}
	expired, err := models.ListMessagesOlderThan(db, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || len(expired[0].GroupKeys) != len(wantGroups) {
		t.Fatalf("expired %+v, want the notice with %d groups", expired, len(wantGroups))
	}
	db.Close()
	res, err := PruneMailbox(context.Background(), root, address, time.Nanosecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 || res.RemovedGroups != len(wantGroups) {
		t.Errorf("prune removed %d messages and %d groups, want 1 and %d", res.Removed, res.RemovedGroups, len(wantGroups))
	}
}

// TestGroupMailboxPerRecipientSameGroup: two failed recipients of one cause
// and one domain land in one group, which counts the notice once and both
// recipients; a second grouping of the notice (Reclassify) keeps that.
func TestGroupMailboxPerRecipientSameGroup(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	blocked := "550 5.7.1 Service unavailable; Client host [203.0.113.5] blocked using zen.spamhaus.org"
	storeRaw(t, root, address, multiNotice("Undelivered Mail Returned to Sender",
		dsnBlock{"x1@customer.example.com", "failed", "5.7.1", blocked},
		dsnBlock{"x2@customer.example.com", "failed", "5.7.1", blocked}), 1, time.Now().UTC())
	for _, full := range []bool{false, true} {
		if _, err := GroupMailbox(context.Background(), root, address, full, nil, nil); err != nil {
			t.Fatal(err)
		}
		db := mustOpenIndex(t, root, address)
		groups, err := models.ListGroups(db, models.GroupFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 1 || groups[0].Category != categoryIPBlocked || groups[0].MessageCount != 1 || groups[0].RecipientCount != 2 {
			t.Errorf("full=%v: groups %+v, want one ip_blocked group with 1 message and 2 recipients", full, groups)
		}
		if n := countRows(t, db, "bounces"); n != 2 {
			t.Errorf("full=%v: %d bounces rows, want 2", full, n)
		}
		db.Close()
	}
}

// TestRejudgeUnverifiedIncremental: a notice whose DNS lookup failure was
// not verified when it was grouped is verified again by every later
// grouping that may query (also an incremental one; not while the DNS check
// is off), and leaves the group it was in (which is removed once empty);
// once verified it is not grouped again.
func TestRejudgeUnverifiedIncremental(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	msg, _ := storeRaw(t, root, address, dnsNotice(bounceKindFailed, "4.4.3", "u@infoseek.jp", "mx1.example.jp", dnsTryAgainDiag),
		1, time.Now().UTC().Add(-30*24*time.Hour))
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	unverified := GroupKey(categoryDNSLookupFailure, "infoseek.jp", "")
	db := mustOpenIndex(t, root, address)
	if b, err := models.GetBounceByMessageID(db, msg.ID); err != nil || b.CategoryRule != "dns_unverified_temporary" || b.GroupKey != unverified {
		t.Fatalf("first grouping: %+v, %v", b, err)
	}
	db.Close()

	// The check still off (no verifier, or one with stored outcomes only):
	// not verified again, the notice stays where it is.
	var lines []string
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	storedOnly := &storedOnlyVerifier{}
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) },
		WithDNS(storedOnly, SendingSources{})); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(lines, func(s string) bool { return strings.HasPrefix(s, "verifying again") }) || storedOnly.calls != 0 {
		t.Errorf("verified again with the check off: progress %q, %d calls", lines, storedOnly.calls)
	}
	db = mustOpenIndex(t, root, address)
	if g, err := models.GetGroup(db, unverified); err != nil || g.MessageCount != 1 {
		t.Errorf("unverified group after the second grouping: %+v, %v", g, err)
	}
	db.Close()

	// The check on but inconclusive (no resolver answered): still
	// unverified, in the same group, and verified again by the next
	// grouping.
	silent := &fakeVerifier{}
	lines = nil
	for range 2 {
		if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) },
			WithDNS(silent, SendingSources{})); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Contains(lines, "verifying again 1 notice(s) whose DNS lookup failure was not verified") {
		t.Errorf("progress %q", lines)
	}
	if len(silent.calls) != 2 {
		t.Errorf("verifier calls while inconclusive %d, want one per grouping", len(silent.calls))
	}
	db = mustOpenIndex(t, root, address)
	if b, err := models.GetBounceByMessageID(db, msg.ID); err != nil || b.CategoryRule != "dns_unverified_temporary" || b.GroupKey != unverified {
		t.Errorf("after inconclusive verifications: %+v, %v", b, err)
	}
	db.Close()

	// The check on: verified (an old notice too), moved to the group of the
	// verdict, and the empty group is removed.
	fake := &fakeVerifier{outcome: DNSOutcomeExists, ok: true}
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, WithDNS(fake, SendingSources{})); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("verifier calls %d, want 1", len(fake.calls))
	}
	db = mustOpenIndex(t, root, address)
	b, err := models.GetBounceByMessageID(db, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolver := GroupKey(categoryDNSResolverFailure, "mx1.example.jp", "")
	if b.CategoryRule != "dns_resolver_failure" || b.GroupKey != resolver {
		t.Errorf("after verification: rule %s, group %s; want dns_resolver_failure, %s", b.CategoryRule, b.GroupKey, resolver)
	}
	if _, err := models.GetGroup(db, unverified); err == nil {
		t.Error("the empty unverified group was kept")
	}
	if g, err := models.GetGroup(db, resolver); err != nil || g.MessageCount != 1 {
		t.Errorf("resolver group: %+v, %v", g, err)
	}
	db.Close()

	// Verified notices are not grouped again.
	fake.calls = nil
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, WithDNS(fake, SendingSources{})); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("a verified notice was verified again: %d calls", len(fake.calls))
	}
}

// TestDelayedSendingSideReason: a delay notice whose reason is a problem of
// the sending side keeps the category of that problem; a plain delay, and
// a delay whose wording is about the recipient, is a delivery delay.
func TestDelayedSendingSideReason(t *testing.T) {
	for _, c := range []struct {
		name, status, diag, category string
	}{
		{"spf", "4.7.1", "451 4.7.1 <newsletter@example.jp>: SPF check failed: example.jp does not designate 203.0.113.5 as permitted sender", categoryAuthFailure},
		{"dkim", "4.7.1", "451 4.7.1 Message rejected: DKIM signature verification failed", categoryAuthFailure},
		{"tls", "4.7.5", "451 4.7.5 TLS is required, but was not offered by host mx.customer.example.com[198.51.100.25]", categoryServerConfig},
		{"size", "4.3.4", "452 4.3.4 Message size exceeds fixed maximum message size", categoryMessageTooLarge},
		{"content", "4.7.1", "451 4.7.1 Message content rejected as spam", categoryContentRejected},
		{"sender", "4.7.1", "450 4.7.1 <newsletter@example.jp>: Sender address rejected: Access denied", categorySenderBlocked},
		{"timeout", "4.4.1", "connect to mx.customer.example.com[198.51.100.25]:25: Connection timed out", categoryDeliveryDelay},
		{"recipient wording", "4.0.0", "450 4.0.0 <d1@customer.example.com>: Recipient address rejected: User unknown in local recipient table, try again", categoryDeliveryDelay},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &models.Bounce{Recipient: "d1@customer.example.com", RecipientDomain: "customer.example.com", StatusCode: c.status,
				Diagnostic: c.diag, Action: bounceKindDelayed, RemoteMTA: "mx.customer.example.com", RemoteIP: "198.51.100.25",
				ReportingMTA: "mx1.example.jp", OriginalFrom: "Newsletter <newsletter@example.jp>"}
			if got := Categorize(b, bounceKindDelayed); got.Category != c.category {
				t.Errorf("Categorize = %+v, want %s", got, c.category)
			}
		})
	}
}

// storedOnlyVerifier is a verifier with stored outcomes only (the DNS check
// off): it may not query, and finds nothing.
type storedOnlyVerifier struct{ calls int }

func (v *storedOnlyVerifier) Live() bool { return false }

func (v *storedOnlyVerifier) Verify(context.Context, DNSFailure, string, time.Time, bool) (DNSOutcome, bool) {
	v.calls++
	return "", false
}

// TestExtractBouncesBlockChoice: the first row is the block that reports
// the kind of the notice, judged as the classification judges a block (by
// its action, else by its status), else the first block that reports no
// success; it is never repeated, and a block without an action whose
// status is 4.x.x counts as a delay.
func TestExtractBouncesBlockChoice(t *testing.T) {
	type row struct{ recipient, kind string }
	for _, c := range []struct {
		name   string
		kind   string
		blocks []dsnBlock
		want   []row
	}{
		{"success first", bounceKindFailed, []dsnBlock{
			{"ok@partner.example.org", "delivered", "2.0.0", "250 2.0.0 Ok: queued"},
			{"f1@customer.example.com", "failed", "5.1.1", "550 5.1.1 User unknown"},
			{"f2@customer.example.com", "failed", "5.1.1", "550 5.1.1 User unknown"},
		}, []row{{"f1@customer.example.com", bounceKindFailed}, {"f2@customer.example.com", bounceKindFailed}}},
		{"failure by status after a delay", bounceKindFailed, []dsnBlock{
			{"slow@slowmail.example.net", "delayed", "4.4.1", "connect to mx.slowmail.example.net[203.0.113.7]:25: Connection timed out"},
			{"f1@customer.example.com", "", "5.1.1", "550 5.1.1 User unknown"},
		}, []row{{"f1@customer.example.com", bounceKindFailed}, {"slow@slowmail.example.net", bounceKindDelayed}}},
		{"delay by status", bounceKindFailed, []dsnBlock{
			{"f1@customer.example.com", "failed", "5.1.1", "550 5.1.1 User unknown"},
			{"slow@slowmail.example.net", "", "4.4.1", "connect to mx.slowmail.example.net[203.0.113.7]:25: Connection timed out"},
		}, []row{{"f1@customer.example.com", bounceKindFailed}, {"slow@slowmail.example.net", bounceKindDelayed}}},
		{"no block of the kind", bounceKindFailed, []dsnBlock{
			{"ok@partner.example.org", "delivered", "2.0.0", "250 2.0.0 Ok: queued"},
			{"x1@customer.example.com", "", "", "550 User unknown"},
		}, []row{{"x1@customer.example.com", bounceKindFailed}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			pm := ParseMessage(multiNotice("Undelivered Mail Returned to Sender", c.blocks...))
			rows := ExtractBounces(pm, c.kind, pm.FromAddress, "newsletter@example.jp")
			var got []row
			for _, r := range rows {
				got = append(got, row{r.Recipient, BounceKind(r, c.kind)})
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("rows %v, want %v", got, c.want)
			}
		})
	}
}

// TestGroupMailboxPerRecipientKind: each recipient block of a notice is
// categorized as the kind that block reports: a delay block of a failed
// notice whose wording is about the recipient is a delivery delay, not the
// failure that wording would mean in a failed block.
func TestGroupMailboxPerRecipientKind(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	msg, _ := storeRaw(t, root, address, multiNotice("Undelivered Mail Returned to Sender",
		dsnBlock{"a1@customer.example.com", "failed", "5.1.1", "550 5.1.1 <a1@customer.example.com>: Recipient address rejected: User unknown"},
		dsnBlock{"d1@other.example.net", "delayed", "4.0.0", "450 4.0.0 <d1@other.example.net>: Recipient address rejected: User unknown in local recipient table, try again"}),
		1, time.Now().UTC())
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	db := mustOpenIndex(t, root, address)
	defer db.Close()
	rows, err := models.ListBouncesByMessage(db, msg.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v, %v; want 2", rows, err)
	}
	want := []string{
		GroupKey(categoryUserUnknown, "a1@customer.example.com", ""),
		GroupKey(categoryDeliveryDelay, "other.example.net", ""),
	}
	for i, r := range rows {
		if r.GroupKey != want[i] {
			g, _ := models.GetGroup(db, r.GroupKey)
			t.Errorf("%s filed into %+v, want %s", r.Recipient, g, want[i])
		}
	}
}

// TestRejudgeLeavesSharedGroup: a notice verified again that leaves a group
// other notices stay in refreshes the counters of that group, which keeps
// the others.
func TestRejudgeLeavesSharedGroup(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	now := time.Now().UTC()
	stays, _ := storeRaw(t, root, address, dnsNotice(bounceKindFailed, "4.4.3", "u1@infoseek.jp", "mx1.example.jp", dnsTryAgainDiag), 1, now)
	leaves, _ := storeRaw(t, root, address, dnsNotice(bounceKindFailed, "4.4.3", "u2@infoseek.jp", "mx1.example.jp", dnsTryAgainDiag), 2, now)
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	unverified := GroupKey(categoryDNSLookupFailure, "infoseek.jp", "")
	db := mustOpenIndex(t, root, address)
	if g, err := models.GetGroup(db, unverified); err != nil || g.MessageCount != 2 || g.RecipientCount != 2 {
		t.Fatalf("unverified group after the first grouping: %+v, %v", g, err)
	}
	db.Close()

	// The notice grouped first stays unverified; the other one is verified
	// and leaves the group.
	fake := &fakeVerifier{}
	fake.probe = func() {
		if len(fake.calls) == 2 {
			fake.outcome, fake.ok = DNSOutcomeExists, true
		}
	}
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, WithDNS(fake, SendingSources{})); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 || fake.calls[0].failure.Name != "infoseek.jp" {
		t.Fatalf("verifier calls %+v, want 2", fake.calls)
	}
	db = mustOpenIndex(t, root, address)
	defer db.Close()
	first, err := models.GetBounceByMessageID(db, stays.ID)
	if err != nil || first.GroupKey != unverified {
		t.Fatalf("the notice still unverified: %+v, %v", first, err)
	}
	moved, err := models.GetBounceByMessageID(db, leaves.ID)
	if err != nil || moved.GroupKey == unverified {
		t.Fatalf("the verified notice: %+v, %v", moved, err)
	}
	g, err := models.GetGroup(db, unverified)
	if err != nil || g.MessageCount != 1 || g.RecipientCount != 1 {
		t.Errorf("unverified group after one notice left: %+v, %v; want 1 message, 1 recipient", g, err)
	}
}
