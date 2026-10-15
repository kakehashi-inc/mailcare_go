package mailengine

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
)

// The addresses of our own sending servers that a run looks up beyond the
// settings: the A and AAAA addresses of the own host names (the setting
// own_sending_hosts and the SMTP hosts of all the monitored mail addresses,
// those of the other mailboxes included: every source matches for every
// mailbox) and the addresses the SPF records of the domains of the
// monitored mail addresses list. Both judgements use them, the backscatter
// rule (design document "mail classification" 2.6; it applies to the run
// only when a host name is configured for its mailbox, OwnSources.hasHosts)
// and whether the reporting MTA of a DNS lookup failure is ours (4.4), and
// only when a message needs an address compared: a run without such a
// message looks nothing up.
//
// Known limitation: a host name resolves to the address clients connect
// to, which can differ from the address the recipients' servers see (NAT
// in front of the server, a smart host that relays for it). The settings
// own_sending_ipv4 and own_sending_ipv6 remain for those cases.

// OwnHostResolver resolves the host names of our own sending servers
// (written by the administrator, so trusted, a single-label name included)
// to their A and AAAA addresses. answered is true when the lookup was
// answered for both families, with addresses or without any (NXDOMAIN, no
// data: the name still matches as text), or when something stands in for
// it (app/modules falls back to a stored lookup). It is false when the
// lookup was not answered (SERVFAIL, a timeout, no resolver reachable, only
// one of the families answered, a limit of the run, a cancellation): an
// address of ours may be missing then, so the lookups of the run are
// incomplete (ownExtra) like an SPF record that could not be read. The
// grouping asks for up to maxParallelHostLookups names at once, so an
// implementation must be safe for concurrent use. The
// implementations live in app/modules/dnscheck (the Checker of the DNS
// lookup failure verification, and the OS resolver alone while the
// verification is off) and app/modules (the stored lookups in front of
// them); the tests replace them.
type OwnHostResolver interface {
	ResolveHost(ctx context.Context, host string) (addrs []netip.Addr, answered bool)
}

// OwnSPFResolver evaluates the SPF record of a domain of the monitored mail
// addresses (the organization's own domain, so trusted) and returns the
// prefixes of the addresses its terms with the pass qualifier list, and the
// host names those a and mx terms point to (normalized). complete is false
// when the evaluation could not be finished (no answer, a lookup failure, a
// limit reached, a range wider than an IPv4 /8 or an IPv6 /16, more address
// space in total than one such range, a malformed or duplicate record, a
// cancellation): the prefixes are then not all of them. A domain without an
// SPF record is complete without prefixes. The
// implementations live in
// app/modules/dnscheck (the Checker) and app/modules (which falls back to
// the stored result of an earlier run); the tests replace them.
type OwnSPFResolver interface {
	ResolveSPF(ctx context.Context, domain string) (prefixes []netip.Prefix, hosts []string, complete bool)
}

// WithOwnHostResolver gives a run the resolver of its own host names: the
// addresses they resolve to count as our own sending servers in both
// judgements (ownLookups). Without it the verifier of WithDNS resolves
// them when it is a HostResolver; with neither the names are compared as
// text only.
func WithOwnHostResolver(r OwnHostResolver) GroupOption {
	return func(cfg *groupConfig) {
		cfg.ownResolver = r
	}
}

// WithOwnSPF gives a run the SPF records of the domains of the monitored
// mail addresses: the addresses they list count as our own sending servers
// in both judgements (ownLookups), like the settings own_sending_ipv4 and
// own_sending_ipv6, and the host names of their a and mx terms are compared
// as text like the own host names, without counting as a configured host
// name for the backscatter rule. While the record of a domain cannot be
// evaluated completely, or the lookup of an own host name is not answered
// (OwnHostResolver), no return notice is taken for backscatter in the run
// (an address of ours may be missing; the progress line says that the
// detection was skipped when the rule applies to the run), and a reporting
// MTA that only an address comparison could tell is left not verified.
// Without the option no SPF record is read.
func WithOwnSPF(r OwnSPFResolver, domains []string) GroupOption {
	return func(cfg *groupConfig) {
		cfg.spfResolver, cfg.spfDomains = r, domains
	}
}

// ownExtra is what the lookups of one run found.
type ownExtra struct {
	addrs      []netip.Addr   // addresses of the own host names (unmapped)
	unanswered []string       // own host names whose lookup was not answered
	prefixes   []netip.Prefix // prefixes the SPF records list (masked)
	hosts      []string       // host names of the a and mx terms of the SPF records
	domains    int            // domains whose SPF record was evaluated
	incomplete []string       // of those, the ones not evaluated completely
}

// complete reports whether the lookup of every own host name was answered
// and every SPF record was evaluated completely: otherwise an address of
// ours may be missing.
func (e ownExtra) complete() bool {
	return len(e.unanswered) == 0 && len(e.incomplete) == 0
}

