package modules

import (
	"context"
	"database/sql"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/dnscheck"
	"mailcare/app/modules/mailengine"
)

// builtOptions is what groupOptions passed to the constructors of its
// options (recordGroupOptions).
type builtOptions struct {
	sources       []mailengine.SendingSources // WithSendingSources
	checkers      []dnscheck.Config           // dnscheck.New
	dnsSources    []mailengine.SendingSources // WithDNS
	verifierOK    bool                        // WithDNS got the Checker dnscheck.New made
	hostResolvers []mailengine.SourceHostResolver
	spfResolvers  []mailengine.SourceSPFResolver
	spfDomains    [][]string
	systemLookups int // dnscheck.NewSystemHostResolver
	checkerUsed   int // the lookups of the Checker given to the cache
}

// recordGroupOptions replaces the constructors groupOptions uses with ones
// that record their arguments, for the rest of the test.
func recordGroupOptions(t *testing.T) *builtOptions {
	t.Helper()
	b := &builtOptions{}
	var made *dnscheck.Checker
	origChecker, origDNS, origSources := newDNSChecker, withDNS, withSendingSources
	origHost, origSPF, origSystem, origLookups := withSourceHostResolver, withSourceSPF, newSystemHostLookup, checkerLookups
	t.Cleanup(func() {
		newDNSChecker, withDNS, withSendingSources = origChecker, origDNS, origSources
		withSourceHostResolver, withSourceSPF, newSystemHostLookup, checkerLookups = origHost, origSPF, origSystem, origLookups
	})
	newDNSChecker = func(db *sql.DB, cfg dnscheck.Config) *dnscheck.Checker {
		b.checkers = append(b.checkers, cfg)
		made = origChecker(db, cfg)
		return made
	}
	withDNS = func(v mailengine.DNSVerifier, sources mailengine.SendingSources) mailengine.GroupOption {
		b.dnsSources = append(b.dnsSources, sources)
		c, ok := v.(*dnscheck.Checker)
		b.verifierOK = ok && c != nil && c == made
		return origDNS(v, sources)
	}
	withSendingSources = func(sources mailengine.SendingSources) mailengine.GroupOption {
		b.sources = append(b.sources, sources)
		return origSources(sources)
	}
	withSourceHostResolver = func(r mailengine.SourceHostResolver) mailengine.GroupOption {
		b.hostResolvers = append(b.hostResolvers, r)
		return origHost(r)
	}
	withSourceSPF = func(r mailengine.SourceSPFResolver, domains []string) mailengine.GroupOption {
		b.spfResolvers, b.spfDomains = append(b.spfResolvers, r), append(b.spfDomains, domains)
		return origSPF(r, domains)
	}
	newSystemHostLookup = func() hostLookupFunc {
		b.systemLookups++
		return origSystem()
	}
	checkerLookups = func(c *dnscheck.Checker) (hostLookupFunc, spfLookupFunc) {
		if c == made {
			b.checkerUsed++
		}
		return origLookups(c)
	}
	return b
}

// fakeLookups stands in for the network of the sending source lookups of every
// run of the test: the OS resolver of the host names (DNS check off) and
// the host and SPF lookups of the Checker (DNS check on). A host name
// missing from hosts is not answered; a domain missing from spf is
// incomplete.
type fakeLookups struct {
	mu        sync.Mutex
	hosts     map[string][]netip.Addr
	spf       map[string]dnscheck.SPFResult
	hostCalls []string
	spfCalls  []string
}

func installFakeLookups(t *testing.T, hosts map[string][]netip.Addr, spf map[string]dnscheck.SPFResult) *fakeLookups {
	t.Helper()
	f := &fakeLookups{hosts: hosts, spf: spf}
	origSystem, origLookups := newSystemHostLookup, checkerLookups
	t.Cleanup(func() { newSystemHostLookup, checkerLookups = origSystem, origLookups })
	newSystemHostLookup = func() hostLookupFunc { return f.lookupHost }
	checkerLookups = func(*dnscheck.Checker) (hostLookupFunc, spfLookupFunc) { return f.lookupHost, f.lookupSPF }
	return f
}

func (f *fakeLookups) lookupHost(_ context.Context, host string) ([]netip.Addr, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostCalls = append(f.hostCalls, host)
	addrs, ok := f.hosts[host]
	return addrs, ok
}

func (f *fakeLookups) lookupSPF(_ context.Context, domain string) dnscheck.SPFResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spfCalls = append(f.spfCalls, domain)
	return f.spf[domain]
}

// addMailbox registers a mailbox whose SMTP host is host ("" = none).
func addMailbox(t *testing.T, db *sql.DB, key []byte, address, host string, enabled bool) *models.Mailbox {
	t.Helper()
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: address, ImapHost: "imap.example.test",
		ImapUsername: "u", ImapPassword: "p", Enabled: &enabled, SmtpHost: &host})
	if err != nil {
		t.Fatalf("create mailbox %s: %v", address, err)
	}
	return mb
}

// groupOptionsOf calls groupOptions for a run of mb and fails the test on
// an error.
func groupOptionsOf(t *testing.T, jm *JobManager, mb *models.Mailbox) []mailengine.GroupOption {
	t.Helper()
	opts, err := jm.groupOptions(mb)
	if err != nil {
		t.Fatalf("groupOptions: %v", err)
	}
	return opts
}

