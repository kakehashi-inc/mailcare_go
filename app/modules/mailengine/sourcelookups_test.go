package mailengine

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
)

// fakeSourceHosts resolves the source host names from a table (a name not listed
// is answered without addresses, as for NXDOMAIN) and records the names
// asked; the lookup of a name in unanswered is not answered (it still
// returns what the table lists, as half an answer does).
type fakeSourceHosts struct {
	addrs      map[string][]netip.Addr
	unanswered map[string]bool

	mu    sync.Mutex // the grouping asks for several names at once
	calls []string
}

func (f *fakeSourceHosts) ResolveHost(_ context.Context, host string) ([]netip.Addr, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, host)
	return f.addrs[host], !f.unanswered[host]
}

// fakeSourceSPF answers every domain with the same evaluation and records the
// domains asked.
type fakeSourceSPF struct {
	prefixes []netip.Prefix
	hosts    []string
	complete bool
	calls    []string
}

func (f *fakeSourceSPF) ResolveSPF(_ context.Context, domain string) ([]netip.Prefix, []string, bool) {
	f.calls = append(f.calls, domain)
	return f.prefixes, f.hosts, f.complete
}

// TestSendingSourcesWithResolved: the looked-up addresses, prefixes and host
// names are added to a copy, leaving the original as it was; they make no
// source configured and no host name the backscatter rule asks for, but
// match in Match and MatchReceived.
func TestSendingSourcesWithResolved(t *testing.T) {
	base := NewSendingSources([]string{"192.0.2.0/24"}, nil, nil, addrs("198.51.100.1"))
	orig := NewSendingSources([]string{"192.0.2.0/24"}, nil, nil, addrs("198.51.100.1"))
	got := base.withResolved(addrs("::ffff:203.0.113.5"), []netip.Prefix{netip.MustParsePrefix("2001:db8::1/64")}, []string{"MX.Example.JP."})
	if !reflect.DeepEqual(base, orig) {
		t.Errorf("the original changed: %+v", base)
	}
	if got.hasHosts() || got.configured != base.configured {
		t.Errorf("looked-up names count as configured host names: %+v", got)
	}
	for line, want := range map[string]bool{
		"from a.example (a.example [203.0.113.5]) by b.example":    true,
		"from a.example (a.example [2001:db8::99]) by b.example":   true,
		"from mx.example.jp (unknown [198.51.100.9]) by b.example": true,
		"from c.example (c.example [198.51.100.9]) by d.example":   false,
		"from c.example (c.example [198.51.100.1]) by d.example":   true,
	} {
		if got.MatchReceived(line) != want {
			t.Errorf("MatchReceived(%q) = %v, want %v", line, !want, want)
		}
	}
	if !got.Match("mx.example.jp") || !got.Match("[203.0.113.5]") || got.Match("other.example.jp") {
		t.Error("Match does not use the looked-up sources")
	}
	empty := SendingSources{}.withResolved(addrs("203.0.113.5"), nil, []string{"mx.example.jp"})
	if !empty.Empty() {
		t.Error("looked-up sources alone make the configuration non-empty")
	}
}

// groupSamples groups the samples into a fresh mailbox with the options and
// returns how many messages each rule took and the progress lines.
func groupSamples(t *testing.T, names []string, opts ...GroupOption) (map[string]int, string) {
	t.Helper()
	root := t.TempDir()
	address := "newsletter@example.jp"
	for i, name := range names {
		storeRaw(t, root, address, readSample(t, name), uint32(i+1), time.Now().UTC())
	}
	var lines []string
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) }, opts...); err != nil {
		t.Fatal(err)
	}
	msgs, err := models.ListAllMessages(mustOpenIndex(t, root, address))
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]int{}
	for _, m := range msgs {
		rules[m.Rule]++
	}
	return rules, strings.Join(lines, "\n")
}

