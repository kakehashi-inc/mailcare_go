package modules

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/dnscheck"
)

// TestSourceCacheHosts: a host name is looked up once for every mailbox
// whose SMTP host it is and for sending_source_hosts, and stored once per
// kind; the stored lookup is used without asking while at most an hour
// old, and stands in for a lookup that is not answered (answered then)
// while at most 14 days old; without such a row the lookup stays
// unanswered. A name is looked up once per run.
func TestSourceCacheHosts(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	addMailbox(t, db, key, "b@example.com", "mail.example.jp", false)
	if err := SaveSendingSourceHosts(db, []string{"mail.example.jp"}); err != nil {
		t.Fatal(err)
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string][]netip.Addr{"mail.example.jp": {netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("::ffff:192.0.2.26")}}
	calls := 0
	now := time.Now()
	newCache := func() *sourceCache {
		c := newSourceCache(db, []string{"mail.example.jp"}, mailboxes)
		c.now = func() time.Time { return now }
		c.lookupHost = func(_ context.Context, host string) ([]netip.Addr, bool) {
			calls++
			addrs, ok := answers[host]
			return addrs, ok
		}
		return c
	}
	want := []netip.Addr{netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("::ffff:192.0.2.26")}
	c := newCache()
	for i := 0; i < 2; i++ {
		if got, answered := c.ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, want) || !answered {
			t.Errorf("call %d: %v, %v", i, got, answered)
		}
	}
	if calls != 1 {
		t.Errorf("%d lookups in one run, want 1", calls)
	}
	rows, err := models.ListSendingSourceLookups(db)
	if err != nil || len(rows) != 2 || rows[0].Kind == rows[1].Kind {
		t.Fatalf("stored rows %+v (err %v), want one per kind", rows, err)
	}
	for _, row := range rows {
		if !reflect.DeepEqual(row.Addresses, []string{"192.0.2.25", "192.0.2.26"}) {
			t.Errorf("stored row %+v", row)
		}
	}
	stored := []netip.Addr{netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("192.0.2.26")}

	// Another run within the hour asks nothing; after it, it asks again.
	now = now.Add(59 * time.Minute)
	if got, answered := newCache().ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, stored) || !answered || calls != 1 {
		t.Errorf("59 minutes later: %v, %v, %d lookups", got, answered, calls)
	}
	now = now.Add(2 * time.Minute)
	if got, answered := newCache().ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, want) || !answered || calls != 2 {
		t.Errorf("61 minutes later: %v, %v, %d lookups", got, answered, calls)
	}

	// Not answered: the freshest row within 14 days stands in.
	delete(answers, "mail.example.jp")
	if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ?, addresses = '198.51.100.1' WHERE kind = 'smtp_host'`,
		now.UTC().Add(-13*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ?, addresses = '198.51.100.2' WHERE kind = 'global_host'`,
		now.UTC().Add(-12*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, answered := newCache().ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("198.51.100.2")}) || !answered {
		t.Errorf("fallback: %v, %v; want the freshest row, answered", got, answered)
	}
	// Every row older than 14 days: no address, and not answered.
	if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ?`, now.UTC().Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, answered := newCache().ResolveHost(context.Background(), "mail.example.jp"); got != nil || answered {
		t.Errorf("old rows: %v, %v; want nothing, not answered", got, answered)
	}
	if rows, _ := models.ListSendingSourceLookups(db); len(rows) != 2 {
		t.Errorf("a failed lookup changed the stored rows: %+v", rows)
	}
}

// TestSourceCacheHalfAnswered: a lookup answered for one family only
// (the other failed) neither replaces the stored row nor counts as
// answered: the row at most 14 days old stands in for it (answered), and
// without one the addresses found come back unanswered, and nothing is
// stored. A name answered without addresses (NXDOMAIN) is stored without
// addresses and answered.
func TestSourceCacheHalfAnswered(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	addMailbox(t, db, key, "b@example.jp", "gone.example.jp", true)
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	full := []netip.Addr{netip.MustParseAddr("192.0.2.25"), netip.MustParseAddr("2001:db8::25")}
	if _, err := models.ReplaceSendingSourceLookup(db, &models.SendingSourceLookup{Kind: models.SendingSourceKindSMTPHost, Name: "mail.example.jp",
		Addresses: []string{"192.0.2.25", "2001:db8::25"}, FetchedAt: time.Now().Add(-2 * time.Hour)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	half := map[string][]netip.Addr{"mail.example.jp": full[:1], "gone.example.jp": full[1:]}
	c := newSourceCache(db, nil, mailboxes)
	c.lookupHost = func(_ context.Context, host string) ([]netip.Addr, bool) { return half[host], false }
	if got, answered := c.ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, full) || !answered {
		t.Errorf("half answered, stored row: %v, %v; want the stored row, answered", got, answered)
	}
	if got, answered := c.ResolveHost(context.Background(), "gone.example.jp"); !reflect.DeepEqual(got, full[1:]) || answered {
		t.Errorf("half answered, no row: %v, %v; want the addresses found, not answered", got, answered)
	}
	row, err := models.GetSendingSourceLookup(db, models.SendingSourceKindSMTPHost, "mail.example.jp")
	if err != nil || !reflect.DeepEqual(row.Addresses, []string{"192.0.2.25", "2001:db8::25"}) || time.Since(row.FetchedAt) < time.Hour {
		t.Errorf("the stored row was replaced: %+v (err %v)", row, err)
	}
	if _, err := models.GetSendingSourceLookup(db, models.SendingSourceKindSMTPHost, "gone.example.jp"); err == nil {
		t.Error("a half answered lookup was stored")
	}

	c = newSourceCache(db, nil, mailboxes)
	c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) { return nil, true }
	if got, answered := c.ResolveHost(context.Background(), "gone.example.jp"); got != nil || !answered {
		t.Errorf("NXDOMAIN: %v, %v; want answered without addresses", got, answered)
	}
	if row, err := models.GetSendingSourceLookup(db, models.SendingSourceKindSMTPHost, "gone.example.jp"); err != nil || len(row.Addresses) != 0 {
		t.Errorf("NXDOMAIN stored as %+v (err %v), want a row without addresses", row, err)
	}
}

// TestSourceCacheFutureRows: a stored row dated after now (the clock
// was set back) is never fresh: the source is looked up again, and the row
// still stands in for a lookup that fails; an answered lookup replaces it.
// The same holds for an SPF record.
func TestSourceCacheFutureRows(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(30 * time.Minute)
	for _, row := range []*models.SendingSourceLookup{
		{Kind: models.SendingSourceKindSMTPHost, Name: "mail.example.jp", Addresses: []string{"198.51.100.1"}, FetchedAt: future},
		{Kind: models.SendingSourceKindSPF, Name: "example.jp", Addresses: []string{"198.51.100.0/24"}, FetchedAt: future},
	} {
		if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	var hostCalls, spfCalls int
	answer := false
	newCache := func() *sourceCache {
		c := newSourceCache(db, nil, mailboxes)
		c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) {
			hostCalls++
			if answer {
				return []netip.Addr{netip.MustParseAddr("192.0.2.25")}, true
			}
			return nil, false
		}
		c.lookupSPF = func(context.Context, string) dnscheck.SPFResult {
			spfCalls++
			if answer {
				return dnscheck.SPFResult{Prefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, Complete: true}
			}
			return dnscheck.SPFResult{}
		}
		return c
	}
	c := newCache()
	if got, answered := c.ResolveHost(context.Background(), "mail.example.jp"); hostCalls != 1 ||
		!reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("198.51.100.1")}) || !answered {
		t.Errorf("future host row: %v, %v, %d lookups; want looked up, the row standing in", got, answered, hostCalls)
	}
	if prefixes, _, complete := c.ResolveSPF(context.Background(), "example.jp"); spfCalls != 1 ||
		!reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}) || !complete {
		t.Errorf("future SPF row: %v, %v, %d evaluations; want evaluated, the row standing in", prefixes, complete, spfCalls)
	}
	answer = true
	c = newCache()
	if got, _ := c.ResolveHost(context.Background(), "mail.example.jp"); hostCalls != 2 || !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("192.0.2.25")}) {
		t.Errorf("future host row, answered: %v, %d lookups", got, hostCalls)
	}
	if prefixes, _, _ := c.ResolveSPF(context.Background(), "example.jp"); spfCalls != 2 ||
		!reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}) {
		t.Errorf("future SPF row, complete: %v, %d evaluations", prefixes, spfCalls)
	}
	rows, err := models.ListSendingSourceLookups(db)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v (err %v)", rows, err)
	}
	for _, row := range rows {
		if row.FetchedAt.After(time.Now()) {
			t.Errorf("the future row was not replaced: %+v", row)
		}
	}
}

// TestSourceCacheSPF: the SPF record of a domain shared by two mailboxes
// is evaluated once and stored once; the stored result is used without
// asking while at most an hour old; an incomplete evaluation uses it as
// complete while at most 14 days old and stays incomplete otherwise.
// Without an SPF lookup (the DNS check off) nothing is read or used.
func TestSourceCacheSPF(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "", true)
	addMailbox(t, db, key, "b@example.jp", "", true)
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	result := dnscheck.SPFResult{Prefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("2001:db8::25/128")},
		Hosts: []string{"mx1.example.jp"}, Complete: true}
	calls := 0
	now := time.Now()
	newCache := func() *sourceCache {
		c := newSourceCache(db, nil, mailboxes)
		c.now = func() time.Time { return now }
		c.lookupSPF = func(context.Context, string) dnscheck.SPFResult {
			calls++
			return result
		}
		return c
	}
	c := newCache()
	for i := 0; i < 2; i++ {
		prefixes, hosts, complete := c.ResolveSPF(context.Background(), "example.jp")
		if !complete || !reflect.DeepEqual(prefixes, result.Prefixes) || !reflect.DeepEqual(hosts, result.Hosts) {
			t.Errorf("complete: %v %v %v", prefixes, hosts, complete)
		}
	}
	rows, err := models.ListSendingSourceLookups(db)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Addresses, []string{"192.0.2.0/24", "2001:db8::25"}) ||
		!reflect.DeepEqual(rows[0].Hosts, []string{"mx1.example.jp"}) || calls != 1 {
		t.Fatalf("stored rows %+v (err %v), %d evaluations", rows, err, calls)
	}

	result = dnscheck.SPFResult{Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}} // partial
	now = now.Add(30 * time.Minute)
	if prefixes, _, complete := newCache().ResolveSPF(context.Background(), "example.jp"); !complete || len(prefixes) != 2 || calls != 1 {
		t.Errorf("within the hour: %v %v, %d evaluations", prefixes, complete, calls)
	}
	now = now.Add(time.Hour)
	prefixes, hosts, complete := newCache().ResolveSPF(context.Background(), "example.jp")
	if !complete || !reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("2001:db8::25/128")}) ||
		!reflect.DeepEqual(hosts, []string{"mx1.example.jp"}) || calls != 2 {
		t.Errorf("incomplete, fresh row: %v %v %v, %d evaluations", prefixes, hosts, complete, calls)
	}
	if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ?`, now.UTC().Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	prefixes, _, complete = newCache().ResolveSPF(context.Background(), "example.jp")
	if complete || !reflect.DeepEqual(prefixes, result.Prefixes) {
		t.Errorf("incomplete, old row: %v %v", prefixes, complete)
	}
	if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ?`, now.UTC()); err != nil {
		t.Fatal(err)
	}
	off := newSourceCache(db, nil, mailboxes)
	if prefixes, hosts, complete := off.ResolveSPF(context.Background(), "example.jp"); complete || prefixes != nil || hosts != nil {
		t.Errorf("without an SPF lookup: %v %v %v", prefixes, hosts, complete)
	}
}

// TestSourceCacheEmptyKeepsStoredRow: a complete lookup that found
// nothing (a host name answered without addresses, an SPF record listing
// nothing) does not replace a non-empty stored row at most 14 days old,
// whichever kind of the name holds it (one dated after now included): the
// row is used as it is, answered or complete, and no row of the name is
// written or renewed. A non-empty row older than 14 days, and an empty row,
// are replaced as usual.
func TestSourceCacheEmptyKeepsStoredRow(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	if err := SaveSendingSourceHosts(db, []string{"mail.example.jp"}); err != nil {
		t.Fatal(err)
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	newCache := func() *sourceCache {
		c := newSourceCache(db, []string{"mail.example.jp"}, mailboxes)
		c.now = func() time.Time { return now }
		c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) { return nil, true }
		c.lookupSPF = func(context.Context, string) dnscheck.SPFResult { return dnscheck.SPFResult{Complete: true} }
		return c
	}
	seed := func(kind, address string, age time.Duration) {
		t.Helper()
		row := &models.SendingSourceLookup{Kind: kind, Name: "mail.example.jp", FetchedAt: now.Add(-age)}
		if kind == models.SendingSourceKindSPF {
			row.Name = "example.jp"
		}
		if address != "" {
			row.Addresses = []string{address}
		}
		if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	rowOf := func(kind, name string) *models.SendingSourceLookup {
		t.Helper()
		row, err := models.GetSendingSourceLookup(db, kind, name)
		if err != nil {
			t.Fatalf("row %s %s: %v", kind, name, err)
		}
		return row
	}
	day := 24 * time.Hour

	// The SMTP host row lists an address, the row of the setting is empty and
	// older: the empty answer keeps both rows as they are and uses the
	// address.
	for _, age := range []time.Duration{2 * time.Hour, 13 * day, -30 * time.Minute} {
		seed(models.SendingSourceKindSMTPHost, "192.0.2.25", age)
		seed(models.SendingSourceKindGlobalHost, "", age+2*time.Hour)
		got, answered := newCache().ResolveHost(context.Background(), "mail.example.jp")
		if !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("192.0.2.25")}) || !answered {
			t.Errorf("host row %v old: %v, %v; want the stored address, answered", age, got, answered)
		}
		smtp, global := rowOf(models.SendingSourceKindSMTPHost, "mail.example.jp"), rowOf(models.SendingSourceKindGlobalHost, "mail.example.jp")
		if !smtp.FetchedAt.Equal(now.Add(-age)) || len(smtp.Addresses) != 1 || !global.FetchedAt.Equal(now.Add(-age-2*time.Hour)) {
			t.Errorf("host row %v old: rows changed: %+v, %+v", age, smtp, global)
		}
	}
	// Older than 14 days: replaced (every kind, empty and renewed).
	seed(models.SendingSourceKindSMTPHost, "192.0.2.25", 15*day)
	seed(models.SendingSourceKindGlobalHost, "", 16*day)
	if got, answered := newCache().ResolveHost(context.Background(), "mail.example.jp"); got != nil || !answered {
		t.Errorf("host row 15 days old: %v, %v; want answered without addresses", got, answered)
	}
	for _, kind := range []string{models.SendingSourceKindSMTPHost, models.SendingSourceKindGlobalHost} {
		if row := rowOf(kind, "mail.example.jp"); len(row.Addresses) != 0 || !row.FetchedAt.Equal(now) {
			t.Errorf("host row 15 days old, %s: %+v; want replaced", kind, row)
		}
	}
	// A stored empty row is replaced as usual.
	seed(models.SendingSourceKindSMTPHost, "", 2*time.Hour)
	seed(models.SendingSourceKindGlobalHost, "", 2*time.Hour)
	newCache().ResolveHost(context.Background(), "mail.example.jp")
	if row := rowOf(models.SendingSourceKindSMTPHost, "mail.example.jp"); !row.FetchedAt.Equal(now) {
		t.Errorf("empty host row: %+v; want renewed", row)
	}

	// The same for an SPF record that lists nothing.
	seed(models.SendingSourceKindSPF, "198.51.100.0/24", 13*day)
	prefixes, _, complete := newCache().ResolveSPF(context.Background(), "example.jp")
	if !complete || !reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}) {
		t.Errorf("SPF row 13 days old: %v, %v; want the stored prefix, complete", prefixes, complete)
	}
	if row := rowOf(models.SendingSourceKindSPF, "example.jp"); !row.FetchedAt.Equal(now.Add(-13 * day)) {
		t.Errorf("SPF row 13 days old: %+v; want untouched", row)
	}
	seed(models.SendingSourceKindSPF, "198.51.100.0/24", 15*day)
	if prefixes, _, complete := newCache().ResolveSPF(context.Background(), "example.jp"); !complete || prefixes != nil {
		t.Errorf("SPF row 15 days old: %v, %v; want complete without prefixes", prefixes, complete)
	}
	if row := rowOf(models.SendingSourceKindSPF, "example.jp"); len(row.Addresses) != 0 || !row.FetchedAt.Equal(now) {
		t.Errorf("SPF row 15 days old: %+v; want replaced", row)
	}
}

// TestSourceCachePrunesGoneSources: before the first lookup of a run the
// rows no current source refers to are deleted: an SMTP host no mailbox has
// any more (changed, cleared, or its mailbox deleted), a domain no mailbox
// address has, a host name that left sending_source_hosts. A name still used
// by another mailbox keeps its row.
func TestSourceCachePrunesGoneSources(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	a := addMailbox(t, db, key, "a@example.jp", "old.example.jp", true)
	addMailbox(t, db, key, "b@example.jp", "keep.example.jp", true)
	gone := addMailbox(t, db, key, "c@example.com", "shared.example.jp", true)
	addMailbox(t, db, key, "d@example.org", "shared.example.jp", true)
	for _, row := range []*models.SendingSourceLookup{
		{Kind: models.SendingSourceKindSMTPHost, Name: "old.example.jp"},
		{Kind: models.SendingSourceKindSMTPHost, Name: "keep.example.jp"},
		{Kind: models.SendingSourceKindSMTPHost, Name: "shared.example.jp"},
		{Kind: models.SendingSourceKindSPF, Name: "example.jp"},
		{Kind: models.SendingSourceKindSPF, Name: "example.com"},
		{Kind: models.SendingSourceKindSPF, Name: "example.org"},
		{Kind: models.SendingSourceKindGlobalHost, Name: "relay.example.jp"},
		{Kind: models.SendingSourceKindGlobalHost, Name: "removed.example.jp"},
	} {
		if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteMailbox(db, jm.mailsRoot, jm.agentRoot, gone, false); err != nil {
		t.Fatal(err)
	}
	in := MailboxInputFrom(a)
	in.SmtpHost = new(string) // cleared
	if err := UpdateMailbox(db, key, jm.mailsRoot, jm.agentRoot, a, in); err != nil {
		t.Fatal(err)
	}
	if err := SaveSendingSourceHosts(db, []string{"relay.example.jp"}); err != nil {
		t.Fatal(err)
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	c := newSourceCache(db, ResolveSendingSourceHosts(db), mailboxes)
	c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) { return nil, false }
	c.ResolveHost(context.Background(), "keep.example.jp")
	rows, err := models.ListSendingSourceLookups(db)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, row := range rows {
		left = append(left, row.Kind+" "+row.Name)
	}
	want := []string{"smtp_host keep.example.jp", "smtp_host shared.example.jp", "spf example.jp", "spf example.org", "global_host relay.example.jp"}
	if !reflect.DeepEqual(left, want) {
		t.Errorf("rows left %v, want %v", left, want)
	}
}

// TestSourceCachePruneReadsTheSourcesAgain: the rows no source refers to
// are found with the mailboxes and sending_source_hosts as they are at the
// first lookup, not as groupOptions saw them: a mailbox another job added
// in between (with its SMTP host and its domain stored by that job) keeps
// its rows, and so does a host name just added to the setting; a row of a
// source gone meanwhile is deleted.
func TestSourceCachePruneReadsTheSourcesAgain(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	gone := addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	addMailbox(t, db, key, "b@example.org", "old.example.org", true)
	installFakeLookups(t, map[string][]netip.Addr{"mail.example.jp": nil}, nil)
	b := recordGroupOptions(t)
	groupOptionsOf(t, jm, gone)
	if len(b.hostResolvers) != 1 {
		t.Fatalf("host resolvers %v", b.hostResolvers)
	}
	// Meanwhile another job adds a mailbox and stores its lookups, a host
	// name is added to the setting, and a mailbox is deleted.
	addMailbox(t, db, key, "c@example.com", "new.example.com", true)
	if err := SaveSendingSourceHosts(db, []string{"relay.example.jp"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteMailbox(db, jm.mailsRoot, jm.agentRoot, gone, false); err != nil {
		t.Fatal(err)
	}
	for _, row := range []*models.SendingSourceLookup{
		{Kind: models.SendingSourceKindSMTPHost, Name: "new.example.com"},
		{Kind: models.SendingSourceKindSPF, Name: "example.com"},
		{Kind: models.SendingSourceKindGlobalHost, Name: "relay.example.jp"},
		{Kind: models.SendingSourceKindSMTPHost, Name: "old.example.org"},
		{Kind: models.SendingSourceKindSMTPHost, Name: "mail.example.jp"},
		{Kind: models.SendingSourceKindSPF, Name: "example.jp"},
	} {
		if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	b.hostResolvers[0].ResolveHost(context.Background(), "old.example.org")
	rows, err := models.ListSendingSourceLookups(db)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, row := range rows {
		left = append(left, row.Kind+" "+row.Name)
	}
	want := []string{"smtp_host new.example.com", "spf example.com", "global_host relay.example.jp", "smtp_host old.example.org"}
	if !reflect.DeepEqual(left, want) {
		t.Errorf("rows left %v, want %v", left, want)
	}
}

// TestSourceSPFDomains: the distinct domains of the mailboxes in mailbox
// order, at most MaxSPFDomains, without a domain that is no host name.
func TestSourceSPFDomains(t *testing.T) {
	var mailboxes []*models.Mailbox
	for i := range MaxSPFDomains + 5 {
		mailboxes = append(mailboxes, &models.Mailbox{Address: fmt.Sprintf("a@d%02d.example.jp", i)},
			&models.Mailbox{Address: fmt.Sprintf("b@d%02d.example.jp", i)})
	}
	mailboxes = append([]*models.Mailbox{{Address: "x@localhost"}, {Address: "y@[192.0.2.1]"}}, mailboxes...)
	got := sourceSPFDomains(mailboxes)
	if len(got) != MaxSPFDomains || got[0] != "d00.example.jp" || got[MaxSPFDomains-1] != fmt.Sprintf("d%02d.example.jp", MaxSPFDomains-1) {
		t.Errorf("domains %v", got)
	}
	if got := sourceSPFDomains(nil); len(got) != 0 {
		t.Errorf("no mailbox: %v", got)
	}
}

// TestPruneDNSChecksRemovesOldSendingSourceLookups: the daily cleanup deletes
// the stored lookups older than 90 days and those dated more than a day
// after now (the clock was set back).
func TestPruneDNSChecksRemovesOldSendingSourceLookups(t *testing.T) {
	db := newTestDB(t)
	jm, _ := newTestJobManager(t, db)
	for _, row := range []*models.SendingSourceLookup{
		{Kind: models.SendingSourceKindGlobalHost, Name: "old.example.jp", FetchedAt: time.Now().Add(-91 * 24 * time.Hour)},
		{Kind: models.SendingSourceKindGlobalHost, Name: "new.example.jp", FetchedAt: time.Now().Add(-89 * 24 * time.Hour)},
		{Kind: models.SendingSourceKindGlobalHost, Name: "future.example.jp", FetchedAt: time.Now().Add(25 * time.Hour)},
		{Kind: models.SendingSourceKindGlobalHost, Name: "soon.example.jp", FetchedAt: time.Now().Add(23 * time.Hour)},
	} {
		if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	var lines []string
	if err := jm.pruneDNSChecks(func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	rows, err := models.ListSendingSourceLookups(db)
	if err != nil || len(rows) != 2 || rows[0].Name != "new.example.jp" || rows[1].Name != "soon.example.jp" {
		t.Errorf("rows after the cleanup %+v (err %v)", rows, err)
	}
	want := []string{"removed 1 stored sending source lookup(s) older than 90 days",
		"removed 1 stored sending source lookup(s) dated more than 24 hours in the future"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("progress %q, want %q", lines, want)
	}
}

// TestPruneDNSChecksGoesOnAfterAFailure: when the DNS check results cannot
// be pruned (the table is unavailable here), the stored lookups of the
// sending sources are still pruned, and the failure is returned.
func TestPruneDNSChecksGoesOnAfterAFailure(t *testing.T) {
	db := newTestDB(t)
	jm, _ := newTestJobManager(t, db)
	if _, err := models.ReplaceSendingSourceLookup(db, &models.SendingSourceLookup{Kind: models.SendingSourceKindGlobalHost, Name: "old.example.jp",
		FetchedAt: time.Now().Add(-91 * 24 * time.Hour)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE dns_checks RENAME TO dns_checks_unavailable`); err != nil {
		t.Fatal(err)
	}
	var lines []string
	err := jm.pruneDNSChecks(func(s string) { lines = append(lines, s) })
	if err == nil || !strings.Contains(err.Error(), "dns checks") {
		t.Errorf("error %v, want the failure of the dns checks", err)
	}
	if rows, err := models.ListSendingSourceLookups(db); err != nil || len(rows) != 0 {
		t.Errorf("rows after the cleanup %+v (err %v): the sending source lookups were not pruned", rows, err)
	}
	if !slices.Contains(lines, "removed 1 stored sending source lookup(s) older than 90 days") {
		t.Errorf("progress %q", lines)
	}
}

