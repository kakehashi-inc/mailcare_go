package mailengine

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
)

// verifyCall is one call of the fake verifier.
type verifyCall struct {
	failure DNSFailure
	domain  string
	at      time.Time
	direct  bool
}

// The dns progress lines of a run with one notice: verified (or reused, by
// a verifier that does not tell), or left inconclusive.
const (
	dnsLineVerified = "dns: 1 notice(s): 1 verified, 0 reused from stored results, 0 over budget (run again), " +
		"0 not applicable, 0 inconclusive, 0 too old for a live check"
	dnsLineInconclusive = "dns: 1 notice(s): 0 verified, 0 reused from stored results, 0 over budget (run again), " +
		"0 not applicable, 1 inconclusive, 0 too old for a live check"
)

// fakeVerifier answers every name with the same outcome and records the
// calls; probe, when set, runs inside each call.
type fakeVerifier struct {
	outcome DNSOutcome
	ok      bool
	probe   func()
	calls   []verifyCall
}

func (f *fakeVerifier) Verify(_ context.Context, failure DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (DNSOutcome, bool) {
	f.calls = append(f.calls, verifyCall{failure, recipientDomain, noticeTime, direct})
	if f.probe != nil {
		f.probe()
	}
	return f.outcome, f.ok
}

// dnsNotice builds a Postfix-style delivery status notification for one
// recipient: action failed or delayed, the status code, the reporting MTA
// and a diagnostic that may report a DNS lookup failure.
func dnsNotice(action, status, recipient, reportingMTA, diagnostic string) []byte {
	return dnsNoticeWith(action, status, recipient, reportingMTA, diagnostic, "")
}

// dnsNoticeWith is dnsNotice whose returned headers (text/rfc822-headers)
// start with originalHeaders (Received, DKIM-Signature, ...; "" for none).
func dnsNoticeWith(action, status, recipient, reportingMTA, diagnostic, originalHeaders string) []byte {
	subject := "Undelivered Mail Returned to Sender"
	if action == bounceKindDelayed {
		subject = "Delayed Mail (still being retried)"
	}
	const b = "=_dns"
	return []byte("Return-Path: <>\r\n" +
		"From: MAILER-DAEMON@mx1.example.jp (Mail Delivery System)\r\n" +
		"To: newsletter@example.jp\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: Tue, 2 Sep 2025 09:15:30 +0900\r\n" +
		"Message-ID: <dns-" + action + "@mx1.example.jp>\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"" + b + "\"\r\n\r\n" +
		"--" + b + "\r\nContent-Type: text/plain; charset=us-ascii\r\n\r\n" +
		"This is the mail system at host mx1.example.jp.\r\n\r\n" +
		"<" + recipient + ">: " + diagnostic + "\r\n" +
		"\r\n--" + b + "\r\nContent-Type: message/delivery-status\r\n\r\n" +
		"Reporting-MTA: dns; " + reportingMTA + "\r\n\r\n" +
		"Final-Recipient: rfc822; " + recipient + "\r\n" +
		"Action: " + action + "\r\n" +
		"Status: " + status + "\r\n" +
		"Diagnostic-Code: X-Postfix; " + diagnostic + "\r\n" +
		"\r\n--" + b + "\r\nContent-Type: text/rfc822-headers\r\n\r\n" + originalHeaders +
		"From: Newsletter <newsletter@example.jp>\r\nTo: " + recipient + "\r\nSubject: Weekly digest\r\n" +
		"Date: Tue, 2 Sep 2025 09:10:00 +0900\r\nMessage-ID: <digest-0005@example.jp>\r\n" +
		"\r\n--" + b + "--\r\n")
}

// storeRaw stores one raw message with classified = 0 under the given UID
// and INTERNALDATE.
func storeRaw(t *testing.T, root, address string, raw []byte, uid uint32, received time.Time) (*models.Message, *ParsedMessage) {
	t.Helper()
	db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	src := Source{Folder: "INBOX", UIDValidity: 1, UID: uid, ReceivedAt: received, FetchedAt: time.Now().UTC()}
	msg, pm, err := storeMessage(db, MailboxDir(root, address), raw, src, storeOptions{writeEML: true})
	if err != nil {
		t.Fatal(err)
	}
	return msg, pm
}

// dnsLines returns the progress lines of the DNS verification.
func dnsLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "dns:") {
			out = append(out, l)
		}
	}
	return out
}

func TestOwnSourcesMatch(t *testing.T) {
	own4 := NewOwnSources([]string{"192.0.2.25", "198.51.100.0/24"}, nil, nil, nil)
	own6 := NewOwnSources(nil, []string{"2001:db8::25", "2001:db8:1::/48"}, nil, nil)
	ownHosts := NewOwnSources(nil, nil, []string{"mx1.example.jp", "MX2.Example.JP."}, nil)
	ownResolved := NewOwnSources(nil, nil, []string{"mx1.example.jp"}, []netip.Addr{
		netip.MustParseAddr("203.0.113.25"), netip.MustParseAddr("2001:db8:2::25"), netip.MustParseAddr("::ffff:203.0.113.26"),
	})
	cases := []struct {
		name string
		own  OwnSources
		mta  string
		want bool
	}{
		// Nothing configured: every reporting MTA is our own (4.4).
		{"empty config", OwnSources{}, "relay.provider.example.net", true},
		{"empty config, empty mta", NewOwnSources(nil, nil, nil, nil), "", true},
		{"blank entries are no configuration", NewOwnSources([]string{" "}, []string{""}, []string{" "}, nil), "relay.example.net", true},
		{"resolved addresses alone are no configuration", NewOwnSources(nil, nil, nil, []netip.Addr{netip.MustParseAddr("192.0.2.1")}), "relay.example.net", true},
		// Host names.
		{"host", ownHosts, "mx1.example.jp", true},
		{"host upper case and trailing dot", ownHosts, "MX1.EXAMPLE.JP.", true},
		{"configured host normalized", ownHosts, "mx2.example.jp", true},
		{"host with a comment", ownHosts, "mx1.example.jp (Postfix)", true},
		{"host with a port", ownHosts, "mx1.example.jp:25", true},
		{"other host", ownHosts, "mx3.example.jp", false},
		{"subdomain of a host", ownHosts, "a.mx1.example.jp", false},
		{"ip literal against host names only", ownHosts, "192.0.2.25", false},
		// IPv4.
		{"ipv4 address", own4, "192.0.2.25", true},
		{"ipv4 bracketed", own4, "[192.0.2.25]", true},
		{"ipv4 bracketed with a port", own4, "[192.0.2.25]:25", true},
		{"ipv4 with a port", own4, "192.0.2.25:25", true},
		{"ipv4 in parentheses", own4, "(192.0.2.25)", true},
		{"ipv4 other address", own4, "192.0.2.26", false},
		{"ipv4 cidr", own4, "198.51.100.200", true},
		{"ipv4 outside the cidr", own4, "198.51.101.1", false},
		{"ipv4-mapped ipv6 literal", own4, "[IPv6:::ffff:198.51.100.7]", true},
		{"ipv4-mapped ipv6 bare", own4, "::ffff:192.0.2.25", true},
		// IPv6.
		{"ipv6 address", own6, "2001:db8::25", true},
		{"ipv6 bracketed with tag", own6, "[IPv6:2001:db8::25]", true},
		{"ipv6 tag without brackets", own6, "IPv6:2001:DB8::25", true},
		{"ipv6 bracketed with a port", own6, "[2001:db8::25]:25", true},
		{"ipv6 cidr", own6, "2001:db8:1:ff::1", true},
		{"ipv6 outside the cidr", own6, "2001:db8:3::1", false},
		// Address families never match across.
		{"ipv4 against an ipv6 list", own6, "192.0.2.25", false},
		{"ipv6 against an ipv4 list", own4, "2001:db8::25", false},
		// Addresses of the own host names.
		{"resolved ipv4", ownResolved, "[203.0.113.25]", true},
		{"resolved ipv6", ownResolved, "IPv6:2001:db8:2::25", true},
		{"resolved ipv4-mapped stored unmapped", ownResolved, "203.0.113.26", true},
		{"resolved other address", ownResolved, "203.0.113.27", false},
		{"resolved list keeps the host names", ownResolved, "mx1.example.jp", true},
		// Unreadable reporting MTAs.
		{"empty mta", own4, "", false},
		{"blank mta", own4, "  ", false},
		{"garbage", own4, "<script>", false},
		{"unclosed bracket", own4, "[192.0.2.25", false},
		{"garbage after the bracket", own4, "[192.0.2.25]x", false},
		{"unparsable entry still counts as configured", NewOwnSources([]string{"not-an-ip"}, nil, nil, nil), "192.0.2.25", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.own.Match(c.mta); got != c.want {
				t.Errorf("Match(%q) = %v, want %v", c.mta, got, c.want)
			}
		})
	}
	for name, c := range map[string]struct {
		own  OwnSources
		want bool
	}{
		"zero value": {OwnSources{}, true},
		"ipv4":       {own4, false},
		"ipv6":       {own6, false},
		"hosts":      {ownHosts, false},
	} {
		if got := c.own.Empty(); got != c.want {
			t.Errorf("%s: Empty() = %v, want %v", name, got, c.want)
		}
	}
}