// The sending sources are always given (the backscatter rule depends on
// them alone), with the lookups of their host names; the SPF records and
// the live verification only while the check is on, with the saved
// resolvers, the system-only switch and the same sending sources, all
// through the Checker. With the check off the host names are looked up
// through the OS resolver alone, and the verification is a StoredOnly
// Checker (the stored outcomes, no query).
func TestGroupOptionsFollowTheCheckSetting(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	// The default is on (no network use).
	first := addMailbox(t, db, key, "first@example.net", "", true)
	b := recordGroupOptions(t)
	if got := groupOptionsOf(t, jm, first); len(got) != 4 {
		t.Fatalf("default options = %d, want 4 (sending sources, host lookups, SPF and the verification)", len(got))
	}
	empty := mailengine.NewSendingSources(nil, nil, nil, nil).WithOtherHosts(nil)
	if len(b.checkers) != 1 || !reflect.DeepEqual(b.checkers[0], dnscheck.Config{Enabled: true, Resolvers: []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}}) {
		t.Errorf("default checker config %+v", b.checkers)
	}
	if len(b.sources) != 1 || len(b.dnsSources) != 1 || !reflect.DeepEqual(b.sources[0], empty) || !reflect.DeepEqual(b.dnsSources[0], empty) || !b.verifierOK {
		t.Errorf("default: sources %+v, verification sources %+v, verifier %v", b.sources, b.dnsSources, b.verifierOK)
	}
	if len(b.hostResolvers) != 1 || len(b.spfResolvers) != 1 || any(b.hostResolvers[0]) != any(b.spfResolvers[0]) ||
		!reflect.DeepEqual(b.spfDomains[0], []string{"example.net"}) || b.checkerUsed != 1 || b.systemLookups != 0 {
		t.Errorf("default lookups: host %v, spf %v %v, checker used %d, system %d", b.hostResolvers, b.spfResolvers, b.spfDomains,
			b.checkerUsed, b.systemLookups)
	}

	// Saved resolvers, the OS resolver only and sending sources: the saved
	// host names and the SMTP host of the mailbox of the run configure the
	// run, the SMTP hosts of the other mailboxes only match.
	resolvers := []string{"192.0.2.53", "2001:db8::53"}
	on := true
	if err := SaveDNSCheck(db, nil, &resolvers, &on); err != nil {
		t.Fatal(err)
	}
	if err := SaveSendingSourceIPv4(db, []string{"192.0.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSendingSourceIPv6(db, []string{"2001:db8::/32"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSendingSourceHosts(db, []string{"relay.example.jp"}); err != nil {
		t.Fatal(err)
	}
	ops := addMailbox(t, db, key, "ops@example.jp", "mx1.example.jp", true)
	info := addMailbox(t, db, key, "info@example.com", "", false)
	b = recordGroupOptions(t)
	if got := groupOptionsOf(t, jm, ops); len(got) != 4 {
		t.Fatalf("configured options = %d, want 4", len(got))
	}
	v4, v6 := []string{"192.0.2.0/24"}, []string{"2001:db8::/32"}
	want := mailengine.NewSendingSources(v4, v6, []string{"relay.example.jp", "mx1.example.jp"}, nil).WithOtherHosts([]string{"mx1.example.jp"})
	if len(b.checkers) != 1 || !reflect.DeepEqual(b.checkers[0], dnscheck.Config{Enabled: true, Resolvers: resolvers, SystemOnly: true}) {
		t.Errorf("configured checker config %+v", b.checkers)
	}
	if len(b.sources) != 1 || len(b.dnsSources) != 1 || !reflect.DeepEqual(b.sources[0], want) || !reflect.DeepEqual(b.dnsSources[0], want) || !b.verifierOK {
		t.Errorf("configured: sources %+v, verification sources %+v (want %+v), verifier %v", b.sources, b.dnsSources, want, b.verifierOK)
	}
	if len(b.spfDomains) != 1 || !reflect.DeepEqual(b.spfDomains[0], []string{"example.com", "example.jp", "example.net"}) || b.checkerUsed != 1 {
		t.Errorf("configured SPF domains %v (checker used %d), want the domains of the mailboxes in mailbox order", b.spfDomains, b.checkerUsed)
	}
	b = recordGroupOptions(t)
	groupOptionsOf(t, jm, info)
	wantInfo := mailengine.NewSendingSources(v4, v6, []string{"relay.example.jp"}, nil).WithOtherHosts([]string{"mx1.example.jp"})
	if len(b.sources) != 1 || !reflect.DeepEqual(b.sources[0], wantInfo) || !reflect.DeepEqual(b.dnsSources[0], wantInfo) {
		t.Errorf("another mailbox: sources %+v, want %+v", b.sources, wantInfo)
	}

	off := false
	if err := SaveDNSCheck(db, &off, nil, nil); err != nil {
		t.Fatal(err)
	}
	b = recordGroupOptions(t)
	if got := groupOptionsOf(t, jm, ops); len(got) != 3 {
		t.Errorf("options with the check off = %d, want 3 (sending sources, the host lookups and the stored outcomes)", len(got))
	}
	if len(b.checkers) != 1 || !reflect.DeepEqual(b.checkers[0], dnscheck.Config{StoredOnly: true}) || !b.verifierOK ||
		len(b.dnsSources) != 1 || !reflect.DeepEqual(b.dnsSources[0], want) || len(b.sources) != 1 || !reflect.DeepEqual(b.sources[0], want) {
		t.Errorf("check off: checkers %+v (verifier %v), verification sources %+v, sources %+v", b.checkers, b.verifierOK, b.dnsSources, b.sources)
	}
	if len(b.hostResolvers) != 1 || len(b.spfResolvers) != 0 || b.systemLookups != 1 || b.checkerUsed != 0 {
		t.Errorf("check off: host lookups %v, SPF %v, OS resolver %d, checker %d", b.hostResolvers, b.spfResolvers, b.systemLookups, b.checkerUsed)
	}
}