// TestSourceCacheTooWideRows: a stored SPF result that takes in more
// address space than an evaluation may count as sending sources (written before the
// limits applied; here sixteen /12 and one more) is never used: not
// without asking while fresh, not in place of an incomplete evaluation,
// and not kept against a complete one that found nothing, which replaces
// it.
func TestSourceCacheTooWideRows(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "", true)
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	var wide []string
	for i := range 16 {
		wide = append(wide, fmt.Sprintf("10.%d.0.0/12", i*16))
	}
	wide = append(wide, "11.0.0.0/12")
	seed := func(age time.Duration) {
		t.Helper()
		if _, err := models.ReplaceSendingSourceLookup(db, &models.SendingSourceLookup{Kind: models.SendingSourceKindSPF, Name: "example.jp",
			Addresses: wide, FetchedAt: time.Now().Add(-age)}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	result := dnscheck.SPFResult{}
	calls := 0
	newCache := func() *sourceCache {
		c := newSourceCache(db, nil, mailboxes)
		c.lookupSPF = func(context.Context, string) dnscheck.SPFResult {
			calls++
			return result
		}
		return c
	}
	seed(10 * time.Minute)
	if prefixes, _, complete := newCache().ResolveSPF(context.Background(), "example.jp"); complete || len(prefixes) != 0 || calls != 1 {
		t.Errorf("fresh too wide row, incomplete evaluation: %v, %v, %d evaluations; want evaluated and incomplete", prefixes, complete, calls)
	}
	result = dnscheck.SPFResult{Complete: true}
	if prefixes, _, complete := newCache().ResolveSPF(context.Background(), "example.jp"); !complete || len(prefixes) != 0 {
		t.Errorf("too wide row, empty evaluation: %v, %v; want complete without prefixes", prefixes, complete)
	}
	if row, err := models.GetSendingSourceLookup(db, models.SendingSourceKindSPF, "example.jp"); err != nil || len(row.Addresses) != 0 {
		t.Errorf("the too wide row was kept: %+v (err %v)", row, err)
	}
	if !dnscheck.SPFTooWide(parseStoredPrefixes(wide)) || dnscheck.SPFTooWide(parseStoredPrefixes(wide[:16])) {
		t.Error("the seeded prefixes are not just over the limit")
	}
}

// TestSourceCacheConcurrentHosts: the grouping looks several source host
// names up at once; the cache gives each its answer and stores each
// once (run with -race).
func TestSourceCacheConcurrentHosts(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	var hosts []string
	for i := range 8 {
		host := fmt.Sprintf("mx%d.example.jp", i)
		hosts = append(hosts, host)
		addMailbox(t, db, key, fmt.Sprintf("m%d@example.jp", i), host, true)
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	c := newSourceCache(db, nil, mailboxes)
	c.lookupHost = func(_ context.Context, host string) ([]netip.Addr, bool) {
		var n int
		fmt.Sscanf(host, "mx%d.", &n)
		return []netip.Addr{netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", n+1))}, true
	}
	got := make([][]netip.Addr, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Go(func() { got[i], _ = c.ResolveHost(context.Background(), host) })
	}
	wg.Wait()
	for i := range hosts {
		if want := []netip.Addr{netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i+1))}; !reflect.DeepEqual(got[i], want) {
			t.Errorf("%s: %v, want %v", hosts[i], got[i], want)
		}
	}
	if rows := sendingSourceRows(t, db); len(rows) != len(hosts) {
		t.Errorf("%d stored rows, want %d", len(rows), len(hosts))
	}
}

// TestSourceCacheConcurrentRuns races the caches of several runs (jobs
// of different mailboxes sharing an SMTP host) storing the same host name,
// half of them with an answer without addresses (run with -race): whatever
// the order, the stored rows keep the address, since the statement itself
// refuses to replace a recent non-empty row with an empty one, and a run
// whose empty answer was refused uses the stored address.
func TestSourceCacheConcurrentRuns(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	if err := SaveSendingSourceHosts(db, []string{"mail.example.jp"}); err != nil {
		t.Fatal(err)
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("192.0.2.25")}
	for round := range 5 {
		if _, err := db.Exec(`DELETE FROM sending_source_lookups`); err != nil {
			t.Fatal(err)
		}
		const runs = 8
		got := make([][]netip.Addr, runs)
		var wg, looking sync.WaitGroup
		looking.Add(runs)
		start := make(chan struct{})
		for i := range runs {
			c := newSourceCache(db, []string{"mail.example.jp"}, mailboxes)
			c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) {
				// Every run looks the name up (none finds a fresh row)
				// before any of them stores its answer.
				looking.Done()
				looking.Wait()
				if i%2 == 0 {
					return want, true
				}
				return nil, true
			}
			wg.Go(func() {
				<-start
				got[i], _ = c.ResolveHost(context.Background(), "mail.example.jp")
			})
		}
		close(start)
		wg.Wait()
		for i := range runs {
			if i%2 == 0 && !reflect.DeepEqual(got[i], want) {
				t.Errorf("round %d, run %d (answered with the address): %v", round, i, got[i])
			}
		}
		for _, kind := range []string{models.SendingSourceKindSMTPHost, models.SendingSourceKindGlobalHost} {
			row, err := models.GetSendingSourceLookup(db, kind, "mail.example.jp")
			if err != nil || !reflect.DeepEqual(row.Addresses, []string{"192.0.2.25"}) {
				t.Errorf("round %d, %s row: %+v (err %v), want the address", round, kind, row, err)
			}
		}
	}
}