// fakeResolver answers the host names from tables and records the lookups
// (the HostResolver of the Checker).
type fakeResolver struct {
	hosts           map[string][]netip.Addr // ResolveHost answers (a name not listed is answered without addresses)
	unansweredHosts map[string]bool         // ResolveHost names whose lookup is not answered
	reporters       map[string][]netip.Addr // ResolveReporter answers (a name not listed is answered without addresses)
	unanswered      map[string]bool         // ResolveReporter names whose lookup is not answered
	mu              sync.Mutex              // ResolveHost is asked for several names at once
	hostCalls       []string
	reporterCalls   []string
}

func (f *fakeResolver) ResolveHost(_ context.Context, host string) ([]netip.Addr, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostCalls = append(f.hostCalls, host)
	return f.hosts[host], !f.unansweredHosts[host]
}

func (f *fakeResolver) ResolveReporter(_ context.Context, host string) ([]netip.Addr, bool) {
	f.reporterCalls = append(f.reporterCalls, host)
	if f.unanswered[host] {
		return nil, false
	}
	return f.reporters[host], true
}

// resolvingVerifier is a fake verifier that also resolves host names, as
// the Checker does.
type resolvingVerifier struct {
	fakeVerifier
	fakeResolver
}

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

// TestOwnMatcherResolves checks the matching of a run with a resolver: a
// reporting MTA given as a host name that is none of the own host names is
// own when one of its addresses lies in an own prefix or is an address of
// an own host name, and someone else's when it has addresses and none of
// them is ours; a name that resolves to nothing (or is no name at all)
// leaves the question open; the own host names are resolved only when an
// address has to be compared with them, once per run; nothing is resolved
// when the text decides.
func TestOwnMatcherResolves(t *testing.T) {
	ipv4 := NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil)
	ipv6 := NewOwnSources(nil, []string{"2001:db8::/32"}, nil, nil)
	hosts := NewOwnSources(nil, nil, []string{"mx1.example.jp", "mailhost"}, nil)
	reporters := map[string][]netip.Addr{
		"mail.example.jp":   addrs("192.0.2.25"),
		"mail6.example.jp":  addrs("2001:db8::25"),
		"mapped.example.jp": addrs("::ffff:192.0.2.26"),
		"other.example.net": addrs("198.51.100.1", "2001:db8:ffff::1"),
		"relay.example.jp":  addrs("203.0.113.25"),
		"relay2.example.jp": addrs("203.0.113.99", "10.0.0.5"),
		"elsewhere.example": addrs("203.0.113.200"),
	}
	ownHosts := map[string][]netip.Addr{
		"mx1.example.jp": addrs("203.0.113.25"),
		"mailhost":       addrs("::ffff:10.0.0.5"),
	}
	cases := []struct {
		name          string
		own           OwnSources
		mta           string
		want, known   bool
		reporterCalls int
		hostCalls     int
	}{
		{"ipv4 config, name resolves into the prefix", ipv4, "mail.example.jp", true, true, 1, 0},
		{"ipv4 config, mapped address resolves into the prefix", ipv4, "mapped.example.jp (Postfix)", true, true, 1, 0},
		{"ipv4 config, name resolves elsewhere", ipv4, "other.example.net", false, true, 1, 0},
		{"ipv4 config, name does not resolve", ipv4, "unknown.example.jp", false, false, 1, 0},
		{"ipv6 config, name resolves into the prefix", ipv6, "mail6.example.jp", true, true, 1, 0},
		{"hosts config, own host name as text", hosts, "MX1.example.jp.", true, true, 0, 0},
		{"hosts config, another name with the address of an own host", hosts, "relay.example.jp", true, true, 1, 2},
		{"hosts config, address of a single-label own host", hosts, "relay2.example.jp", true, true, 1, 2},
		{"hosts config, another name elsewhere", hosts, "elsewhere.example", false, true, 1, 2},
		{"hosts config, name does not resolve: own hosts not resolved", hosts, "unknown.example.jp", false, false, 1, 0},
		{"hosts config, ip literal of an own host", hosts, "[203.0.113.25]", true, true, 0, 2},
		{"empty config", OwnSources{}, "anything.example.net", true, true, 0, 0},
		{"unparsable config only, name resolves", NewOwnSources([]string{"not-an-ip"}, nil, nil, nil), "mail.example.jp", false, true, 1, 0},
		{"unreadable reporting MTA", ipv4, "<script>", false, false, 1, 0},
		{"empty reporting MTA", ipv4, " ", false, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeResolver{hosts: ownHosts, reporters: reporters}
			m := ownMatcher{own: c.own, resolver: r}
			if got, known := m.match(context.Background(), c.mta, true); got != c.want || known != c.known {
				t.Errorf("match(%q) = %v, %v; want %v, %v", c.mta, got, known, c.want, c.known)
			}
			if len(r.reporterCalls) != c.reporterCalls || len(r.hostCalls) != c.hostCalls {
				t.Errorf("ResolveReporter %v, ResolveHost %v; want %d and %d calls", r.reporterCalls, r.hostCalls, c.reporterCalls, c.hostCalls)
			}
			// Without a resolver (Match) a name is compared as text only.
			if got, want := c.own.Match(c.mta), c.own.Empty() || c.mta == "MX1.example.jp."; got != want {
				t.Errorf("Match(%q) = %v, want %v", c.mta, got, want)
			}
		})
	}

	// The own host names are resolved once per run.
	r := &fakeResolver{hosts: ownHosts, reporters: reporters}
	m := ownMatcher{own: hosts, resolver: r}
	for _, mta := range []string{"[203.0.113.25]", "relay.example.jp", "elsewhere.example", "[198.51.100.7]"} {
		m.match(context.Background(), mta, true)
	}
	if len(r.hostCalls) != 2 {
		t.Errorf("own host names resolved %v, want once each", r.hostCalls)
	}
	// A cancellation leaves them unresolved for the next notice.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = &fakeResolver{hosts: ownHosts, reporters: reporters}
	m = ownMatcher{own: hosts, resolver: r}
	m.match(ctx, "[203.0.113.25]", true)
	if own, _ := m.match(context.Background(), "[203.0.113.25]", true); !own || len(r.hostCalls) != 4 {
		t.Errorf("after a cancellation: ResolveHost %v", r.hostCalls)
	}
}