// The options resolve nothing, not even the source host names: the grouping
// resolves them when a message needs them. A resolver that never answers
// would otherwise hold the call for the control query's timeout.
func TestGroupOptionsResolveNothing(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	resolvers := []string{"192.0.2.53"} // documentation address: never answers
	if err := SaveDNSCheck(db, nil, &resolvers, nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveSendingSourceHosts(db, []string{"mail.example.jp"}); err != nil {
		t.Fatal(err)
	}
	mb := addMailbox(t, db, key, "b@example.jp", "mailhost", true)
	b := recordGroupOptions(t)
	start := time.Now()
	if got := groupOptionsOf(t, jm, mb); len(got) != 4 {
		t.Fatalf("options = %d, want 4", len(got))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("groupOptions took %v: it resolved something", elapsed)
	}
	// The source host names reach both options unresolved.
	want := mailengine.NewSendingSources(nil, nil, []string{"mail.example.jp", "mailhost"}, nil).WithOtherHosts([]string{"mailhost"})
	if len(b.sources) != 1 || len(b.dnsSources) != 1 || !reflect.DeepEqual(b.sources[0], want) || !reflect.DeepEqual(b.dnsSources[0], want) {
		t.Errorf("sources %+v, verification sources %+v; want %+v", b.sources, b.dnsSources, want)
	}
	if len(b.checkers) != 1 || !reflect.DeepEqual(b.checkers[0].Resolvers, resolvers) || b.checkers[0].SystemOnly {
		t.Errorf("checker config %+v", b.checkers)
	}
	if rows, err := models.ListSendingSourceLookups(db); err != nil || len(rows) != 0 {
		t.Errorf("stored lookups %+v (err %v): nothing was looked up", rows, err)
	}
}

// The host names of the sending sources are the saved sending_source_hosts
// first, then the SMTP hosts of every mailbox, enabled or not, in the order
// of the mailbox list (domain, then address), without repeats; a host that
// is no valid host name any more is skipped. Every one of them matches in
// the run of any mailbox, but the sources are configured for a mailbox
// only by the saved lists and the SMTP host of the mailbox: a mailbox
// without one (or with an invalid one) keeps the empty configuration, in
// which the DNS lookup failure judgement (4.4) takes every reporting MTA
// for a sending server, until
// sending_source_hosts is set.
func TestSendingSourceHostsUnion(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	addMailbox(t, db, key, "a@zeta.example", "mail.example.jp", false)
	withHost := addMailbox(t, db, key, "b@alpha.example", "MX1.Example.JP.", true)
	broken := addMailbox(t, db, key, "c@alpha.example", "", true)
	addMailbox(t, db, key, "d@mid.example", "mx1.example.jp", true)
	// A host edited into the database by hand that is no host name.
	if _, err := db.Exec(`UPDATE mailboxes SET detail_info = json_set(detail_info, '$.smtp_host', 'bad_host') WHERE id = ?`, broken.ID); err != nil {
		t.Fatal(err)
	}
	check := func(name string, mb *models.Mailbox, wantMailbox, wantAll []string, empty bool) {
		t.Helper()
		sources, err := ResolveSendingSources(db)
		if err != nil || !reflect.DeepEqual(sources.MailboxHosts, wantMailbox) || !reflect.DeepEqual(sources.AllHosts(), wantAll) {
			t.Errorf("%s: sending sources %+v, all hosts %v (err %v); want mailbox hosts %v, all %v", name, sources, sources.AllHosts(), err, wantMailbox, wantAll)
		}
		b := recordGroupOptions(t)
		groupOptionsOf(t, jm, mb)
		if len(b.sources) != 1 || b.sources[0].Empty() != empty {
			t.Fatalf("%s: sending sources %+v, want empty %v", name, b.sources, empty)
		}
		for mta, wantMatch := range map[string]bool{"mx1.example.jp": true, "MAIL.example.jp.": true, "bad_host": empty, "mx.partner.example.net": empty} {
			if got := b.sources[0].Match(mta); got != wantMatch {
				t.Errorf("%s: Match(%q) = %v, want %v", name, mta, got, wantMatch)
			}
		}
	}
	mailboxHosts := []string{"mx1.example.jp", "mail.example.jp"}
	check("mailbox hosts only, a mailbox with one", withHost, mailboxHosts, mailboxHosts, false)
	check("mailbox hosts only, a mailbox without a valid one", broken, mailboxHosts, mailboxHosts, true)
	if err := SaveSendingSourceHosts(db, []string{"relay.example.jp", "mail.example.jp"}); err != nil {
		t.Fatal(err)
	}
	all := []string{"relay.example.jp", "mail.example.jp", "mx1.example.jp"}
	check("with sending_source_hosts, a mailbox with one", withHost, mailboxHosts, all, false)
	check("with sending_source_hosts, a mailbox without a valid one", broken, mailboxHosts, all, false)
}

// seedDNSChecks stores a result checked just before and one just after the
// cutoff of the cleanup: the mail retention plus the 24-hour reuse window.
func seedDNSChecks(t *testing.T, db *sql.DB) {
	t.Helper()
	keep := time.Duration(ResolveMailKeepDays(db)) * 24 * time.Hour
	for _, c := range []*models.DNSCheck{
		{Name: "old.example.jp", QType: "MX", Outcome: "exists", Detail: "x", CheckedAt: time.Now().Add(-keep - 25*time.Hour)},
		{Name: "window.example.jp", QType: "MX", Outcome: "exists", Detail: "x", CheckedAt: time.Now().Add(-keep - 23*time.Hour)},
		{Name: "new.example.jp", QType: "MX", Outcome: "exists", Detail: "x", CheckedAt: time.Now().Add(-keep + time.Hour)},
	} {
		if err := models.InsertDNSCheck(db, c); err != nil {
			t.Fatal(err)
		}
	}
}

// checkPrunedDNSChecks checks that only the result before the cutoff of
// seedDNSChecks is gone.
func checkPrunedDNSChecks(t *testing.T, db *sql.DB) {
	t.Helper()
	epoch := time.Unix(0, 0)
	if _, err := models.FindDNSCheck(db, "old.example.jp", "MX", epoch, time.Now(), false); err == nil {
		t.Error("the result past the mail retention and the reuse window was kept")
	}
	// A notice received at the end of the retention may still use a result
	// checked up to 24 hours before it.
	for _, name := range []string{"window.example.jp", "new.example.jp"} {
		if _, err := models.FindDNSCheck(db, name, "MX", epoch, time.Now(), false); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}

func TestPruneDNSChecksKeepsTheMailRetention(t *testing.T) {
	db := newTestDB(t)
	jm, _ := newTestJobManager(t, db)
	seedDNSChecks(t, db)
	var lines []string
	if err := jm.pruneDNSChecks(func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	checkPrunedDNSChecks(t, db)
	want := "removed 1 dns check result(s) older than the mail retention (180 days) and the 24-hour reuse window"
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("progress = %q, want %q", lines, want)
	}
}

// TestCleanupJobPrunesDNSChecks: the daily cleanup prunes the DNS
// verification results, both from the job of a mailbox and, without any
// mailbox, from the expansion job.
func TestCleanupJobPrunesDNSChecks(t *testing.T) {
	t.Run("mailbox", func(t *testing.T) {
		db := newTestDB(t)
		jm, key := newTestJobManager(t, db)
		mb, _ := seedMailbox(t, db, key, "ops@example.test", nil)
		seedDNSChecks(t, db)
		var lines []string
		if _, err := jm.RunJob(context.Background(), mailboxJob(JobKindCleanup, mb, ""), func(s string) { lines = append(lines, s) }); err != nil {
			t.Fatalf("cleanup: %v\n%s", err, strings.Join(lines, "\n"))
		}
		checkPrunedDNSChecks(t, db)
		if !strings.Contains(strings.Join(lines, "\n"), "removed 1 dns check result(s)") {
			t.Errorf("progress lacks the pruning:\n%s", strings.Join(lines, "\n"))
		}
	})
	t.Run("no mailbox", func(t *testing.T) {
		db := newTestDB(t)
		jm, _ := newTestJobManager(t, db)
		seedDNSChecks(t, db)
		var lines []string
		result, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindCleanup, RequestedBy: "cli"}, func(s string) { lines = append(lines, s) })
		if err != nil || !strings.HasPrefix(result, "queued 0 jobs (no mailbox)") {
			t.Fatalf("cleanup expansion: %q, %v", result, err)
		}
		checkPrunedDNSChecks(t, db)
		if !strings.Contains(strings.Join(lines, "\n"), "removed 1 dns check result(s)") {
			t.Errorf("progress lacks the pruning:\n%s", strings.Join(lines, "\n"))
		}
	})
}