// TestSourceCacheFreshBoundary: a stored lookup exactly an hour old is
// still used without asking, for a host name and for an SPF record.
func TestSourceCacheFreshBoundary(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	for _, row := range []*models.SendingSourceLookup{
		{Kind: models.SendingSourceKindSMTPHost, Name: "mail.example.jp", Addresses: []string{"192.0.2.25"}, FetchedAt: now.Add(-SourceCacheFreshAge)},
		{Kind: models.SendingSourceKindSPF, Name: "example.jp", Addresses: []string{"192.0.2.0/24"}, FetchedAt: now.Add(-SourceCacheFreshAge)},
	} {
		if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	c := newSourceCache(db, nil, mailboxes)
	c.now = func() time.Time { return now }
	c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) {
		calls++
		return []netip.Addr{netip.MustParseAddr("198.51.100.1")}, true
	}
	c.lookupSPF = func(context.Context, string) dnscheck.SPFResult {
		calls++
		return dnscheck.SPFResult{Prefixes: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}, Complete: true}
	}
	if got, answered := c.ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("192.0.2.25")}) || !answered {
		t.Errorf("host row an hour old: %v, %v; want the stored address", got, answered)
	}
	if prefixes, _, complete := c.ResolveSPF(context.Background(), "example.jp"); !reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}) || !complete {
		t.Errorf("SPF row an hour old: %v, %v; want the stored prefix", prefixes, complete)
	}
	if calls != 0 {
		t.Errorf("%d lookups, want none", calls)
	}
}