// TestOwnMatcherUnknown checks the reporting MTAs whose server cannot be
// told: a host name whose lookup was not answered (the limits of the run,
// no resolver answered, a cancellation), was refused by the resolver (a
// single-label name), or answered without any address (NXDOMAIN, no
// data), a name not looked up because the notice is too old for live
// lookups or because there is no resolver at all, and an empty or
// unreadable reporting MTA: neither ours nor someone else's (known false).
// The text or an IP literal decides without any lookup, and addresses that
// were found and are none of ours make it someone else's.
func TestOwnMatcherUnknown(t *testing.T) {
	ipv4 := NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil)
	hosts := NewOwnSources(nil, nil, []string{"mx1.example.jp"}, nil)
	for _, c := range []struct {
		name        string
		own         OwnSources
		mta         string
		live        bool
		noResolver  bool
		own2, known bool
		lookups     int
	}{
		{"unanswered name", ipv4, "relay.example.net", true, false, false, false, 1},
		{"unanswered name, hosts configured", hosts, "relay.example.net", true, false, false, false, 1},
		{"refused single-label name", ipv4, "mailhost", true, false, false, false, 1},
		{"answered without addresses", ipv4, "nxdomain.example.net", true, false, false, false, 1},
		{"addresses, none of ours", ipv4, "other.example.net", true, false, false, true, 1},
		{"addresses, ours", ipv4, "ours.example.net", true, false, true, true, 1},
		{"too old for a live lookup", ipv4, "other.example.net", false, false, false, false, 0},
		{"no resolver", ipv4, "other.example.net", true, true, false, false, 0},
		{"empty", ipv4, "", true, false, false, false, 0},
		{"unreadable", ipv4, "[192.0.2.25", true, false, false, false, 0},
		{"own host name as text", hosts, "mx1.example.jp", false, true, true, true, 0},
		{"ip literal", ipv4, "[192.0.2.25]", false, true, true, true, 0},
		{"ip literal elsewhere", ipv4, "[198.51.100.1]", false, true, false, true, 0},
		{"nothing configured", OwnSources{}, "relay.example.net", true, false, true, true, 0},
		{"nothing configured, empty", OwnSources{}, "", true, false, true, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeResolver{
				reporters:  map[string][]netip.Addr{"other.example.net": addrs("198.51.100.1"), "ours.example.net": addrs("192.0.2.1")},
				unanswered: map[string]bool{"relay.example.net": true, "mailhost": true},
			}
			m := ownMatcher{own: c.own, resolver: r}
			if c.noResolver {
				m.resolver = nil
			}
			if own, known := m.match(context.Background(), c.mta, c.live); own != c.own2 || known != c.known {
				t.Errorf("match(%q) = %v, %v; want %v, %v", c.mta, own, known, c.own2, c.known)
			}
			if len(r.reporterCalls) != c.lookups {
				t.Errorf("ResolveReporter %v, want %d calls", r.reporterCalls, c.lookups)
			}
		})
	}
}

// TestGroupMailboxOwnByAddress files a DNS lookup failure whose reporting
// MTA is a host name through the grouping with a verifier that resolves
// names: with only own_sending_ipv4 configured, the notice is our own
// resolver failure when the name resolves into the prefix and external when
// it resolves elsewhere. When the name resolves to nothing, or its lookup
// is not answered (the limits of the run, no resolver answered), whether
// the MTA is ours cannot be told: the notice stays not verified and is
// counted so.
func TestGroupMailboxOwnByAddress(t *testing.T) {
	notice := dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "mail.example.jp",
		"Name service error for name=nowhere.example type=MX: Host not found")
	for _, c := range []struct {
		name       string
		resolved   []netip.Addr
		unanswered bool
		category   string
		unit       string
		rule       string
		progress   string
	}{
		{"resolves into the prefix", addrs("192.0.2.25"), false, categoryDNSResolverFailure, "mail.example.jp",
			"dns_resolver_failure", dnsLineVerified},
		{"resolves elsewhere", addrs("198.51.100.25"), false, categoryDNSFailureExternal, "nowhere.example",
			"dns_failure_external", dnsLineVerified},
		{"does not resolve", nil, false, categoryDomainNotFound, "nowhere.example",
			"dns_unverified_not_found", dnsLineInconclusive},
		{"lookup not answered", nil, true, categoryDomainNotFound, "nowhere.example",
			"dns_unverified_not_found", dnsLineInconclusive},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			address := "newsletter@example.jp"
			msg, _ := storeRaw(t, root, address, notice, 1, time.Now().UTC())
			v := &resolvingVerifier{fakeVerifier: fakeVerifier{outcome: DNSOutcomeExists, ok: true},
				fakeResolver: fakeResolver{reporters: map[string][]netip.Addr{"mail.example.jp": c.resolved},
					unanswered: map[string]bool{"mail.example.jp": c.unanswered}}}
			var lines []string
			if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) },
				WithDNS(v, NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil))); err != nil {
				t.Fatal(err)
			}
			b, err := models.GetBounceByMessageID(mustOpenIndex(t, root, address), msg.ID)
			if err != nil {
				t.Fatal(err)
			}
			if want := GroupKey(c.category, c.unit, ""); b.GroupKey != want || b.CategoryRule != c.rule {
				t.Errorf("group_key = %s, rule %s; want %s (%s: %s), rule %s", b.GroupKey, b.CategoryRule, want, c.category, c.unit, c.rule)
			}
			if got := dnsLines(lines); len(got) != 1 || got[0] != c.progress {
				t.Errorf("progress %q, want %q", got, c.progress)
			}
			if len(v.reporterCalls) != 1 || v.reporterCalls[0] != "mail.example.jp" || len(v.hostCalls) != 0 {
				t.Errorf("ResolveReporter %v, ResolveHost %v", v.reporterCalls, v.hostCalls)
			}
		})
	}
}