// contains reports whether an unmapped address is one of the addresses or
// lies in one of the prefixes.
func (e ownExtra) contains(a netip.Addr) bool {
	if slices.Contains(e.addrs, a) {
		return true
	}
	for _, p := range e.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ownLookups looks up the addresses of ownExtra once per run, on the first
// need, and keeps them for the run unless ctx ended meanwhile (the next
// message tries again).
type ownLookups struct {
	hostResolver OwnHostResolver
	hosts        []string // the own host names, sorted
	spf          OwnSPFResolver
	domains      []string
	extra        ownExtra
	known        bool // extra was filled for the run
}

// newOwnLookups returns the lookups of a run: the host names of own through
// hr and the SPF records of domains through spf. nil when there is nothing
// to look up.
func newOwnLookups(hr OwnHostResolver, own OwnSources, spf OwnSPFResolver, domains []string) *ownLookups {
	l := &ownLookups{}
	if hosts := own.hostNames(); hr != nil && len(hosts) > 0 {
		l.hostResolver, l.hosts = hr, hosts
	}
	if spf != nil && len(domains) > 0 {
		l.spf, l.domains = spf, domains
	}
	if l.hostResolver == nil && l.spf == nil {
		return nil
	}
	return l
}

// get returns what the lookups found, looking them up on the first call of
// the run. A nil value found nothing and is complete.
func (l *ownLookups) get(ctx context.Context) ownExtra {
	if l == nil {
		return ownExtra{}
	}
	if l.known {
		return l.extra
	}
	var e ownExtra
	for i, res := range l.resolveHosts(ctx) {
		for _, a := range res.addrs {
			if a.IsValid() {
				e.addrs = append(e.addrs, a.Unmap().WithZone(""))
			}
		}
		if !res.answered {
			e.unanswered = append(e.unanswered, l.hosts[i])
		}
	}
	for _, domain := range l.domains {
		prefixes, hosts, complete := l.spf.ResolveSPF(ctx, domain)
		for _, p := range prefixes {
			if p.IsValid() {
				e.prefixes = append(e.prefixes, p.Masked())
			}
		}
		for _, h := range hosts {
			if h = normalizeHostName(h); h != "" && !slices.Contains(e.hosts, h) {
				e.hosts = append(e.hosts, h)
			}
		}
		e.domains++
		if !complete {
			e.incomplete = append(e.incomplete, domain)
		}
	}
	if ctx.Err() != nil {
		return e
	}
	l.extra, l.known = e, true
	return e
}

// maxParallelHostLookups bounds the own host names looked up at the same
// time (ownLookups.resolveHosts): a long list of names the resolver leaves
// unanswered then waits for a quarter of the timeouts, while the resolver
// bounds the network time of the run (its budget of the own sources).
const maxParallelHostLookups = 4

// hostAnswer is the lookup of one own host name.
type hostAnswer struct {
	addrs    []netip.Addr
	answered bool
}

// resolveHosts looks up the own host names, at most maxParallelHostLookups
// at a time, and returns the answers in the order of the names.
func (l *ownLookups) resolveHosts(ctx context.Context) []hostAnswer {
	out := make([]hostAnswer, len(l.hosts))
	slots := make(chan struct{}, maxParallelHostLookups)
	var wg sync.WaitGroup
	for i, host := range l.hosts {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			out[i].addrs, out[i].answered = l.hostResolver.ResolveHost(ctx, host)
		})
	}
	wg.Wait()
	return out
}

// readsSPF reports whether the lookups evaluate SPF records.
func (l *ownLookups) readsSPF() bool {
	return l != nil && l.spf != nil
}

// reportSummary adds the progress line of the lookups when they were made
// in the run, naming the own host names whose lookup was not answered and
// the domains whose SPF record was not read completely. backscatter tells
// whether the backscatter rule applies to the run (a host name is
// configured for the mailbox): only then did an incomplete lookup skip it,
// and the line says so; otherwise the missing lookups are named without
// that clause (they only leave a reporting MTA that an address comparison
// would have told not verified).
func (l *ownLookups) reportSummary(progress Progress, backscatter bool) {
	if l == nil || !l.known {
		return
	}
	e := l.extra
	line := fmt.Sprintf("own sources: %d address(es) of %d host name(s), %d SPF prefix(es) and %d SPF host name(s) of %d domain(s)",
		len(e.addrs), len(l.hosts), len(e.prefixes), len(e.hosts), e.domains)
	var missing []string
	if len(e.unanswered) > 0 {
		missing = append(missing, "no answer for the addresses of "+strings.Join(e.unanswered, ", "))
	}
	if len(e.incomplete) > 0 {
		missing = append(missing, "SPF not read completely for "+strings.Join(e.incomplete, ", "))
	}
	switch {
	case len(missing) == 0:
	case backscatter:
		line += "; backscatter detection was skipped for this run: " + strings.Join(missing, "; ")
	default:
		line += "; " + strings.Join(missing, "; ")
	}
	report(progress, line)
}
