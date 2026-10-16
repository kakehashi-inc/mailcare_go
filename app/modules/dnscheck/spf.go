package dnscheck

import (
	"context"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"mailcare/app/modules/mailengine"
)

// The SPF records of the domains of the monitored mail addresses (RFC 7208)
// add to the sending sources: every address a term with the pass qualifier
// (none, or "+") lists counts as a sending source, and the host names its a
// and mx terms point to are compared as text like the source host names
// (design document "mail classification" 2.6 and 4.4). A term with another
// qualifier ("-", "~", "?") names no sending server and is skipped, an
// include with one together with everything it includes. Only the monitored
// domains are evaluated (trusted; never a name a notice chose), through the
// same resolvers as the source host names: the first
// reachable resolver that answers decides, with no vote. The evaluation
// follows ip4, ip6, a, mx, include and redirect; all, ptr, exists, exp and
// unknown modifiers are ignored, and so is any term whose domain-spec holds
// a macro ("%"). An evaluation that cannot be finished is incomplete, and
// so is one with a term that yields a range wider than spfMinBits4 /
// spfMinBits6 and one whose ranges together cover more than
// spfMaxSpaceBits4 / spfMaxSpaceBits6 allow; a domain without an SPF record
// (NXDOMAIN, no TXT record, no "v=spf1" record) is complete without
// addresses.

// Bounds of the evaluation of one domain (fail-safe: reaching one makes it
// incomplete, except a loop, which is skipped). New sets the Checker's
// fields from them; tests may lower them.
const (
	defaultSPFMaxQueries   = 30   // DNS queries per domain, includes and redirects counted
	defaultSPFMaxDepth     = 10   // nesting of include and redirect
	defaultSPFMaxPrefixes  = 1000 // prefixes per domain
	defaultSPFMaxMXTargets = 10   // MX targets of one mx term (RFC 7208 4.6.4)
)

// spfMinBits4 and spfMinBits6 are the shortest prefixes an SPF term may
// yield (an IPv4 /8, an IPv6 /16); a shorter one makes the evaluation
// incomplete (fail-safe). Counting such a range as a sending source,
// "ip4:0.0.0.0/0" reached through the include of a third party for one,
// would take almost every received address for a sending server and
// silently defeat the backscatter rule and the judgement of the reporting
// MTAs.
const (
	spfMinBits4 = 8
	spfMinBits6 = 16
)

// spfMaxSpaceBits4 and spfMaxSpaceBits6 bound the address space that all
// the prefixes of one evaluation cover together, every address counted once
// (overlapping and adjacent prefixes merged): 2^24 IPv4 addresses (one /8)
// and 2^112 IPv6 addresses (one /16), the bounds of mailengine, which the
// grouping also applies to the prefixes of all the domains of a run
// together. An evaluation that covers more is incomplete (fail-safe), like a
// single range wider than spfMinBits4 / spfMinBits6: many narrower terms
// ("ip4:N.0.0.0/8" for every N) would otherwise take every address for a sending source
// just the same. The records of the large mail services stay far below them
// (testdata/spf_real_senders.txt: Google Workspace, Microsoft 365, Amazon
// SES and SendGrid together cover fewer than 2^20 IPv4 addresses).
const (
	spfMaxSpaceBits4 = mailengine.SPFMaxSpaceBits4
	spfMaxSpaceBits6 = mailengine.SPFMaxSpaceBits6
)

// SPFResult is the evaluation of the SPF record of one domain: the prefixes
// it lists (masked; an address is a single-address prefix) and the host
// names its a and mx terms point to (the a hosts and the MX targets,
// normalized), in record order without repeats. Complete is false when the
// evaluation could not be finished: a lookup failed (SERVFAIL, a timeout,
// no resolver answered), a bound was reached, a term yielded a range wider
// than spfMinBits4 / spfMinBits6, the prefixes together cover more than
// spfMaxSpaceBits4 / spfMaxSpaceBits6 allow, a record was malformed or
// there were two, the time budget of the sending sources was used up or ctx
// ended. What was found before is kept.
type SPFResult struct {
	Prefixes []netip.Prefix
	Hosts    []string
	Complete bool
}

// ResolveSPF is LookupSPF in the form of mailengine.SourceSPFResolver.
func (c *Checker) ResolveSPF(ctx context.Context, domain string) (prefixes []netip.Prefix, hosts []string, complete bool) {
	r := c.LookupSPF(ctx, domain)
	return r.Prefixes, r.Hosts, r.Complete
}