// TestBackscatterWithLookedUpSources files the return notice of forged mail
// and a notice about mail from the sending sources (its hop 192.0.2.20
// named app01.example.jp, reported by mx1.example.jp) with looked-up
// sources: an address a source host name resolves to, or an SPF record
// listing it, keeps that notice a bounce;
// a name answered without addresses (NXDOMAIN) still matches as text; a
// name whose lookup is not answered (even half answered) and an incomplete
// SPF record stop the rule for the run; the host names of an SPF record
// match as text but do not make the rule apply without a configured host
// name.
func TestBackscatterWithLookedUpSources(t *testing.T) {
	samples := []string{"postfix_backscatter.eml", "postfix_dsn.eml"}
	smtp := NewSendingSources(nil, nil, []string{"smtp.example.jp"}, nil) // in no header of the samples
	ipsOnly := NewSendingSources([]string{"198.51.100.0/24"}, nil, nil, nil)
	hop := []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")}
	notAnswered := map[string]bool{"smtp.example.jp": true}
	for _, c := range []struct {
		name       string
		sources    SendingSources
		hosts      map[string][]netip.Addr
		unanswered map[string]bool
		spf        *fakeSourceSPF
		rules      map[string]int
		hostCalls  int
		spfCalls   int
		progress   string // a clause the summary of the lookups must hold
	}{
		{"host resolves to the hop", smtp, map[string][]netip.Addr{"smtp.example.jp": addrs("192.0.2.20")}, nil, nil,
			map[string]int{ruleBackscatter: 1, ruleDSNReport: 1}, 1, 0, "\njunk: 1 backscatter, 0 fake_notice_link, 0 fake_notice_auth\n"},
		{"host resolves to the mapped hop", smtp, map[string][]netip.Addr{"smtp.example.jp": addrs("::ffff:192.0.2.20")}, nil, nil,
			map[string]int{ruleBackscatter: 1, ruleDSNReport: 1}, 1, 0, ""},
		{"host answered without addresses", smtp, nil, nil, nil, map[string]int{ruleBackscatter: 2}, 1, 0,
			"sending sources: 0 address(es) of 1 host name(s), 0 SPF prefix(es) and 0 SPF host name(s) of 0 domain(s)\n"},
		{"host lookup not answered", smtp, nil, notAnswered, nil, map[string]int{ruleDSNReport: 2}, 1, 0,
			"; backscatter detection was skipped for this run: no answer for the addresses of smtp.example.jp\n"},
		{"host lookup half answered, elsewhere", smtp, map[string][]netip.Addr{"smtp.example.jp": addrs("198.51.100.250")}, notAnswered, nil,
			map[string]int{ruleDSNReport: 2}, 1, 0, "no answer for the addresses of smtp.example.jp"},
		{"host answered without addresses but matches as text", NewSendingSources(nil, nil, []string{"mx1.example.jp"}, nil), nil, nil, nil,
			map[string]int{ruleBackscatter: 1, ruleDSNReport: 1}, 1, 0, ""},
		{"host not answered but matches as text", NewSendingSources(nil, nil, []string{"mx1.example.jp"}, nil), nil,
			map[string]bool{"mx1.example.jp": true}, nil, map[string]int{ruleDSNReport: 2}, 1, 0, "no answer for the addresses of mx1.example.jp"},
		{"SPF lists the hop", smtp, nil, nil, &fakeSourceSPF{prefixes: hop, complete: true},
			map[string]int{ruleBackscatter: 1, ruleDSNReport: 1}, 1, 1, ""},
		{"SPF lists no sending source", smtp, nil, nil, &fakeSourceSPF{complete: true}, map[string]int{ruleBackscatter: 2}, 1, 1, ""},
		{"SPF incomplete", smtp, nil, nil, &fakeSourceSPF{prefixes: hop[:0]}, map[string]int{ruleDSNReport: 2}, 1, 1,
			"; backscatter detection was skipped for this run: SPF not read completely for example.jp\n"},
		{"host not answered and SPF incomplete", smtp, nil, notAnswered, &fakeSourceSPF{}, map[string]int{ruleDSNReport: 2}, 1, 1,
			"; backscatter detection was skipped for this run: no answer for the addresses of smtp.example.jp; SPF not read completely for example.jp\n"},
		{"SPF host name matches as text", smtp, nil, nil, &fakeSourceSPF{hosts: []string{"mx1.example.jp"}, complete: true},
			map[string]int{ruleBackscatter: 1, ruleDSNReport: 1}, 1, 1, ""},
		{"SPF host names without a configured host", ipsOnly, nil, nil, &fakeSourceSPF{hosts: []string{"mx1.example.jp"}, complete: true},
			map[string]int{ruleDSNReport: 2}, 0, 0, ""},
		{"addresses only", ipsOnly, map[string][]netip.Addr{"smtp.example.jp": addrs("192.0.2.20")}, nil, &fakeSourceSPF{complete: true},
			map[string]int{ruleDSNReport: 2}, 0, 0, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			hosts := &fakeSourceHosts{addrs: c.hosts, unanswered: c.unanswered}
			opts := []GroupOption{WithSendingSources(c.sources), WithSourceHostResolver(hosts)}
			if c.spf != nil {
				opts = append(opts, WithSourceSPF(c.spf, []string{"example.jp"}))
			}
			rules, progress := groupSamples(t, samples, opts...)
			if !reflect.DeepEqual(rules, c.rules) {
				t.Errorf("rules %v, want %v\n%s", rules, c.rules, progress)
			}
			spfCalls := 0
			if c.spf != nil {
				spfCalls = len(c.spf.calls)
			}
			if len(hosts.calls) != c.hostCalls || spfCalls != c.spfCalls {
				t.Errorf("host lookups %v, SPF lookups %d; want %d and %d (once per run)", hosts.calls, spfCalls, c.hostCalls, c.spfCalls)
			}
			if !strings.Contains(progress+"\n", c.progress) {
				t.Errorf("progress lacks %q:\n%s", c.progress, progress)
			}
		})
	}
}