// TestOwnHostsResolvedOnlyWhenNeeded checks that a run resolves nothing
// unless a notice with a verified DNS lookup failure needs it: not
// for bounces without a lookup failure, not for an unverified one, not for
// the outcomes the reporting MTA does not change (not_found, broken), and
// the own host names once for the whole run otherwise.
func TestOwnHostsResolvedOnlyWhenNeeded(t *testing.T) {
	own := NewOwnSources(nil, nil, []string{"mx9.example.jp"}, nil)
	failure := func(uid uint32, mta string) []byte {
		return dnsNotice(bounceKindFailed, "5.4.4", fmt.Sprintf("n%d@nowhere.example", uid), mta,
			"Name service error for name=nowhere.example type=MX: Host not found")
	}
	run := func(t *testing.T, raws [][]byte, outcome DNSOutcome, ok bool) *resolvingVerifier {
		t.Helper()
		root := t.TempDir()
		address := "newsletter@example.jp"
		for i, raw := range raws {
			storeRaw(t, root, address, raw, uint32(i+1), time.Now().UTC())
		}
		v := &resolvingVerifier{fakeVerifier: fakeVerifier{outcome: outcome, ok: ok},
			fakeResolver: fakeResolver{hosts: map[string][]netip.Addr{"mx9.example.jp": addrs("192.0.2.9")}}}
		if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, WithDNS(v, own)); err != nil {
			t.Fatal(err)
		}
		return v
	}
	none := func(t *testing.T, v *resolvingVerifier) {
		t.Helper()
		if len(v.hostCalls)+len(v.reporterCalls) != 0 {
			t.Errorf("ResolveHost %v, ResolveReporter %v; want no lookup", v.hostCalls, v.reporterCalls)
		}
	}
	t.Run("no lookup failure", func(t *testing.T) {
		none(t, run(t, [][]byte{readSample(t, "postfix_dsn.eml"), readSample(t, "postfix_delayed.eml")}, DNSOutcomeExists, true))
	})
	t.Run("not verified", func(t *testing.T) {
		none(t, run(t, [][]byte{failure(1, "[192.0.2.9]")}, DNSOutcomeExists, false))
	})
	t.Run("not found", func(t *testing.T) {
		none(t, run(t, [][]byte{failure(1, "[192.0.2.9]")}, DNSOutcomeNotFound, true))
	})
	t.Run("broken", func(t *testing.T) {
		none(t, run(t, [][]byte{failure(1, "relay.example.jp")}, DNSOutcomeBroken, true))
	})
	t.Run("exists, several notices", func(t *testing.T) {
		v := run(t, [][]byte{failure(1, "[192.0.2.9]"), failure(2, "[198.51.100.1]"), failure(3, "relay.example.jp")}, DNSOutcomeExists, true)
		if len(v.hostCalls) != 1 || v.hostCalls[0] != "mx9.example.jp" {
			t.Errorf("ResolveHost %v, want once", v.hostCalls)
		}
		if len(v.reporterCalls) != 1 || v.reporterCalls[0] != "relay.example.jp" {
			t.Errorf("ResolveReporter %v", v.reporterCalls)
		}
	})
}

func TestNoticeTime(t *testing.T) {
	date := time.Date(2025, 9, 3, 0, 5, 0, 0, time.UTC)
	received := date.Add(2 * time.Minute)
	if got := noticeTime(&models.Message{Date: date, ReceivedAt: models.NullTime(received)}); !got.Equal(received) {
		t.Errorf("with INTERNALDATE: %v, want %v", got, received)
	}
	if got := noticeTime(&models.Message{Date: date}); !got.Equal(date) {
		t.Errorf("without INTERNALDATE: %v, want %v", got, date)
	}
}