// TestCleanupWithoutMailboxGoesOnAfterAFailure: without any mailbox the
// expansion job of the cleanup still prunes the DNS check results when the
// job history cannot be pruned (the table is unavailable here), and fails
// with that error.
func TestCleanupWithoutMailboxGoesOnAfterAFailure(t *testing.T) {
	db := newTestDB(t)
	jm, _ := newTestJobManager(t, db)
	seedDNSChecks(t, db)
	if _, err := db.Exec(`ALTER TABLE jobs RENAME TO jobs_unavailable`); err != nil {
		t.Fatal(err)
	}
	var lines []string
	result, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindCleanup, RequestedBy: "cli"}, func(s string) { lines = append(lines, s) })
	if err == nil || !strings.Contains(err.Error(), "job history") || result != "queued 0 jobs (no mailbox)" {
		t.Errorf("cleanup expansion: %q, %v; want the failure of the job history", result, err)
	}
	checkPrunedDNSChecks(t, db)
	if !strings.Contains(strings.Join(lines, "\n"), "removed 1 dns check result(s)") {
		t.Errorf("progress lacks the pruning:\n%s", strings.Join(lines, "\n"))
	}
}

// The backscatter rule follows the sending sources whether the DNS check
// is on or off: with nothing configured the return notice of forged mail
// stays a bounce as before, and so it does with the addresses of the
// sending network alone; a reclassify after also setting the sending MTA as
// the SMTP host of the
// mailbox makes it junk (the notice about mail from the sending sources stays a bounce), and
// one after clearing the host makes it a bounce again. A host name of
// sending_source_hosts counts the same way; the SMTP host of another, disabled
// mailbox does not switch the rule on for this one. (The lookups stand in
// for the network: the host name is answered without addresses, and the
// domain has no SPF record.)
func TestSendingSourcesApplyWithoutDNSCheck(t *testing.T) {
	for _, dnsCheck := range []bool{false, true} {
		t.Run(map[bool]string{false: "check off", true: "check on"}[dnsCheck], func(t *testing.T) {
			db := newTestDB(t)
			jm, key := newTestJobManager(t, db)
			if err := SetAgentEnabled(db, false); err != nil {
				t.Fatal(err)
			}
			if err := SaveDNSCheck(db, &dnsCheck, nil, nil); err != nil {
				t.Fatal(err)
			}
			installFakeLookups(t, map[string][]netip.Addr{"mx1.example.jp": nil}, map[string]dnscheck.SPFResult{"example.jp": {Complete: true}})
			mb, mailsRoot := seedMailbox(t, db, key, "newsletter@example.jp", []string{"postfix_backscatter.eml", "postfix_dsn.eml"})
			other := addMailbox(t, db, key, "info@example.jp", "", false)
			// run sets the sending source IPv4 list and the SMTP hosts of the
			// monitored mailbox and of another, disabled one.
			run := func(kind string, ipv4 []string, host, otherHost string, wantRules map[string]int, wantGroups int) {
				t.Helper()
				if err := SaveSendingSourceIPv4(db, ipv4); err != nil {
					t.Fatal(err)
				}
				for m, h := range map[*models.Mailbox]string{mb: host, other: otherHost} {
					m.SmtpHost = h
					if err := models.UpdateMailbox(db, m); err != nil {
						t.Fatal(err)
					}
				}
				sources := append(append([]string{}, ipv4...), host, otherHost)
				var lines []string
				if _, err := jm.RunJob(context.Background(), mailboxJob(kind, mb, ""), func(s string) { lines = append(lines, s) }); err != nil {
					t.Fatalf("%s: %v\n%s", kind, err, strings.Join(lines, "\n"))
				}
				idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer idx.Close()
				msgs, err := models.ListAllMessages(idx)
				if err != nil {
					t.Fatal(err)
				}
				rules := map[string]int{}
				for _, m := range msgs {
					rules[m.Rule]++
				}
				var groups int
				if err := idx.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&groups); err != nil {
					t.Fatal(err)
				}
				if len(rules) != len(wantRules) || groups != wantGroups {
					t.Errorf("%s with %v: rules %v, %d groups; want %v, %d groups", kind, sources, rules, groups, wantRules, wantGroups)
				}
				for rule, n := range wantRules {
					if rules[rule] != n {
						t.Errorf("%s with %v: rules %v, want %v", kind, sources, rules, wantRules)
					}
				}
			}
			network, mta := []string{"192.0.2.0/24"}, "mx1.example.jp"
			run(JobKindReindex, nil, "", "", map[string]int{"dsn_report": 2}, 2)
			run(JobKindReclassify, network, "", "", map[string]int{"dsn_report": 2}, 2)
			run(JobKindReclassify, network, mta, "", map[string]int{"dsn_report": 1, "backscatter": 1}, 1)
			run(JobKindReclassify, network, "", "", map[string]int{"dsn_report": 2}, 2)
			// The SMTP host of another, disabled mailbox does not switch the
			// rule on for this one (it only matches); a host name of the
			// setting does, alone.
			run(JobKindReclassify, nil, "", mta, map[string]int{"dsn_report": 2}, 2)
			run(JobKindReclassify, nil, "", "", map[string]int{"dsn_report": 2}, 2)
			if err := SaveSendingSourceHosts(db, []string{mta}); err != nil {
				t.Fatal(err)
			}
			run(JobKindReclassify, nil, "", "", map[string]int{"dsn_report": 1, "backscatter": 1}, 1)
		})
	}
}