// TestSourceCacheFreshestKind: when a host name is both the SMTP host of a
// mailbox and in sending_source_hosts, the fresher of its two rows stands
// in, whichever kind holds it: for a lookup that is not answered and for
// one answered without addresses.
func TestSourceCacheFreshestKind(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	if err := SaveSendingSourceHosts(db, []string{"mail.example.jp"}); err != nil {
		t.Fatal(err)
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	day := 24 * time.Hour
	for _, fresher := range []string{models.SendingSourceKindSMTPHost, models.SendingSourceKindGlobalHost} {
		for _, kind := range []string{models.SendingSourceKindSMTPHost, models.SendingSourceKindGlobalHost} {
			row := &models.SendingSourceLookup{Kind: kind, Name: "mail.example.jp", Addresses: []string{"198.51.100.1"}, FetchedAt: now.Add(-3 * day)}
			if kind == fresher {
				row.Addresses, row.FetchedAt = []string{"192.0.2.25"}, now.Add(-2*day)
			}
			if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
				t.Fatal(err)
			}
		}
		for name, answered := range map[string]bool{"not answered": false, "answered without addresses": true} {
			c := newSourceCache(db, []string{"mail.example.jp"}, mailboxes)
			c.now = func() time.Time { return now }
			c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) { return nil, answered }
			if got, ok := c.ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("192.0.2.25")}) || !ok {
				t.Errorf("%s, %s fresher: %v, %v; want the fresher row", name, fresher, got, ok)
			}
		}
	}
}