// TestGroupMailboxDNSVerification files DNS lookup failures through the
// grouping phase with a fake verifier (design document "mail
// classification" 4.4): the outcome and whether the reporting MTA is our own
// decide the category, the rule is recorded on the bounce, and the group
// counters and analysis flag follow the actionability of the category.
func TestGroupMailboxDNSVerification(t *testing.T) {
	const (
		postfixPermanent = "Host or domain name not found. Name service error for name=nowhere.example type=MX: Host not found"
		sendmailUnknown  = "550 5.1.2 <a@nowhere.example>... Host unknown (Name server: nowhere.example.: host not found)"
		sendmailDeferred = "Deferred: Name server: example.com.: host name lookup failure"
	)
	delayedDNS := readSample(t, "postfix_delayed_dns.eml")
	failedPostfix := dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "mx1.example.jp", postfixPermanent)
	failedSendmail := dnsNotice(bounceKindFailed, "5.1.2", "a@nowhere.example", "relay.example.edu", sendmailUnknown)
	deferredSendmail := dnsNotice(bounceKindDelayed, "4.4.3", "b@example.com", "relay.example.edu", sendmailDeferred)
	failedFromIP := dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "[192.0.2.25]", postfixPermanent)
	ownMX1 := NewOwnSources(nil, nil, []string{"mail.example.jp", "mx1.example.jp"}, nil)
	ownOther := NewOwnSources(nil, nil, []string{"mail.example.jp"}, nil)
	ownRelay := NewOwnSources(nil, nil, []string{"relay.example.edu"}, nil)
	ownNet := NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil)
	otherNet := NewOwnSources([]string{"198.51.100.0/24"}, nil, nil, nil)

	exists := func() *fakeVerifier { return &fakeVerifier{outcome: DNSOutcomeExists, ok: true} }
	elsewhere := map[string][]netip.Addr{"mx1.example.jp": addrs("203.0.113.1"), "relay.example.edu": addrs("203.0.113.2")}
	cases := []struct {
		name        string
		raw         []byte
		fake        *fakeVerifier           // nil: no verifier
		nilVerifier bool                    // WithDNS(nil, ...) instead of no option
		reporters   map[string][]netip.Addr // non-nil: the verifier resolves reporting MTAs from it
		own         OwnSources
		wantName    string // name the verifier must receive
		wantDomain  string // recipient domain it must receive
		category    string
		unit        string
		rule        string
		actionable  bool
	}{
		{name: "exists, nothing configured counts as own", raw: delayedDNS, fake: exists(),
			wantName: "infoseek.jp", wantDomain: "infoseek.jp",
			category: categoryDNSResolverFailure, unit: "mx1.example.jp", rule: "dns_resolver_failure", actionable: true},
		{name: "exists, reporting MTA configured", raw: delayedDNS, fake: exists(), own: ownMX1,
			wantName: "infoseek.jp", wantDomain: "infoseek.jp",
			category: categoryDNSResolverFailure, unit: "mx1.example.jp", rule: "dns_resolver_failure", actionable: true},
		{name: "exists, reporting MTA not configured", raw: delayedDNS, fake: exists(), own: ownOther, reporters: elsewhere,
			wantName: "infoseek.jp", wantDomain: "infoseek.jp",
			category: categoryDNSFailureExternal, unit: "infoseek.jp", rule: "dns_failure_external"},
		{name: "exists, reporting MTA not configured and not resolved", raw: delayedDNS, fake: exists(), own: ownOther,
			wantName: "infoseek.jp", wantDomain: "infoseek.jp",
			category: categoryDeliveryDelay, unit: "infoseek.jp", rule: "dns_unverified_delay"},
		{name: "broken", raw: delayedDNS, fake: &fakeVerifier{outcome: DNSOutcomeBroken, ok: true}, own: ownMX1,
			wantName: "infoseek.jp", wantDomain: "infoseek.jp",
			category: categoryDNSFailureExternal, unit: "infoseek.jp", rule: "dns_failure_external"},
		{name: "not found", raw: delayedDNS, fake: &fakeVerifier{outcome: DNSOutcomeNotFound, ok: true}, own: ownMX1,
			wantName: "infoseek.jp", wantDomain: "infoseek.jp",
			category: categoryDomainNotFound, unit: "infoseek.jp", rule: "dns_verified_not_found"},
		{name: "not verified delay", raw: delayedDNS, fake: &fakeVerifier{outcome: DNSOutcomeExists, ok: false},
			wantName: "infoseek.jp", wantDomain: "infoseek.jp",
			category: categoryDeliveryDelay, unit: "infoseek.jp", rule: "dns_unverified_delay"},
		{name: "nil verifier", raw: delayedDNS, nilVerifier: true,
			category: categoryDeliveryDelay, unit: "infoseek.jp", rule: "dns_unverified_delay"},
		{name: "no option", raw: delayedDNS,
			category: categoryDeliveryDelay, unit: "infoseek.jp", rule: "dns_unverified_delay"},
		// A permanent failure carries 5.4.4 / 5.1.2; the status code must not
		// hide a name that exists.
		{name: "postfix 5.4.4 exists, own", raw: failedPostfix, fake: exists(), own: ownMX1,
			wantName: "nowhere.example", wantDomain: "nowhere.example",
			category: categoryDNSResolverFailure, unit: "mx1.example.jp", rule: "dns_resolver_failure", actionable: true},
		{name: "postfix 5.4.4 not verified", raw: failedPostfix, fake: &fakeVerifier{ok: false},
			wantName: "nowhere.example", wantDomain: "nowhere.example",
			category: categoryDomainNotFound, unit: "nowhere.example", rule: "dns_unverified_not_found"},
		{name: "sendmail 5.1.2 exists, own", raw: failedSendmail, fake: exists(), own: ownRelay,
			wantName: "nowhere.example", wantDomain: "nowhere.example",
			category: categoryDNSResolverFailure, unit: "relay.example.edu", rule: "dns_resolver_failure", actionable: true},
		{name: "sendmail 5.1.2 without verifier", raw: failedSendmail,
			category: categoryDomainNotFound, unit: "nowhere.example", rule: "dns_unverified_not_found"},
		{name: "sendmail deferred exists, other MTA", raw: deferredSendmail, fake: exists(), own: ownMX1, reporters: elsewhere,
			wantName: "example.com", wantDomain: "example.com",
			category: categoryDNSFailureExternal, unit: "example.com", rule: "dns_failure_external"},
		{name: "ip literal reporting MTA in own network", raw: failedFromIP, fake: exists(), own: ownNet,
			wantName: "nowhere.example", wantDomain: "nowhere.example",
			category: categoryDNSResolverFailure, unit: "[192.0.2.25]", rule: "dns_resolver_failure", actionable: true},
		{name: "ip literal reporting MTA outside own network", raw: failedFromIP, fake: exists(), own: otherNet,
			wantName: "nowhere.example", wantDomain: "nowhere.example",
			category: categoryDNSFailureExternal, unit: "nowhere.example", rule: "dns_failure_external"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			address := "newsletter@example.jp"
			// Recent enough for the reporting MTA to be resolved.
			received := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			msg, _ := storeRaw(t, root, address, c.raw, 1, received)
			var opts []GroupOption
			switch {
			case c.fake != nil && c.reporters != nil:
				v := &resolvingVerifier{fakeVerifier: *c.fake, fakeResolver: fakeResolver{reporters: c.reporters}}
				c.fake = &v.fakeVerifier
				opts = append(opts, WithDNS(v, c.own))
			case c.fake != nil:
				opts = append(opts, WithDNS(c.fake, c.own))
			case c.nilVerifier:
				opts = append(opts, WithDNS(nil, c.own))
			}
			var lines []string
			res, err := GroupMailbox(context.Background(), root, address, false, nil, func(m string) { lines = append(lines, m) }, opts...)
			if err != nil {
				t.Fatal(err)
			}
			db := mustOpenIndex(t, root, address)
			b, err := models.GetBounceByMessageID(db, msg.ID)
			if err != nil {
				t.Fatalf("bounce: %v", err)
			}
			if b.CategoryRule != c.rule {
				t.Errorf("category_rule = %q, want %q", b.CategoryRule, c.rule)
			}
			wantKey := GroupKey(c.category, c.unit, "")
			if b.GroupKey != wantKey {
				t.Errorf("group_key = %s, want %s (%s: %s)", b.GroupKey, wantKey, c.category, c.unit)
			}
			g, err := models.GetGroup(db, wantKey)
			if err != nil {
				t.Fatalf("group %s: %s: %v", c.category, c.unit, err)
			}
			if g.Category != c.category || g.UnitValue != c.unit || g.Actionable != c.actionable || g.MessageCount != 1 {
				t.Errorf("group = %+v, want %s / %s actionable=%v with 1 message", g, c.category, c.unit, c.actionable)
			}
			// Only an actionable group is flagged for analysis and reported
			// as touched.
			if g.NeedsAnalysis != c.actionable || slices.Contains(res.GroupsTouched, wantKey) != c.actionable {
				t.Errorf("needs_analysis = %v, touched = %v; want flagged = %v", g.NeedsAnalysis, res.GroupsTouched, c.actionable)
			}
			got := dnsLines(lines)
			if c.fake == nil {
				if len(got) != 0 {
					t.Errorf("progress without a verifier: %v", got)
				}
				return
			}
			if len(c.fake.calls) != 1 {
				t.Fatalf("verifier called %d times, want once", len(c.fake.calls))
			}
			call := c.fake.calls[0]
			if call.failure.Name != c.wantName || call.domain != c.wantDomain || !call.at.Equal(received) {
				t.Errorf("verifier got %+v, want name %s, domain %s, time %v", call, c.wantName, c.wantDomain, received)
			}
			want := dnsLineVerified
			if strings.HasPrefix(c.rule, "dns_unverified_") {
				want = dnsLineInconclusive
			}
			if len(got) != 1 || got[0] != want {
				t.Errorf("progress = %v, want [%s]", got, want)
			}
		})
	}
}