// regroupRules reindexes (the first time) or reclassifies the mailbox and
// returns how many messages each rule took, and the progress lines.
func regroupRules(t *testing.T, jm *JobManager, mb *models.Mailbox, mailsRoot string) (map[string]int, string) {
	t.Helper()
	kind := JobKindReclassify
	if _, err := os.Stat(mailengine.MailboxIndexPath(mailsRoot, mb.Address)); err != nil {
		kind = JobKindReindex
	}
	var lines []string
	if _, err := jm.RunJob(context.Background(), mailboxJob(kind, mb, ""), func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatalf("%s: %v\n%s", kind, err, strings.Join(lines, "\n"))
	}
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	msgs, err := models.ListAllMessages(idx)
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]int{}
	for _, m := range msgs {
		rules[m.Rule]++
	}
	return rules, strings.Join(lines, "\n")
}

// The looked-up addresses decide the backscatter rule with the DNS check on:
// the SPF record of the domain of the mailbox lists the hop of mail from the sending sources
// (the notice about it stays a bounce) and its host names match as text; a
// complete lookup is stored, used without asking for an hour and stands in
// for a failed one for 14 days, an older row or none leaves the record
// incomplete, and then no return notice is taken for backscatter in the
// run.
func TestSPFDecidesTheBackscatterRule(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	spf := map[string]dnscheck.SPFResult{}
	f := installFakeLookups(t, map[string][]netip.Addr{"smtp.example.jp": nil}, spf) // the host has no address
	mb, mailsRoot := seedMailbox(t, db, key, "newsletter@example.jp", []string{"postfix_backscatter.eml", "postfix_dsn.eml"})
	mb.SmtpHost = "smtp.example.jp" // in no header of the samples
	if err := models.UpdateMailbox(db, mb); err != nil {
		t.Fatal(err)
	}
	stored := func() *models.SendingSourceLookup {
		t.Helper()
		row, err := models.GetSendingSourceLookup(db, models.SendingSourceKindSPF, "example.jp")
		if err != nil {
			return nil
		}
		return row
	}
	hop := netip.MustParsePrefix("192.0.2.20/32") // the hop of mail from the sending sources in postfix_dsn.eml
	for _, step := range []struct {
		name   string
		spf    dnscheck.SPFResult
		age    time.Duration // ages the stored row first (0: as it is; -1: deleted)
		lookup bool          // the record is evaluated (not the stored row of the last hour)
		rules  map[string]int
	}{
		{"complete, listing the hop", dnscheck.SPFResult{Prefixes: []netip.Prefix{hop}, Complete: true}, 0, true,
			map[string]int{"dsn_report": 1, "backscatter": 1}},
		{"stored 59 minutes ago: used without asking", dnscheck.SPFResult{}, 59 * time.Minute, false,
			map[string]int{"dsn_report": 1, "backscatter": 1}},
		{"incomplete, a row 13 days old stands in", dnscheck.SPFResult{}, 13 * 24 * time.Hour, true,
			map[string]int{"dsn_report": 1, "backscatter": 1}},
		{"incomplete, the stored row 15 days old", dnscheck.SPFResult{}, 15 * 24 * time.Hour, true,
			map[string]int{"dsn_report": 2}},
		{"incomplete, no stored row", dnscheck.SPFResult{}, -1, true, map[string]int{"dsn_report": 2}},
		{"complete, nothing listed", dnscheck.SPFResult{Complete: true}, 0, true, map[string]int{"backscatter": 2}},
		{"complete, the host name of an mx term", dnscheck.SPFResult{Hosts: []string{"mx1.example.jp"}, Complete: true}, 61 * time.Minute, true,
			map[string]int{"dsn_report": 1, "backscatter": 1}},
	} {
		spf["example.jp"] = step.spf
		switch {
		case step.age < 0:
			if _, err := db.Exec(`DELETE FROM sending_source_lookups`); err != nil {
				t.Fatal(err)
			}
		case step.age > 0:
			if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ? WHERE kind = 'spf'`, time.Now().UTC().Add(-step.age)); err != nil {
				t.Fatal(err)
			}
		}
		calls := len(f.spfCalls)
		rules, progress := regroupRules(t, jm, mb, mailsRoot)
		if !reflect.DeepEqual(rules, step.rules) {
			t.Errorf("%s: rules %v, want %v\n%s", step.name, rules, step.rules, progress)
		}
		if step.lookup && (len(f.spfCalls) != calls+1 || f.spfCalls[calls] != "example.jp") {
			t.Errorf("%s: SPF lookups %v, want example.jp once", step.name, f.spfCalls[calls:])
		}
		if !step.lookup && len(f.spfCalls) != calls {
			t.Errorf("%s: SPF lookups %v, want none", step.name, f.spfCalls[calls:])
		}
		if !step.spf.Complete && step.rules["backscatter"] == 0 && !strings.Contains(progress, "SPF not read completely for example.jp") {
			t.Errorf("%s: progress lacks the incomplete SPF:\n%s", step.name, progress)
		}
		row := stored()
		if step.spf.Complete && (row == nil || time.Since(row.FetchedAt) > time.Minute) {
			t.Errorf("%s: the complete lookup was not stored: %+v", step.name, row)
		}
		if step.name == "complete, listing the hop" && (row == nil || !reflect.DeepEqual(row.Addresses, []string{"192.0.2.20"})) {
			t.Errorf("%s: stored row %+v", step.name, row)
		}
	}
}

// With the DNS check off no SPF record is read and no stored one is used;
// the host names are looked up through the OS resolver, with the stored
// lookup as the fallback while it does not answer.
func TestSourceLookupsWithoutDNSCheck(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := SaveDNSCheck(db, &off, nil, nil); err != nil {
		t.Fatal(err)
	}
	hosts := map[string][]netip.Addr{"smtp.example.jp": {netip.MustParseAddr("192.0.2.20")}}
	f := installFakeLookups(t, hosts, map[string]dnscheck.SPFResult{"example.jp": {Complete: true}})
	mb, mailsRoot := seedMailbox(t, db, key, "newsletter@example.jp", []string{"postfix_backscatter.eml", "postfix_dsn.eml"})
	mb.SmtpHost = "smtp.example.jp"
	if err := models.UpdateMailbox(db, mb); err != nil {
		t.Fatal(err)
	}
	// A stored SPF row is not used either.
	if _, err := models.ReplaceSendingSourceLookup(db, &models.SendingSourceLookup{Kind: models.SendingSourceKindSPF, Name: "example.jp",
		Addresses: []string{"198.51.100.0/24"}}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"dsn_report": 1, "backscatter": 1}
	if rules, progress := regroupRules(t, jm, mb, mailsRoot); !reflect.DeepEqual(rules, want) {
		t.Errorf("host answered: rules %v, want %v\n%s", rules, want, progress)
	}
	row, err := models.GetSendingSourceLookup(db, models.SendingSourceKindSMTPHost, "smtp.example.jp")
	if err != nil || !reflect.DeepEqual(row.Addresses, []string{"192.0.2.20"}) {
		t.Errorf("stored host lookup %+v (err %v)", row, err)
	}
	// The OS resolver does not answer now: the stored lookup (two hours
	// old, so asked again) stands in.
	delete(hosts, "smtp.example.jp")
	if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ? WHERE kind = 'smtp_host'`, time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rules, progress := regroupRules(t, jm, mb, mailsRoot); !reflect.DeepEqual(rules, want) {
		t.Errorf("host not answered, fresh row: rules %v, want %v\n%s", rules, want, progress)
	}
	// Without a row of at most 14 days an address of a sending source may be missing:
	// the run takes no return notice for backscatter.
	if _, err := db.Exec(`UPDATE sending_source_lookups SET fetched_at = ? WHERE kind = 'smtp_host'`, time.Now().UTC().Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rules, progress := regroupRules(t, jm, mb, mailsRoot); !reflect.DeepEqual(rules, map[string]int{"dsn_report": 2}) ||
		!strings.Contains(progress, "backscatter detection was skipped for this run: no answer for the addresses of smtp.example.jp") {
		t.Errorf("host not answered, old row: rules %v\n%s", rules, progress)
	}
	// Answered without addresses (NXDOMAIN): the host adds no address, both
	// notices look foreign (the host name is in no header), which a host
	// name without addresses is allowed to cause.
	hosts["smtp.example.jp"] = nil
	if rules, progress := regroupRules(t, jm, mb, mailsRoot); !reflect.DeepEqual(rules, map[string]int{"backscatter": 2}) {
		t.Errorf("host answered without addresses: rules %v\n%s", rules, progress)
	}
	if len(f.spfCalls) != 0 {
		t.Errorf("SPF read with the check off: %v", f.spfCalls)
	}
	if len(f.hostCalls) != 4 {
		t.Errorf("host lookups %v, want one per run", f.hostCalls)
	}
}

// A source host name whose lookup is not answered, with no stored row of at
// most 14 days to stand in, stops the backscatter rule for the run with the
// DNS check on (the Checker) as with it off (the OS resolver): the return
// notice of forged mail stays a bounce, and the progress names the host. A
// host answered without addresses (NXDOMAIN) does not.
func TestUnansweredSourceHostStopsBackscatter(t *testing.T) {
	for _, dnsCheck := range []bool{false, true} {
		t.Run(map[bool]string{false: "check off", true: "check on"}[dnsCheck], func(t *testing.T) {
			db := newTestDB(t)
			jm, key := newTestJobManager(t, db)
			if err := SetAgentEnabled(db, false); err != nil {
				t.Fatal(err)
			}
			if err := SaveDNSCheck(db, &dnsCheck, nil, nil); err != nil {
				t.Fatal(err)
			}
			hosts := map[string][]netip.Addr{}
			installFakeLookups(t, hosts, map[string]dnscheck.SPFResult{"example.jp": {Complete: true}})
			mb, mailsRoot := seedMailbox(t, db, key, "newsletter@example.jp", []string{"postfix_backscatter.eml", "postfix_dsn.eml"})
			mb.SmtpHost = "smtp.example.jp" // in no header of the samples
			if err := models.UpdateMailbox(db, mb); err != nil {
				t.Fatal(err)
			}
			rules, progress := regroupRules(t, jm, mb, mailsRoot)
			if !reflect.DeepEqual(rules, map[string]int{"dsn_report": 2}) ||
				!strings.Contains(progress, "backscatter detection was skipped for this run: no answer for the addresses of smtp.example.jp") {
				t.Errorf("not answered: rules %v\n%s", rules, progress)
			}
			for _, row := range sendingSourceRows(t, db) {
				if row.Kind == models.SendingSourceKindSMTPHost {
					t.Errorf("an unanswered lookup was stored: %+v", row)
				}
			}
			hosts["smtp.example.jp"] = nil
			if rules, progress := regroupRules(t, jm, mb, mailsRoot); !reflect.DeepEqual(rules, map[string]int{"backscatter": 2}) {
				t.Errorf("answered without addresses: rules %v\n%s", rules, progress)
			}
		})
	}
}

// sendingSourceRows returns every stored lookup of the sending sources.
func sendingSourceRows(t *testing.T, db *sql.DB) []*models.SendingSourceLookup {
	t.Helper()
	rows, err := models.ListSendingSourceLookups(db)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// With the check off a lookup failure is filed by the outcome stored for it
// (the reuse rule) without any query: none stored, it stays unverified (a
// delay warning is a delivery delay, and the rule name says that nothing was
// verified); once an outcome is stored, the reclassify files it as verified
// (nothing configured: the reporting MTA counts as a sending server).
func TestReindexJobWithoutDNSCheckUsesStoredOutcomes(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := SaveDNSCheck(db, &off, nil, nil); err != nil {
		t.Fatal(err)
	}
	b := recordGroupOptions(t)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", []string{"postfix_delayed_dns.eml"})
	run := func(kind, wantCategory, wantRule, wantProgress string) {
		t.Helper()
		var lines []string
		if _, err := jm.RunJob(context.Background(), mailboxJob(kind, mb, ""), func(s string) { lines = append(lines, s) }); err != nil {
			t.Fatalf("%s: %v\n%s", kind, err, strings.Join(lines, "\n"))
		}
		idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()
		var category, rule string
		if err := idx.QueryRow(`SELECT g.category, b.category_rule FROM bounces b JOIN groups g ON g.group_key = b.group_key`).Scan(&category, &rule); err != nil {
			t.Fatal(err)
		}
		if category != wantCategory || rule != wantRule {
			t.Errorf("%s: category = %s, rule = %s; want %s, %s", kind, category, rule, wantCategory, wantRule)
		}
		if !slices.Contains(lines, wantProgress) {
			t.Errorf("%s: progress lacks %q:\n%s", kind, wantProgress, strings.Join(lines, "\n"))
		}
	}
	run(JobKindReindex, CategoryDeliveryDelay, "dns_unverified_delay", "dns: 1 notice(s): 0 verified, 0 reused from stored results, "+
		"0 over budget (run again), 0 not applicable, 0 inconclusive, 0 too old for a live check, 1 not checked (the DNS check is off)")
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "infoseek.jp", QType: "MX", Outcome: "exists", Detail: "stored",
		CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	run(JobKindReclassify, CategoryDNSResolverFailure, "dns_resolver_failure", "dns: 1 notice(s): 0 verified, 1 reused from stored results, "+
		"0 over budget (run again), 0 not applicable, 0 inconclusive, 0 too old for a live check")
	for _, cfg := range b.checkers {
		if !reflect.DeepEqual(cfg, dnscheck.Config{StoredOnly: true}) {
			t.Errorf("a Checker that may query was made with the check off: %+v", cfg)
		}
	}
	if len(b.checkers) != 2 || !b.verifierOK {
		t.Errorf("checkers %+v (verifier %v), want a StoredOnly one per run", b.checkers, b.verifierOK)
	}
}

// addRawMessage writes one more raw message of a mailbox seeded with
// seedMailbox (n numbers it after the samples).
func addRawMessage(t *testing.T, mailsRoot, address string, n int, raw []byte) {
	t.Helper()
	msgKey := mailengine.MessageKey(time.Date(2025, 9, 1+n, 0, 0, 0, 0, time.UTC), "INBOX", 1, uint32(n+1))
	path := mailengine.MessageFilePath(mailsRoot, address, msgKey, "eml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The "nothing configured" rules are judged per mailbox, while every source
// matches in every run: with mailbox A's SMTP host mx1.example.jp, mailbox C's
// smtp-c.example.jp and none for mailbox B, the return notice of forged mail
// is junk for A and C but stays a bounce for B; the notice about mail from
// the sending sources (its hop handed on by mx1.example.jp, A's host) stays
// a bounce for C too. A verified DNS lookup failure reported by
// mx1.example.jp is a resolver failure of a sending server everywhere (by
// the text for A and C, by the empty configuration for B); one reported by
// an outside address is external for A and C and a resolver failure of a
// sending server for B, which has nothing configured. A
// host name of sending_source_hosts configures every mailbox: then B is
// judged like the others. (The DNS check is off: a stored outcome files the
// notices without any query.)
func TestSendingSourcesActivatePerMailbox(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := SaveDNSCheck(db, &off, nil, nil); err != nil {
		t.Fatal(err)
	}
	installFakeLookups(t, map[string][]netip.Addr{"mx1.example.jp": nil, "smtp-c.example.jp": nil, "relay.example.jp": nil}, nil)
	if err := models.InsertDNSCheck(db, &models.DNSCheck{Name: "infoseek.jp", QType: "MX", Outcome: "exists", Detail: "stored",
		CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	samples := []string{"postfix_backscatter.eml", "postfix_dsn.eml", "postfix_delayed_dns.eml"}
	delayed, err := os.ReadFile(filepath.Join("mailengine", "testdata", "postfix_delayed_dns.eml"))
	if err != nil {
		t.Fatal(err)
	}
	fromElsewhere := []byte(strings.Replace(string(delayed), "Reporting-MTA: dns; mx1.example.jp", "Reporting-MTA: dns; [203.0.113.9]", 1))
	mailboxes := map[string]*models.Mailbox{}
	var mailsRoot string
	for name, host := range map[string]string{"a": "mx1.example.jp", "b": "", "c": "smtp-c.example.jp"} {
		mb, root := seedMailbox(t, db, key, name+"@example.jp", samples)
		addRawMessage(t, root, mb.Address, len(samples), fromElsewhere)
		mb.SmtpHost = host
		if err := models.UpdateMailbox(db, mb); err != nil {
			t.Fatal(err)
		}
		mailboxes[name], mailsRoot = mb, root
	}
	// judge regroups a mailbox and returns how many return notices it took
	// for backscatter, and the category rule of the DNS lookup failure each
	// reporting MTA reported.
	judge := func(name string) (int, map[string]string) {
		t.Helper()
		rules, progress := regroupRules(t, jm, mailboxes[name], mailsRoot)
		idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mailboxes[name].Address, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()
		rows, err := idx.Query(`SELECT reporting_mta, category_rule FROM bounces WHERE category_rule LIKE 'dns_%'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		dns := map[string]string{}
		for rows.Next() {
			var mta, rule string
			if err := rows.Scan(&mta, &rule); err != nil {
				t.Fatal(err)
			}
			dns[mta] = rule
		}
		if !strings.Contains(progress, "dns: 2 notice(s): 0 verified, 2 reused from stored results, 0 over budget (run again), "+
			"0 not applicable, 0 inconclusive, 0 too old for a live check") {
			t.Errorf("%s: progress lacks the stored outcomes:\n%s", name, progress)
		}
		return rules["backscatter"], dns
	}
	sourceFailure := map[string]string{"mx1.example.jp": "dns_resolver_failure", "[203.0.113.9]": "dns_resolver_failure"}
	external := map[string]string{"mx1.example.jp": "dns_resolver_failure", "[203.0.113.9]": "dns_failure_external"}
	for _, step := range []struct {
		globalHosts []string
		backscatter map[string]int
		dns         map[string]map[string]string
	}{
		{nil, map[string]int{"a": 1, "b": 0, "c": 1}, map[string]map[string]string{"a": external, "b": sourceFailure, "c": external}},
		{[]string{"relay.example.jp"}, map[string]int{"a": 1, "b": 1, "c": 1}, map[string]map[string]string{"a": external, "b": external, "c": external}},
	} {
		if err := SaveSendingSourceHosts(db, step.globalHosts); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b", "c"} {
			backscatter, dns := judge(name)
			if backscatter != step.backscatter[name] || !reflect.DeepEqual(dns, step.dns[name]) {
				t.Errorf("sending_source_hosts %v, mailbox %s: %d backscatter, DNS %v; want %d, %v",
					step.globalHosts, name, backscatter, dns, step.backscatter[name], step.dns[name])
			}
		}
	}
}