// spfByDomain evaluates the SPF record of each domain from a table,
// completely (a domain not listed lists nothing).
type spfByDomain map[string][]netip.Prefix

func (f spfByDomain) ResolveSPF(_ context.Context, domain string) ([]netip.Prefix, []string, bool) {
	return f[domain], nil, true
}

// TestBackscatterSPFUnionCap: the records of the domains of a run each
// stay within the bounds of one evaluation, but may take in more address
// space together than one record may (2^24 IPv4 or 2^112 IPv6 addresses,
// every address counted once): the SPF supplement is then incomplete for the
// run, the backscatter rule does not take the notice and the progress line
// names the reason. Together within the bounds (a range listed by both
// counts once), the rule applies.
func TestBackscatterSPFUnionCap(t *testing.T) {
	samples := []string{"postfix_backscatter.eml", "postfix_dsn.eml"}
	smtp := NewSendingSources(nil, nil, []string{"smtp.example.jp"}, nil) // in no header of the samples
	p := func(s ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, v := range s {
			out = append(out, netip.MustParsePrefix(v))
		}
		return out
	}
	const skipped = "; backscatter detection was skipped for this run: SPF ranges too wide in total"
	for _, c := range []struct {
		name   string
		spf    spfByDomain
		rules  map[string]int
		reason bool
	}{
		{"IPv4 within together", spfByDomain{"example.jp": p("20.0.0.0/9"), "example.com": p("20.128.0.0/9")},
			map[string]int{ruleBackscatter: 2}, false},
		{"IPv4 the same range twice", spfByDomain{"example.jp": p("20.0.0.0/8"), "example.com": p("20.0.0.0/8")},
			map[string]int{ruleBackscatter: 2}, false},
		{"IPv4 too wide together", spfByDomain{"example.jp": p("20.0.0.0/8"), "example.com": p("30.0.0.0/32")},
			map[string]int{ruleDSNReport: 2}, true},
		{"IPv6 too wide together", spfByDomain{"example.jp": p("2400::/16"), "example.com": p("2600::/16")},
			map[string]int{ruleDSNReport: 2}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			rules, progress := groupSamples(t, samples, WithSendingSources(smtp), WithSourceHostResolver(&fakeSourceHosts{}),
				WithSourceSPF(c.spf, []string{"example.jp", "example.com"}))
			if !reflect.DeepEqual(rules, c.rules) {
				t.Errorf("rules %v, want %v\n%s", rules, c.rules, progress)
			}
			if got := strings.Contains(progress, skipped); got != c.reason {
				t.Errorf("progress names the reason: %v, want %v\n%s", got, c.reason, progress)
			}
		})
	}
}