// TestDNSVerifierNotCalled checks that only failed and delayed notices whose
// diagnostic reports a DNS lookup failure reach the verifier: never junk
// (even a fake notice quoting a lookup failure), a bounce without one, a
// DMARC report or an automatic reply.
func TestDNSVerifierNotCalled(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	dnsText := "Name service error for name=partner.example.com type=MX: Host not found, try again."
	junk := plainMessage("From: Mail Delivery System <mailer-daemon@evil.example>\r\nTo: newsletter@example.jp\r\n"+
		"Subject: Undelivered Mail Returned to Sender\r\n",
		"3 incoming messages for newsletter@example.jp are on hold. "+dnsText+
			" Release them: https://release.mailportal.example.net/inbox?id=4711")
	autoReply := plainMessage("From: Hanako <hanako@partner.example.com>\r\nTo: newsletter@example.jp\r\n"+
		"Subject: Automatic reply: Weekly digest\r\nAuto-Submitted: auto-replied\r\n",
		"I am out of the office until next week. "+dnsText)
	report := dmarcMail("application/gzip", "r.xml.gz", gzipped(t, dmarcXML(recSPFMissing)))
	inputs := []struct {
		raw  []byte
		kind string
	}{
		{junk, bounceKindJunk},
		{readSample(t, "postfix_dsn.eml"), bounceKindFailed},
		{readSample(t, "postfix_delayed.eml"), bounceKindDelayed},
		{report, bounceKindReport},
		{autoReply, bounceKindAutoReply},
	}
	for i, in := range inputs {
		storeRaw(t, root, address, in.raw, uint32(i+1), time.Now().UTC())
	}
	fake := &fakeVerifier{outcome: DNSOutcomeExists, ok: true}
	var lines []string
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(m string) { lines = append(lines, m) },
		WithDNS(fake, OwnSources{})); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("verifier called for %+v", fake.calls)
	}
	if got := dnsLines(lines); len(got) != 0 {
		t.Errorf("progress without any name checked: %v", got)
	}
	db := mustOpenIndex(t, root, address)
	var kinds []string
	for _, m := range mustListAllMessages(t, db) {
		kinds = append(kinds, m.BounceKind)
	}
	for _, in := range inputs {
		if !slices.Contains(kinds, in.kind) {
			t.Errorf("no message classified as %s: %v", in.kind, kinds)
		}
	}
}