// LookupSPF evaluates the SPF record of one of the monitored domains,
// once per run (remembered unless ctx ended). Every query counts against
// the time budget of the sending sources (Config.SourceBudget, apart from the
// budget of the verification, so that the names hostile notices make the
// run look up cannot use it up first) and is bounded by the per-query
// timeout. A Checker whose
// verification is off (or StoredOnly) reads no SPF record: the result is
// incomplete, so nothing relies on it.
func (c *Checker) LookupSPF(ctx context.Context, domain string) SPFResult {
	if !c.live() {
		return SPFResult{}
	}
	name, ok := spfDomain(domain)
	if !ok {
		return SPFResult{}
	}
	c.mu.Lock()
	res, ok := c.spf[name]
	c.mu.Unlock()
	if ok {
		return res
	}
	e := &spfEval{c: c, seenPrefix: map[netip.Prefix]bool{}, seenHost: map[string]bool{}}
	e.domain(ctx, name, 0, nil)
	if !e.failed && mailengine.SPFTooMuchSpace(e.prefixes) {
		e.failed = true
	}
	res = SPFResult{Prefixes: e.prefixes, Hosts: e.hosts, Complete: !e.failed}
	if ended(ctx) {
		res.Complete = false
		return res
	}
	c.mu.Lock()
	c.spf[name] = res
	c.mu.Unlock()
	return res
}

// spfEval is the evaluation of the SPF record of one domain.
type spfEval struct {
	c          *Checker
	queries    int
	prefixes   []netip.Prefix
	seenPrefix map[netip.Prefix]bool
	hosts      []string
	seenHost   map[string]bool
	failed     bool // incomplete
}

// domain evaluates the SPF record of a domain reached at the given depth
// (0 for the domain asked for, 1 for an include or redirect of it, ...).
// chain is the domains evaluated above it: a domain met again in that
// chain is a loop and is skipped.
func (e *spfEval) domain(ctx context.Context, domain string, depth int, chain []string) {
	if e.failed {
		return
	}
	if depth > e.c.spfMaxDepth {
		e.failed = true
		return
	}
	if slices.Contains(chain, domain) {
		return
	}
	chain = append(chain[:len(chain):len(chain)], domain)
	txts, a, ok := e.query(ctx, domain, typeTXT)
	if !ok || a != answerData {
		return
	}
	record, found, usable := pickSPF(txts)
	if !usable {
		e.failed = true
		return
	}
	if !found {
		return
	}
	var redirect string
	hasAll := false
	for _, term := range strings.Fields(record)[1:] {
		if e.failed {
			return
		}
		if name, value, ok := spfModifier(term); ok {
			if name == "redirect" {
				if redirect != "" {
					e.failed = true // a second redirect is a syntax error (RFC 7208 6)
					return
				}
				redirect = value
				if redirect == "" {
					e.failed = true
					return
				}
			}
			continue // exp and unknown modifiers are ignored
		}
		qualifier := byte('+')
		if strings.ContainsRune("+-~?", rune(term[0])) {
			qualifier, term = term[0], term[1:]
		}
		mech, rest := splitMechanism(term)
		switch mech {
		case "all":
			hasAll = true // whatever its qualifier: it still keeps a redirect from being followed
			continue
		case "ptr", "exists", "ip4", "ip6", "a", "mx", "include":
		default:
			e.failed = true // an unknown mechanism is a syntax error (RFC 7208 5)
			return
		}
		if qualifier != '+' {
			// A fail, softfail or neutral term names servers that are no
			// sending sources, or not vouched for: it is not evaluated, an include is
			// not followed.
			continue
		}
		switch mech {
		case "ptr", "exists":
			// Not followed: neither lists addresses that can be collected.
		case "ip4", "ip6":
			e.ipTerm(mech, rest)
		case "a", "mx":
			e.hostTerm(ctx, mech, rest, domain)
		case "include":
			target, ok := strings.CutPrefix(rest, ":")
			if !ok || target == "" {
				e.failed = true
				return
			}
			if strings.Contains(target, "%") {
				continue // a macro: ignored
			}
			include, ok := spfDomain(target)
			if !ok {
				e.failed = true
				return
			}
			e.domain(ctx, include, depth+1, chain)
		}
	}
	if redirect != "" && !hasAll && !strings.Contains(redirect, "%") {
		target, ok := spfDomain(redirect)
		if !ok {
			e.failed = true
			return
		}
		e.domain(ctx, target, depth+1, chain)
	}
}