// slowSourceHosts answers each source host name after a delay with an address
// made from its number, leaves the odd ones unanswered, and records how
// many lookups ran at the same time.
type slowSourceHosts struct {
	delay time.Duration

	mu            sync.Mutex
	running, most int
}

func (s *slowSourceHosts) ResolveHost(_ context.Context, host string) ([]netip.Addr, bool) {
	s.mu.Lock()
	s.running++
	s.most = max(s.most, s.running)
	s.mu.Unlock()
	time.Sleep(s.delay)
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
	var n int
	fmt.Sscanf(host, "mx%d.", &n)
	return addrs(fmt.Sprintf("192.0.2.%d", n)), n%2 == 0
}

// TestSourceHostLookupsInParallel: the source host names are looked up at most
// four at a time, and what they found is taken in the order of the names,
// so the run and its progress line do not depend on which answered first.
func TestSourceHostLookupsInParallel(t *testing.T) {
	var hosts []string
	for i := range 10 {
		hosts = append(hosts, fmt.Sprintf("mx%d.example.jp", i))
	}
	r := &slowSourceHosts{delay: 20 * time.Millisecond}
	l := newSourceLookups(r, NewSendingSources(nil, nil, hosts, nil), nil, nil)
	start := time.Now()
	e := l.get(context.Background())
	if r.most != maxParallelHostLookups {
		t.Errorf("%d lookups at the same time, want %d", r.most, maxParallelHostLookups)
	}
	if elapsed := time.Since(start); elapsed >= 10*r.delay {
		t.Errorf("the lookups took %v, as long as one after another", elapsed)
	}
	var want []netip.Addr
	for i := range 10 {
		want = append(want, netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i)))
	}
	if !reflect.DeepEqual(e.addrs, want) {
		t.Errorf("addresses %v, want %v", e.addrs, want)
	}
	if want := []string{"mx1.example.jp", "mx3.example.jp", "mx5.example.jp", "mx7.example.jp", "mx9.example.jp"}; !reflect.DeepEqual(e.unanswered, want) {
		t.Errorf("unanswered %v, want %v", e.unanswered, want)
	}
}

// TestNothingLookedUpWithoutCandidate: a run without a return notice the
// rule would take, and without a verified DNS lookup failure, looks
// nothing up.
func TestNothingLookedUpWithoutCandidate(t *testing.T) {
	hosts := &fakeSourceHosts{}
	spf := &fakeSourceSPF{complete: true}
	sources := NewSendingSources(nil, nil, []string{"mx1.example.jp"}, nil)
	rules, progress := groupSamples(t, []string{"postfix_dsn.eml", "exim_bounce.eml", "normal.eml"},
		WithSendingSources(sources), WithSourceHostResolver(hosts), WithSourceSPF(spf, []string{"example.jp"}))
	if rules[ruleBackscatter] != 0 || len(hosts.calls) != 0 || len(spf.calls) != 0 {
		t.Errorf("rules %v, host lookups %v, SPF lookups %v", rules, hosts.calls, spf.calls)
	}
	if strings.Contains(progress, "sending sources:") {
		t.Errorf("a summary of lookups that were not made:\n%s", progress)
	}
}