// TestSourceCacheSPFHostsOnly: an SPF result that lists host names only is
// no empty result: stored, it is kept against a later evaluation that lists
// nothing, and found, it replaces a stored row that lists prefixes.
func TestSourceCacheSPFHostsOnly(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "", true)
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Second)
	seed := func(row *models.SendingSourceLookup) {
		t.Helper()
		row.Kind, row.Name, row.FetchedAt = models.SendingSourceKindSPF, "example.jp", now.Add(-2*time.Hour)
		if _, err := models.ReplaceSendingSourceLookup(db, row, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(result dnscheck.SPFResult) ([]netip.Prefix, []string, bool) {
		c := newSourceCache(db, nil, mailboxes)
		c.now = func() time.Time { return now }
		c.lookupSPF = func(context.Context, string) dnscheck.SPFResult { return result }
		return c.ResolveSPF(context.Background(), "example.jp")
	}

	seed(&models.SendingSourceLookup{Hosts: []string{"mx1.example.jp"}})
	if prefixes, hosts, complete := resolve(dnscheck.SPFResult{Complete: true}); prefixes != nil || !reflect.DeepEqual(hosts, []string{"mx1.example.jp"}) || !complete {
		t.Errorf("empty result over a row of host names: %v %v %v; want the stored host names", prefixes, hosts, complete)
	}

	seed(&models.SendingSourceLookup{Addresses: []string{"192.0.2.0/24"}})
	prefixes, hosts, complete := resolve(dnscheck.SPFResult{Hosts: []string{"mx2.example.jp"}, Complete: true})
	if prefixes != nil || !reflect.DeepEqual(hosts, []string{"mx2.example.jp"}) || !complete {
		t.Errorf("host names only over a row of prefixes: %v %v %v; want the new host names", prefixes, hosts, complete)
	}
	if row, err := models.GetSendingSourceLookup(db, models.SendingSourceKindSPF, "example.jp"); err != nil || row.Addresses != nil ||
		!reflect.DeepEqual(row.Hosts, []string{"mx2.example.jp"}) || !row.FetchedAt.Equal(now) {
		t.Errorf("host names only over a row of prefixes: stored %+v (err %v); want replaced", row, err)
	}
}

// TestSourceCacheCanceledNotRemembered: a lookup cut off by the end of ctx
// is not remembered for the run: the next call looks the source up again.
func TestSourceCacheCanceledNotRemembered(t *testing.T) {
	db := newTestDB(t)
	_, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@example.jp", "mail.example.jp", true)
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var hostCalls, spfCalls int
	c := newSourceCache(db, nil, mailboxes)
	c.lookupHost = func(context.Context, string) ([]netip.Addr, bool) {
		hostCalls++
		if hostCalls == 1 {
			cancel()
			return nil, false
		}
		return []netip.Addr{netip.MustParseAddr("192.0.2.25")}, true
	}
	c.lookupSPF = func(context.Context, string) dnscheck.SPFResult {
		spfCalls++
		if spfCalls == 1 {
			return dnscheck.SPFResult{}
		}
		return dnscheck.SPFResult{Prefixes: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, Complete: true}
	}
	if _, answered := c.ResolveHost(ctx, "mail.example.jp"); answered {
		t.Error("canceled host lookup answered")
	}
	if _, _, complete := c.ResolveSPF(ctx, "example.jp"); complete {
		t.Error("canceled SPF evaluation complete")
	}
	if got, answered := c.ResolveHost(context.Background(), "mail.example.jp"); !reflect.DeepEqual(got, []netip.Addr{netip.MustParseAddr("192.0.2.25")}) || !answered || hostCalls != 2 {
		t.Errorf("after the cancellation: %v, %v, %d lookups; want looked up again", got, answered, hostCalls)
	}
	if prefixes, _, complete := c.ResolveSPF(context.Background(), "example.jp"); !reflect.DeepEqual(prefixes, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}) || !complete || spfCalls != 2 {
		t.Errorf("after the cancellation: %v, %v, %d evaluations; want evaluated again", prefixes, complete, spfCalls)
	}
}