// ipTerm collects the prefix of an ip4 or ip6 term (rest is ":address" or
// ":address/length"); a value of the wrong family or shape is malformed. An
// ip6 term of an IPv4-mapped address ("::ffff:192.0.2.1", "/120") names
// IPv4 addresses, which the received addresses are compared as (they are
// unmapped): it is taken as the IPv4 prefix of length - 96, and ignored when
// shorter than 96 (it does not lie within the mapped range).
func (e *spfEval) ipTerm(mech, rest string) {
	value, ok := strings.CutPrefix(rest, ":")
	if !ok || value == "" {
		e.failed = true
		return
	}
	var p netip.Prefix
	if strings.Contains(value, "/") {
		parsed, err := netip.ParsePrefix(value)
		if err != nil {
			e.failed = true
			return
		}
		p = parsed
	} else {
		a, err := netip.ParseAddr(value)
		if err != nil {
			e.failed = true
			return
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if p.Addr().Zone() != "" || p.Addr().Is4() != (mech == "ip4") {
		e.failed = true
		return
	}
	if p.Addr().Is4In6() {
		if p.Bits() < 96 {
			return
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	e.addPrefix(p)
}

// hostTerm collects an a or mx term: rest is the optional ":domain" and the
// optional dual CIDR length ("/24", "//64", "/24//64") applied to the
// addresses found (RFC 7208 5.6). The a host, or every MX target (at most
// spfMaxMXTargets; a null MX has none), is recorded as a host name and its
// A and AAAA addresses as prefixes (one shorter than spfMinBits4 /
// spfMinBits6 makes the evaluation incomplete, see addPrefix). NXDOMAIN and
// no data add nothing.
func (e *spfEval) hostTerm(ctx context.Context, mech, rest, current string) {
	target := current
	if value, ok := strings.CutPrefix(rest, ":"); ok {
		spec, cidr, _ := strings.Cut(value, "/")
		if strings.Contains(spec, "%") {
			return // a macro: ignored
		}
		name, ok := spfDomain(spec)
		if !ok {
			e.failed = true
			return
		}
		target = name
		rest = ""
		if cidr != "" || strings.Contains(value, "/") {
			rest = "/" + cidr
		}
	}
	bits4, bits6, ok := parseDualCIDR(rest)
	if !ok {
		e.failed = true
		return
	}
	hosts := []string{target}
	if mech == "mx" {
		values, a, ok := e.query(ctx, target, typeMX)
		if !ok {
			return
		}
		hosts = nil
		if a == answerData {
			for _, v := range values {
				if v == "." || v == "(invalid name)" {
					continue
				}
				hosts = append(hosts, v)
			}
		}
		if len(hosts) > e.c.spfMaxMXTargets {
			e.failed = true
			return
		}
	}
	for _, h := range hosts {
		e.addHost(h)
		for _, t := range []queryType{typeA, typeAAAA} {
			values, a, ok := e.query(ctx, h, t)
			if !ok {
				return
			}
			if a != answerData {
				continue
			}
			for _, v := range values {
				addr, err := netip.ParseAddr(v)
				if err != nil {
					continue
				}
				addr = addr.Unmap()
				bits := bits4
				if addr.Is6() {
					bits = bits6
				}
				e.addPrefix(netip.PrefixFrom(addr, bits))
			}
		}
	}
}

// query asks one question of the evaluation through the first reachable
// resolver that answers (no vote: the monitored domains are trusted). ok
// is false, and the evaluation incomplete, when the bound of queries or the
// time budget of the sending sources was reached first or no
// resolver answered (an error reply or none at all); otherwise the answer
// is data, no data or NXDOMAIN.
func (e *spfEval) query(ctx context.Context, name string, t queryType) ([]string, answer, bool) {
	if e.failed || ended(ctx) {
		e.failed = true
		return nil, answerNone, false
	}
	if e.queries >= e.c.spfMaxQueries || !e.c.sourceBudgetLeft() {
		e.failed = true
		return nil, answerNone, false
	}
	e.queries++
	start := time.Now()
	values, a := e.c.firstRecords(ctx, name, t)
	e.c.spendSource(time.Since(start))
	if a == answerFail || a == answerNone {
		e.failed = true
		return nil, a, false
	}
	return values, a, true
}

// addPrefix records a prefix (masked) unless it is already there; a prefix
// shorter than spfMinBits4 / spfMinBits6, and more than spfMaxPrefixes,
// make the evaluation incomplete.
func (e *spfEval) addPrefix(p netip.Prefix) {
	minBits := spfMinBits6
	if p.Addr().Is4() {
		minBits = spfMinBits4
	}
	if p.Bits() < minBits {
		e.failed = true
		return
	}
	p = p.Masked()
	if e.seenPrefix[p] {
		return
	}
	if len(e.prefixes) >= e.c.spfMaxPrefixes {
		e.failed = true
		return
	}
	e.seenPrefix[p] = true
	e.prefixes = append(e.prefixes, p)
}

// addHost records a host name unless it is already there.
func (e *spfEval) addHost(h string) {
	if !e.seenHost[h] {
		e.seenHost[h] = true
		e.hosts = append(e.hosts, h)
	}
}

// SPFTooWide reports whether prefixes (an SPF evaluation, or a stored one
// of an earlier run) take in more than an evaluation may count as sending sources: a
// range wider than spfMinBits4 / spfMinBits6, or more address space
// together than spfMaxSpaceBits4 / spfMaxSpaceBits6 allow. LookupSPF never
// completes with such prefixes; the cache of the sending sources (app/modules)
// does not use a stored result that does (one written before these limits).
func SPFTooWide(prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		minBits := spfMinBits6
		if p.Addr().Is4() {
			minBits = spfMinBits4
		}
		if p.IsValid() && p.Bits() < minBits {
			return true
		}
	}
	return mailengine.SPFTooMuchSpace(prefixes)
}

// firstRecords returns the records of one type of a name through the first
// reachable resolver that answers (data, no data or NXDOMAIN): the
// configured resolvers, or the OS resolver of the SystemOnly mode asked
// with the absolute name. answerNone when none answered.
func (c *Checker) firstRecords(ctx context.Context, name string, t queryType) ([]string, answer) {
	for _, p := range c.reachableResolvers(ctx) {
		var values []string
		var a answer
		if p.addr.IsValid() {
			a, values, _ = c.lookupType(ctx, p.addr, name, t, nil)
		} else {
			values, a = c.systemRecords(ctx, name, t)
		}
		if a == answerFail || a == answerNone {
			continue
		}
		return values, a
	}
	return nil, answerNone
}

// pickSPF picks the SPF record among the TXT records of a name: the one
// that is "v=spf1" or starts with "v=spf1 " (case-insensitive). found is
// false when there is none; usable is false when there are several (RFC
// 7208 4.5: a permanent error).
func pickSPF(txts []string) (record string, found, usable bool) {
	for _, txt := range txts {
		lower := strings.ToLower(txt)
		if lower != "v=spf1" && !strings.HasPrefix(lower, "v=spf1 ") {
			continue
		}
		if found {
			return "", true, false
		}
		record, found = txt, true
	}
	return record, found, true
}

// spfModifier reports whether a term is a modifier (name "=" value, the
// name a letter followed by letters, digits, "-", "_" and ".") and returns
// its lower-cased name and its value.
func spfModifier(term string) (name, value string, ok bool) {
	name, value, ok = strings.Cut(term, "=")
	if !ok || name == "" {
		return "", "", false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		letter := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
		if !letter && (i == 0 || !(ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.')) {
			return "", "", false
		}
	}
	return strings.ToLower(name), value, true
}

// splitMechanism splits a mechanism (without its qualifier) into its
// lower-cased name and the rest (":domain-spec", "/length", or "").
func splitMechanism(term string) (name, rest string) {
	i := strings.IndexAny(term, ":/")
	if i < 0 {
		return strings.ToLower(term), ""
	}
	return strings.ToLower(term[:i]), term[i:]
}

// parseDualCIDR reads the dual CIDR length of an a or mx term: "", "/n",
// "//m" or "/n//m", with n at most 32 and m at most 128 (32 and 128 when
// absent).
func parseDualCIDR(s string) (bits4, bits6 int, ok bool) {
	bits4, bits6 = 32, 128
	if s == "" {
		return bits4, bits6, true
	}
	if !strings.HasPrefix(s, "/") {
		return 0, 0, false
	}
	v4, v6, dual := strings.Cut(s[1:], "//")
	if strings.HasPrefix(s, "//") {
		v4, v6, dual = "", s[2:], true
	}
	if !dual && v4 == "" {
		return 0, 0, false
	}
	if v4 != "" {
		n, err := strconv.Atoi(v4)
		if err != nil || n < 0 || n > 32 || strconv.Itoa(n) != v4 {
			return 0, 0, false
		}
		bits4 = n
	}
	if dual {
		n, err := strconv.Atoi(v6)
		if err != nil || n < 0 || n > 128 || strconv.Itoa(n) != v6 {
			return 0, 0, false
		}
		bits6 = n
	}
	return bits4, bits6, true
}

// spfDomain lower-cases a domain of an SPF record (or of a monitored mail
// address), removes its trailing dot and validates it: at least two labels
// of 1-63 letters, digits, hyphens and underscores (SPF records commonly
// name "_spf.example.jp"), at most 253 characters, a last label that is not
// all digits.
func spfDomain(s string) (string, bool) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if name == "" || len(name) > 253 {
		return "", false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			ch := label[i]
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
				return "", false
			}
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", false
	}
	return name, true
}