// TestSourceMTAWithLookedUpSources files a verified DNS lookup failure whose
// reporting MTA is an IP literal or a name: an address of a source host name
// (resolved through WithSourceHostResolver, the verifier resolving nothing) or
// a prefix of an SPF record makes it a resolver failure of a sending server; an address in
// none of them is external while the lookups are complete and not verified
// while the lookup of a source host name was not answered or an SPF record is
// incomplete; a host name of an SPF record matches as text.
func TestSourceMTAWithLookedUpSources(t *testing.T) {
	sources := NewSendingSources(nil, nil, []string{"mx9.example.jp"}, nil)
	sourceHosts := func() *fakeSourceHosts {
		return &fakeSourceHosts{addrs: map[string][]netip.Addr{"mx9.example.jp": addrs("203.0.113.25")}}
	}
	halfAnswered := func() *fakeSourceHosts {
		f := sourceHosts()
		f.unanswered = map[string]bool{"mx9.example.jp": true}
		return f
	}
	spfNet := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	for _, c := range []struct {
		name     string
		mta      string
		hosts    *fakeSourceHosts
		spf      *fakeSourceSPF
		rule     string
		progress string
	}{
		{"address of a source host", "[203.0.113.25]", sourceHosts(), nil, "dns_resolver_failure", dnsLineVerified},
		{"source host not resolved", "[203.0.113.25]", nil, nil, "dns_failure_external", dnsLineVerified},
		{"source host answered without addresses", "[203.0.113.25]", &fakeSourceHosts{}, nil, "dns_failure_external",
			dnsLineVerified},
		{"outside, source host not answered", "[192.0.2.99]", halfAnswered(), nil, "dns_unverified_not_found",
			dnsLineInconclusive},
		{"address of a source host half answered", "[203.0.113.25]", halfAnswered(), nil, "dns_resolver_failure",
			dnsLineVerified},
		{"in an SPF prefix", "[198.51.100.7]", nil, &fakeSourceSPF{prefixes: spfNet, complete: true}, "dns_resolver_failure",
			dnsLineVerified},
		{"outside, SPF complete", "[192.0.2.99]", sourceHosts(), &fakeSourceSPF{prefixes: spfNet, complete: true}, "dns_failure_external",
			dnsLineVerified},
		{"outside, SPF incomplete", "[192.0.2.99]", sourceHosts(), &fakeSourceSPF{prefixes: spfNet}, "dns_unverified_not_found",
			dnsLineInconclusive},
		{"inside, SPF incomplete", "[198.51.100.7]", nil, &fakeSourceSPF{prefixes: spfNet}, "dns_resolver_failure",
			dnsLineVerified},
		{"host name of an SPF record", "relay.example.jp", nil, &fakeSourceSPF{hosts: []string{"relay.example.jp"}, complete: true},
			"dns_resolver_failure", dnsLineVerified},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			address := "newsletter@example.jp"
			msg, _ := storeRaw(t, root, address, dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", c.mta,
				"Name service error for name=nowhere.example type=MX: Host not found"), 1, time.Now().UTC())
			opts := []GroupOption{WithSendingSources(sources), WithDNS(&fakeVerifier{outcome: DNSOutcomeExists, ok: true}, sources)}
			if c.hosts != nil {
				opts = append(opts, WithSourceHostResolver(c.hosts))
			}
			if c.spf != nil {
				opts = append(opts, WithSourceSPF(c.spf, []string{"example.jp"}))
			}
			var lines []string
			if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) }, opts...); err != nil {
				t.Fatal(err)
			}
			b, err := models.GetBounceByMessageID(mustOpenIndex(t, root, address), msg.ID)
			if err != nil {
				t.Fatal(err)
			}
			if b.CategoryRule != c.rule {
				t.Errorf("rule %s, want %s\n%s", b.CategoryRule, c.rule, strings.Join(lines, "\n"))
			}
			if got := dnsLines(lines); len(got) != 1 || got[0] != c.progress {
				t.Errorf("progress %q, want %q", got, c.progress)
			}
		})
	}
}

// TestSendingSourcesLineWithoutBackscatter: in the run of a mailbox without a
// source host name (the backscatter rule does not apply), a lookup the
// DNS judgement made that was not answered is named in the progress line
// without the clause that backscatter detection was skipped.
func TestSendingSourcesLineWithoutBackscatter(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	storeRaw(t, root, address, dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "[192.0.2.99]",
		"Name service error for name=nowhere.example type=MX: Host not found"), 1, time.Now().UTC())
	sources := NewSendingSources([]string{"198.51.100.0/24"}, nil, nil, nil).WithOtherHosts([]string{"smtp-b.example.jp"})
	hosts := &fakeSourceHosts{unanswered: map[string]bool{"smtp-b.example.jp": true}}
	var lines []string
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) },
		WithSendingSources(sources), WithSourceHostResolver(hosts), WithDNS(&fakeVerifier{outcome: DNSOutcomeExists, ok: true}, sources)); err != nil {
		t.Fatal(err)
	}
	progress := strings.Join(lines, "\n")
	want := "sending sources: 0 address(es) of 1 host name(s), 0 SPF prefix(es) and 0 SPF host name(s) of 0 domain(s); " +
		"no answer for the addresses of smtp-b.example.jp\n"
	if !strings.Contains(progress+"\n", want) || strings.Contains(progress, "backscatter") {
		t.Errorf("progress:\n%s\nwant %q and no word about backscatter", progress, want)
	}
	if got := dnsLines(lines); len(got) != 1 || got[0] != dnsLineInconclusive {
		t.Errorf("dns line %q, want %q", got, dnsLineInconclusive)
	}
}