// TestDNSVerifiedBeforeTransaction checks that the verifier runs while the
// grouping phase holds no write transaction on the index (it may use the
// network): from inside the verifier a second connection can take the write
// lock at once, and a message driven through groupMessage directly has no
// connection of the index in use.
func TestDNSVerifiedBeforeTransaction(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	received := time.Date(2025, 9, 3, 0, 7, 0, 0, time.UTC)
	storeRaw(t, root, address, readSample(t, "spamhaus_block_a.eml"), 1, received)
	storeRaw(t, root, address, readSample(t, "postfix_delayed_dns.eml"), 2, received)
	path := MailboxIndexPath(root, address)
	probe := func() error {
		ctx := context.Background()
		other, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(50)")
		if err != nil {
			return err
		}
		defer other.Close()
		conn, err := other.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&n); err != nil || n == 0 {
			return fmt.Errorf("the probe does not see the index: %d messages (%v)", n, err)
		}
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}

	// The probe fails while a write transaction is open on the index.
	db := mustOpenIndex(t, root, address)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE messages SET subject = subject`); err != nil {
		t.Fatal(err)
	}
	if err := probe(); err == nil {
		t.Fatal("the probe took the write lock while a write transaction was open")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	// Through GroupMailbox the probe succeeds inside the verifier.
	var probeErr error
	fake := &fakeVerifier{outcome: DNSOutcomeExists, ok: true}
	fake.probe = func() { probeErr = probe() }
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, WithDNS(fake, OwnSources{})); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("verifier called %d times, want once", len(fake.calls))
	}
	if probeErr != nil {
		t.Errorf("write lock not available inside the verifier: %v", probeErr)
	}

	// Driven directly: no connection of the index is in use (a transaction
	// would hold one) while the verifier runs.
	msg, pm := storeRaw(t, root, address, dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "mx1.example.jp",
		"Name service error for name=nowhere.example type=MX: Host not found"), 3, received)
	inUse := -1
	direct := &fakeVerifier{outcome: DNSOutcomeExists, ok: true}
	direct.probe = func() { inUse = db.Stats().InUse }
	if err := groupMessage(context.Background(), db, msg, pm, newGroupTracker(nil), groupConfig{dns: &dnsVerification{verifier: direct}}, address); err != nil {
		t.Fatal(err)
	}
	if len(direct.calls) != 1 || inUse != 0 {
		t.Errorf("verifier called %d times with %d connections in use, want once with none", len(direct.calls), inUse)
	}
}

// TestDNSVerificationFullRegroupAndReindex checks that a full regroup and a
// reindex verify the notice again and file it into the same group, and that
// the verifier gets the INTERNALDATE of the notice (carried over by the
// reindex) rather than its Date header.
func TestDNSVerificationFullRegroupAndReindex(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	received := time.Date(2025, 9, 3, 0, 7, 0, 0, time.UTC) // the Date header is 00:05 UTC
	msg, _ := storeRaw(t, root, address, readSample(t, "postfix_delayed_dns.eml"), 1, received)
	fake := &fakeVerifier{outcome: DNSOutcomeExists, ok: true}
	opt := WithDNS(fake, NewOwnSources(nil, nil, []string{"mx1.example.jp"}, nil))
	wantKey := GroupKey(categoryDNSResolverFailure, "mx1.example.jp", "")
	runs := []struct {
		name string
		run  func(progress Progress) error
	}{
		{"incremental", func(p Progress) error {
			_, err := GroupMailbox(context.Background(), root, address, false, nil, p, opt)
			return err
		}},
		{"full regroup", func(p Progress) error {
			_, err := GroupMailbox(context.Background(), root, address, true, nil, p, opt)
			return err
		}},
		{"reindex", func(p Progress) error {
			_, err := Reindex(context.Background(), root, address, nil, p, opt)
			return err
		}},
	}
	for _, r := range runs {
		fake.calls = nil
		var lines []string
		if err := r.run(func(m string) { lines = append(lines, m) }); err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if len(fake.calls) != 1 {
			t.Fatalf("%s: verifier called %d times, want once", r.name, len(fake.calls))
		}
		call := fake.calls[0]
		if call.failure != (DNSFailure{Name: "infoseek.jp", QType: "MX", Temporary: true}) || call.domain != "infoseek.jp" ||
			!call.at.Equal(received) {
			t.Errorf("%s: verifier got %+v, want infoseek.jp / MX at %v", r.name, call, received)
		}
		if got := dnsLines(lines); len(got) != 1 || got[0] != dnsLineVerified {
			t.Errorf("%s: progress = %v", r.name, got)
		}
		db, err := models.OpenMailIndex(MailboxIndexPath(root, address))
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := models.GetMessageByKey(db, msg.MessageKey)
		if err != nil {
			db.Close()
			t.Fatalf("%s: %v", r.name, err)
		}
		b, err := models.GetBounceByMessageID(db, fresh.ID)
		g, gerr := models.GetGroup(db, wantKey)
		db.Close()
		if err != nil || b.GroupKey != wantKey || b.CategoryRule != "dns_resolver_failure" {
			t.Errorf("%s: bounce = %+v (err %v), want the resolver failure of mx1.example.jp", r.name, b, err)
		}
		if gerr != nil || g.Category != categoryDNSResolverFailure || g.MessageCount != 1 {
			t.Errorf("%s: group = %+v (err %v)", r.name, g, gerr)
		}
	}
}

// TestDNSVerificationCancelled checks that a cancellation during the
// verification writes nothing: the notice is not filed as not verified but
// stays unclassified for the next run, and a reindex keeps the previous
// index.
func TestDNSVerificationCancelled(t *testing.T) {
	root := t.TempDir()
	address := "newsletter@example.jp"
	msg, _ := storeRaw(t, root, address, readSample(t, "postfix_delayed_dns.eml"), 1, time.Now().UTC())

	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeVerifier{ok: false, probe: cancel}
	if _, err := GroupMailbox(ctx, root, address, false, nil, nil, WithDNS(fake, OwnSources{})); err != context.Canceled {
		t.Fatalf("GroupMailbox cancelled in the verifier returned %v, want context.Canceled", err)
	}
	db := mustOpenIndex(t, root, address)
	assertUntouched(t, db, msg, false, "after a cancelled verification")

	ctx, cancel = context.WithCancel(context.Background())
	fake = &fakeVerifier{ok: false, probe: cancel}
	if _, err := Reindex(ctx, root, address, nil, nil, WithDNS(fake, OwnSources{})); err != context.Canceled {
		t.Fatalf("Reindex cancelled in the verifier returned %v, want context.Canceled", err)
	}
	assertUntouched(t, db, msg, false, "after a cancelled reindex")
}

// TestDirectOnlyForOurMail checks when the grouping lets the verifier ask
// authoritative servers directly (DNSVerifier, direct): only when sources
// are configured and the notice shows mail of ours without any lookup, by
// its reporting MTA (as text or an IP literal), a Received header of the
// returned original (within the bounds), or a DKIM signature of the
// monitored domain on it. A forged notice that names a domain and a server
// of its author's choosing never does, and neither does any notice without
// configured sources.
func TestDirectOnlyForOurMail(t *testing.T) {
	const diagnostic = "Name service error for name=nowhere.example type=MX: Host not found"
	hosts := NewOwnSources(nil, nil, []string{"mx1.example.jp"}, nil)
	network := NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil)
	ourHop := "Received: from app01.example.jp (app01.example.jp [198.51.100.20])\r\n\tby mx1.example.jp with ESMTP id 4Q1; Tue, 2 Sep 2025 09:10:01 +0900\r\n"
	ourAddress := "Received: from app01 (unknown [192.0.2.20]) by relay.example.net with ESMTP id 4Q2; Tue, 2 Sep 2025 09:10:01 +0900\r\n"
	otherHop := "Received: from mx.evil.example (mx.evil.example [203.0.113.66]) by mx2.evil.example; Tue, 2 Sep 2025 09:10:01 +0900\r\n"
	dkim := func(domain string) string {
		return "DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; d=" + domain + "; s=sel; h=from:to; bh=abc=; b=def=\r\n"
	}
	tooMany := ourHop + strings.Repeat(otherHop, maxReceivedLines)
	for _, c := range []struct {
		name      string
		own       OwnSources
		mta       string
		recipient string
		headers   string
		direct    bool
	}{
		// (Received headers of others with an own host name configured would
		// make the notice backscatter, never verified.)
		{"forged: name, recipient and server of the author", hosts, "mx.evil.example", "n@evil.example", "", false},
		{"forged, with Received headers of others", network, "mx.evil.example", "n@evil.example", otherHop, false},
		{"nothing configured", OwnSources{}, "mx1.example.jp", "n@nowhere.example", ourHop + dkim("example.jp"), false},
		{"reporting MTA is an own host", hosts, "MX1.example.jp.", "n@nowhere.example", "", true},
		{"reporting MTA in an own prefix", network, "[192.0.2.25]", "n@nowhere.example", "", true},
		{"reporting MTA outside", network, "[198.51.100.25]", "n@nowhere.example", "", false},
		{"Received shows an own host", hosts, "mx.evil.example", "n@nowhere.example", otherHop + ourHop, true},
		{"Received shows an own address", network, "mx.evil.example", "n@nowhere.example", ourAddress, true},
		{"Received past the bounds", hosts, "mx.evil.example", "n@nowhere.example", tooMany, false},
		{"DKIM of the monitored domain", hosts, "mx.evil.example", "n@nowhere.example", dkim("example.jp"), true},
		{"DKIM of a subdomain of it", network, "mx.evil.example", "n@nowhere.example", dkim("Mail.Example.JP"), true},
		{"DKIM of another domain", hosts, "mx.evil.example", "n@nowhere.example", dkim("evil.example"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			address := "newsletter@example.jp"
			storeRaw(t, root, address, dnsNoticeWith(bounceKindFailed, "5.4.4", c.recipient, c.mta, diagnostic, c.headers), 1, time.Now().UTC())
			fake := &fakeVerifier{outcome: DNSOutcomeNotFound, ok: true}
			if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, WithOwnSources(c.own), WithDNS(fake, c.own)); err != nil {
				t.Fatal(err)
			}
			if len(fake.calls) != 1 || fake.calls[0].direct != c.direct {
				t.Errorf("verifier calls %+v, want one with direct = %v", fake.calls, c.direct)
			}
		})
	}
}

func TestLiveCheckAllowed(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"zero", time.Time{}, true},
		{"in the future", now.Add(time.Hour), true},
		{"now", now, true},
		{"one hour", now.Add(-time.Hour), true},
		{"exactly 48 hours", now.Add(-DNSLiveMaxAge), true},
		{"48 hours and a second", now.Add(-DNSLiveMaxAge - time.Second), false},
		{"three days", now.Add(-72 * time.Hour), false},
	} {
		if got := LiveCheckAllowed(c.at, now); got != c.want {
			t.Errorf("%s: LiveCheckAllowed = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestReporterResolvedOnlyForRecentNotices: the reporting MTA of a verified
// DNS lookup failure is resolved only for a notice at most DNSLiveMaxAge
// old (by its INTERNALDATE); for an older one whose reporting MTA only a
// lookup could tell, nothing is looked up and the notice stays not
// verified, while the text of an own host name still decides without a
// lookup.
func TestReporterResolvedOnlyForRecentNotices(t *testing.T) {
	notice := dnsNotice(bounceKindFailed, "5.4.4", "n@nowhere.example", "mail.example.jp",
		"Name service error for name=nowhere.example type=MX: Host not found")
	for _, c := range []struct {
		name     string
		age      time.Duration
		own      OwnSources
		calls    int
		rule     string
		category string
		unit     string
	}{
		{"one hour", time.Hour, NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil), 1,
			"dns_resolver_failure", categoryDNSResolverFailure, "mail.example.jp"},
		{"47h59m", 48*time.Hour - time.Minute, NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil), 1,
			"dns_resolver_failure", categoryDNSResolverFailure, "mail.example.jp"},
		{"48h01m", 48*time.Hour + time.Minute, NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil), 0,
			"dns_unverified_not_found", categoryDomainNotFound, "nowhere.example"},
		{"three days", 72 * time.Hour, NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil), 0,
			"dns_unverified_not_found", categoryDomainNotFound, "nowhere.example"},
		{"three days, own host name as text", 72 * time.Hour, NewOwnSources(nil, nil, []string{"mail.example.jp"}, nil), 0,
			"dns_resolver_failure", categoryDNSResolverFailure, "mail.example.jp"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			address := "newsletter@example.jp"
			msg, _ := storeRaw(t, root, address, notice, 1, time.Now().UTC().Add(-c.age))
			v := &resolvingVerifier{fakeVerifier: fakeVerifier{outcome: DNSOutcomeExists, ok: true},
				fakeResolver: fakeResolver{reporters: map[string][]netip.Addr{"mail.example.jp": addrs("192.0.2.25")}}}
			if _, err := GroupMailbox(context.Background(), root, address, false, nil, nil, WithDNS(v, c.own)); err != nil {
				t.Fatal(err)
			}
			if len(v.reporterCalls) != c.calls {
				t.Errorf("ResolveReporter %v, want %d calls", v.reporterCalls, c.calls)
			}
			b, err := models.GetBounceByMessageID(mustOpenIndex(t, root, address), msg.ID)
			if err != nil {
				t.Fatal(err)
			}
			if want := GroupKey(c.category, c.unit, ""); b.CategoryRule != c.rule || b.GroupKey != want {
				t.Errorf("rule %s, group %s; want %s (%s: %s)", b.CategoryRule, b.GroupKey, c.rule, c.category, c.unit)
			}
		})
	}
}

// statusVerifier answers each name with the outcome and status given for
// it (DNSStatusVerifier) and resolves no reporting MTA (a HostResolver whose
// lookups are never answered).
type statusVerifier struct {
	answers map[string]struct {
		outcome DNSOutcome
		status  DNSVerifyStatus
	}
}

func (v *statusVerifier) Verify(ctx context.Context, f DNSFailure, domain string, at time.Time, direct bool) (DNSOutcome, bool) {
	outcome, _ := v.VerifyStatus(ctx, f, domain, at, direct)
	return outcome, outcome != ""
}

func (v *statusVerifier) VerifyStatus(_ context.Context, f DNSFailure, _ string, _ time.Time, _ bool) (DNSOutcome, DNSVerifyStatus) {
	a := v.answers[f.Name]
	return a.outcome, a.status
}

func (v *statusVerifier) ResolveHost(context.Context, string) ([]netip.Addr, bool) { return nil, false }
func (v *statusVerifier) ResolveReporter(context.Context, string) ([]netip.Addr, bool) {
	return nil, false
}

// TestDNSProgressLineCounts checks the dns progress line of a run: every
// notice whose name reached the verifier counts once, by the status the
// verifier told, except a name that exists while whether the reporting MTA
// is ours could not be told (inconclusive; too old when only a lookup of
// its name, not allowed for an old notice, could have told); the notices
// not checked because the check is off are named last, only when there are
// any; a status that does not fit the outcome is corrected.
func TestDNSProgressLineCounts(t *testing.T) {
	type answer = struct {
		outcome DNSOutcome
		status  DNSVerifyStatus
	}
	v := &statusVerifier{answers: map[string]answer{
		"a.nowhere.example": {DNSOutcomeExists, DNSStatusVerified},
		"b.nowhere.example": {"", DNSStatusOverBudget},
		"c.nowhere.example": {"", DNSStatusNotApplicable},
		"d.nowhere.example": {"", DNSStatusInconclusive},
		"e.nowhere.example": {"", DNSStatusTooOld},
		"f.nowhere.example": {"", DNSStatusCheckOff},
		"g.nowhere.example": {DNSOutcomeNotFound, DNSStatusReused},
		"h.nowhere.example": {DNSOutcomeExists, DNSStatusReused},   // old notice, reporting MTA a name
		"i.nowhere.example": {DNSOutcomeExists, DNSStatusVerified}, // recent notice, reporting MTA not answered
		"j.nowhere.example": {"", DNSStatusVerified},               // not verified after all
	}}
	root := t.TempDir()
	address := "newsletter@example.jp"
	now := time.Now().UTC()
	notices := []struct {
		name, mta string
		at        time.Time
	}{
		{"a", "[192.0.2.99]", now}, {"b", "[192.0.2.99]", now}, {"c", "[192.0.2.99]", now}, {"d", "[192.0.2.99]", now},
		{"e", "[192.0.2.99]", now}, {"f", "[192.0.2.99]", now}, {"g", "[192.0.2.99]", now},
		{"h", "relay.other.example", now.Add(-72 * time.Hour)}, {"i", "relay.other.example", now}, {"j", "[192.0.2.99]", now},
	}
	for i, n := range notices {
		name := n.name + ".nowhere.example"
		storeRaw(t, root, address, dnsNotice(bounceKindFailed, "5.4.4", "n@"+name, n.mta,
			"Name service error for name="+name+" type=MX: Host not found"), uint32(i+1), n.at)
	}
	own := NewOwnSources([]string{"192.0.2.0/24"}, nil, nil, nil)
	var lines []string
	if _, err := GroupMailbox(context.Background(), root, address, false, nil, func(s string) { lines = append(lines, s) },
		WithOwnSources(own), WithDNS(v, own)); err != nil {
		t.Fatal(err)
	}
	want := "dns: 10 notice(s): 1 verified, 1 reused from stored results, 1 over budget (run again), 1 not applicable, " +
		"3 inconclusive, 2 too old for a live check, 1 not checked (the DNS check is off)"
	if got := dnsLines(lines); len(got) != 1 || got[0] != want {
		t.Errorf("progress %q,\nwant %q", got, want)
	}
}