// TestSendingSourcesLineNothingToLookUp: a run whose sending sources hold
// addresses only (no host name to resolve, no SPF record to read) makes no
// lookup of the sending sources and prints no line about them, even with a
// resolver given and a notice that needs the judgement.
func TestSendingSourcesLineNothingToLookUp(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	storeRaw(t, root, address, withHops(dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "[192.0.2.99]",
		"Name service error for name=nowhere.example type=MX: Host not found"), hopForeign), 1, time.Now().UTC())
	sources := NewSendingSources([]string{"198.51.100.0/24"}, nil, nil, nil)
	hosts := &fakeSourceHosts{}
	var lines []string
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) },
		WithSendingSources(sources), WithSourceHostResolver(hosts), WithDNS(&fakeVerifier{outcome: DNSOutcomeExists, ok: true}, sources)); err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "sending sources:") {
			t.Errorf("progress line %q, want none about the sending sources", line)
		}
	}
	if got := dnsLines(lines); len(got) != 1 || got[0] != dnsLineVerified {
		t.Errorf("dns line %q, want %q", got, dnsLineVerified)
	}
}

// TestJunkProgressLine checks the line that counts the messages the rules
// about delivery-failure notices took for junk in a run: every rule named
// with its count, other rules not counted, no line without any, and the
// same line for a grouping and a reindex of the same mail (a later grouping
// with nothing new prints none).
func TestJunkProgressLine(t *testing.T) {
	var c noticeJunkCounts
	var lines []string
	c.reportSummary(func(s string) { lines = append(lines, s) })
	for _, rule := range []string{ruleFakeNoticeLink, ruleBackscatter, ruleFakeNoticeLink, ruleFakeNoticeAuth, rulePhishingLink, ruleSpamFlag, ""} {
		c.add(rule)
	}
	c.reportSummary(func(s string) { lines = append(lines, s) })
	if want := []string{"junk: 1 backscatter, 2 fake_notice_link, 1 fake_notice_auth"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines %q, want %q", lines, want)
	}

	root := t.TempDir()
	address := "newsletter@example.jp"
	storeRaw(t, root, address, readSample(t, "postfix_backscatter.eml"), 1, time.Now().UTC())
	storeRaw(t, root, address, readSample(t, "postfix_dsn.eml"), 2, time.Now().UTC())
	sources := NewSendingSources(nil, nil, []string{"smtp.example.jp"}, nil)
	const want = "junk: 2 backscatter, 0 fake_notice_link, 0 fake_notice_auth" // the hop of the notice about sent mail is not known without lookups
	for _, run := range []struct {
		name string
		run  func(Progress) error
		want bool
	}{
		{"grouping", func(p Progress) error {
			_, err := GroupMailbox(context.Background(), root, address, false, nil, p, WithSendingSources(sources))
			return err
		}, true},
		{"grouping with nothing new", func(p Progress) error {
			_, err := GroupMailbox(context.Background(), root, address, false, nil, p, WithSendingSources(sources))
			return err
		}, false},
		{"reindex", func(p Progress) error {
			_, err := Reindex(context.Background(), root, address, nil, p, WithSendingSources(sources))
			return err
		}, true},
	} {
		var lines []string
		if err := run.run(func(s string) { lines = append(lines, s) }); err != nil {
			t.Fatalf("%s: %v", run.name, err)
		}
		if got := slices.Contains(lines, want); got != run.want {
			t.Errorf("%s: line %q present %v, want %v:\n%s", run.name, want, got, run.want, strings.Join(lines, "\n"))
		}
	}
}

// TestLookupsSharedByBothJudgements: a run with a return notice of forged
// mail and a verified DNS lookup failure looks every source host name and
// every domain up once, for both judgements.
func TestLookupsSharedByBothJudgements(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	storeRaw(t, root, address, readSample(t, "postfix_backscatter.eml"), 1, time.Now().UTC())
	storeRaw(t, root, address, dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "[192.0.2.99]",
		"Name service error for name=nowhere.example type=MX: Host not found"), 2, time.Now().UTC())
	storeRaw(t, root, address, dnsNotice(bounceKindFailed, "5.4.4", "m@nowhere.example", "[192.0.2.98]",
		"Name service error for name=nowhere.example type=MX: Host not found"), 3, time.Now().UTC())
	sources := NewSendingSources(nil, nil, []string{"mx9.example.jp", "mx8.example.jp"}, nil)
	hosts := &fakeSourceHosts{}
	spf := &fakeSourceSPF{complete: true}
	var lines []string
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) },
		WithSendingSources(sources), WithSourceHostResolver(hosts), WithSourceSPF(spf, []string{"example.jp", "example.com"}),
		WithDNS(&fakeVerifier{outcome: DNSOutcomeExists, ok: true}, sources)); err != nil {
		t.Fatal(err)
	}
	slices.Sort(hosts.calls) // looked up at the same time
	if !reflect.DeepEqual(hosts.calls, []string{"mx8.example.jp", "mx9.example.jp"}) || !reflect.DeepEqual(spf.calls, []string{"example.jp", "example.com"}) {
		t.Errorf("host lookups %v, SPF lookups %v; want each once", hosts.calls, spf.calls)
	}
	want := "sending sources: 0 address(es) of 2 host name(s), 0 SPF prefix(es) and 0 SPF host name(s) of 2 domain(s)"
	if !strings.Contains(strings.Join(lines, "\n"), want) {
		t.Errorf("progress lacks %q:\n%s", want, strings.Join(lines, "\n"))
	}
}

// TestSendingSourcesWithOtherHosts: the SMTP hosts of the other mailboxes match
// as text (Match, MatchReceived) and are looked up with the host names of
// the mailbox, but configure nothing for it: without a source configured
// for the mailbox the configuration stays empty (every reporting MTA is a
// sending server, 4.4) and no
// host name switches the backscatter rule on (2.6). The original is not
// changed.
func TestSendingSourcesWithOtherHosts(t *testing.T) {
	none := NewSendingSources(nil, nil, nil, nil)
	other := none.WithOtherHosts([]string{"MX-A.Example.JP.", " "})
	if !other.Empty() || other.hasHosts() || len(none.otherHosts) != 0 {
		t.Errorf("other hosts configure the mailbox: %+v (original %+v)", other, none)
	}
	if !other.Match("relay.partner.example.net") {
		t.Error("an empty configuration with other hosts no longer takes every reporting MTA for a sending server")
	}
	if !reflect.DeepEqual(other.hostNames(), []string{"mx-a.example.jp"}) {
		t.Errorf("host names to look up %v", other.hostNames())
	}

	sources := NewSendingSources(nil, nil, []string{"mx-b.example.jp"}, nil).WithOtherHosts([]string{"mx-a.example.jp", "mx-b.example.jp"})
	if sources.Empty() || !sources.hasHosts() || len(sources.otherHosts) != 1 {
		t.Errorf("sending sources %+v", sources)
	}
	for mta, want := range map[string]bool{"mx-a.example.jp": true, "mx-b.example.jp": true, "mx-c.example.jp": false} {
		if got := sources.Match(mta); got != want {
			t.Errorf("Match(%q) = %v, want %v", mta, got, want)
		}
	}
	if !sources.MatchReceived("from app01 (app01 [192.0.2.20]) by mx-a.example.jp with ESMTP") ||
		sources.MatchReceived("from app01 (app01 [192.0.2.20]) by mx-c.example.jp with ESMTP") {
		t.Error("MatchReceived does not use the other hosts")
	}
	if !reflect.DeepEqual(sources.hostNames(), []string{"mx-a.example.jp", "mx-b.example.jp"}) {
		t.Errorf("host names to look up %v", sources.hostNames())
	}
	if l := newSourceLookups(&fakeSourceHosts{}, other, nil, nil); l == nil || !reflect.DeepEqual(l.hosts, []string{"mx-a.example.jp"}) {
		t.Errorf("lookups of the other hosts alone: %+v", l)
	}
}
