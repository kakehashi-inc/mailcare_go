package mailengine

import (
	"context"
	"net/netip"
	"slices"
	"strings"
)

// SendingSources are the sending sources (the settings sending_source_ipv4,
// sending_source_ipv6 and sending_source_hosts, and the SMTP hosts of the
// monitored mail addresses as further host names), with the addresses the
// host names resolve to when the caller knows them, as the run of one
// mailbox sees them. They serve two judgements of the design document "mail
// classification": whether a notice about a DNS lookup failure came from a
// sending server (4.4, Match; the zero value, the empty configuration,
// counts every reporting MTA as a sending server) and whether a return
// notice is about a message sent from the sending sources at all (2.6: the
// Received headers of the returned original through MatchReceived, the
// reporting MTA through Match; the
// backscatter rule applies only while a host name is configured). Every
// source matches, but whether anything is configured (Empty) and whether a
// host name is (hasHosts) are judged per mailbox: the SMTP hosts of the
// other mailboxes (WithOtherHosts) only match and are looked up.
type SendingSources struct {
	prefixes   []netip.Prefix
	hosts      map[string]bool
	resolved   []netip.Addr
	otherHosts map[string]bool // SMTP hosts of the other mailboxes: matched as text and looked up, not configured for this one
	extraHosts map[string]bool // host names of SPF records: matched as text, no configured host name
	configured bool            // any non-blank entry was given in ipv4, ipv6 or hosts
}

// NewSendingSources builds the sending sources from the settings: ipv4 and
// ipv6 hold validated entries (a bare address is a /32 or /128 prefix, or a
// CIDR), hosts the host names configured for the mailbox (sending_source_hosts
// and the SMTP host of the mailbox; those of the other mailboxes are added with
// WithOtherHosts), and resolved addresses of those host names the caller
// already knows (usually nil: during a run the host names are resolved when
// first needed, see WithDNS). Blank entries are ignored; an entry that does
// not parse is dropped but still counts as configured, so it never turns
// the configuration into the empty one.
func NewSendingSources(ipv4, ipv6, hosts []string, resolved []netip.Addr) SendingSources {
	o := SendingSources{hosts: map[string]bool{}}
	for _, list := range [][]string{ipv4, ipv6} {
		for _, v := range list {
			if v = strings.TrimSpace(v); v == "" {
				continue
			}
			o.configured = true
			if p, ok := parseSourcePrefix(v); ok {
				o.prefixes = append(o.prefixes, p)
			}
		}
	}
	for _, h := range hosts {
		if h = normalizeHostName(h); h == "" {
			continue
		}
		o.configured = true
		o.hosts[h] = true
	}
	for _, a := range resolved {
		if a.IsValid() {
			o.resolved = append(o.resolved, a.Unmap().WithZone(""))
		}
	}
	return o
}

// WithOtherHosts returns a copy of o that also matches hosts (the SMTP
// hosts of the other monitored mail addresses) as text, like its host
// names, and looks their addresses up with them (sourceLookups), without
// counting them as configured or as host names of the mailbox: the SMTP
// host of one mailbox neither switches the backscatter rule on for another
// one nor ends the empty configuration of another one (design document
// "mail classification" 2.6 and 4.4), while a hop through any sending
// server still shows mail from the sending sources. A name already among
// the host names of o is
// left there; the maps of o are never changed in place.
func (o SendingSources) WithOtherHosts(hosts []string) SendingSources {
	other := make(map[string]bool, len(o.otherHosts)+len(hosts))
	for h := range o.otherHosts {
		other[h] = true
	}
	for _, h := range hosts {
		if h = normalizeHostName(h); h != "" && !o.hosts[h] {
			other[h] = true
		}
	}
	o.otherHosts = other
	return o
}

// Empty reports whether none of the settings holds an entry for the
// mailbox (no address and no host name of the settings, no SMTP host of
// the mailbox; the SMTP hosts of the other mailboxes and resolved addresses
// do not count).
func (o SendingSources) Empty() bool {
	return !o.configured
}

// hasHosts reports whether a host name is configured for the mailbox
// (sending_source_hosts or the SMTP host of the mailbox), which the backscatter rule
// requires. The SMTP hosts of the other mailboxes and resolved addresses do
// not count.
func (o SendingSources) hasHosts() bool {
	return len(o.hosts) > 0
}

// hostNames returns every host name to look the addresses of up, the SMTP
// hosts of the other mailboxes included, in sorted order.
func (o SendingSources) hostNames() []string {
	hosts := make([]string, 0, len(o.hosts)+len(o.otherHosts))
	for h := range o.hosts {
		hosts = append(hosts, h)
	}
	for h := range o.otherHosts {
		if !o.hosts[h] {
			hosts = append(hosts, h)
		}
	}
	slices.Sort(hosts)
	return hosts
}

// matchHost reports whether a normalized host name is a sending source as text:
// a host name of the mailbox, an SMTP host of another mailbox or a host
// name of an SPF record (withResolved).
func (o SendingSources) matchHost(name string) bool {
	return o.hosts[name] || o.otherHosts[name] || o.extraHosts[name]
}

// withResolved returns a copy of o that also counts addrs (addresses its
// host names resolve to) and prefixes (the addresses the SPF records of the
// monitored domains list) as sending sources, and compares hosts (the host names of
// the a and mx terms of those records) as text like its host names. An
// address or prefix already there is not added again (several domains often
// include the same provider), so every received address is compared with
// each range once. The slices and maps of o are copied, never changed in
// place, and neither its host names nor the configuration change: what was
// looked up alone makes no source configured and is no host name the
// backscatter rule asks for.
func (o SendingSources) withResolved(addrs []netip.Addr, prefixes []netip.Prefix, hosts []string) SendingSources {
	if len(addrs) == 0 && len(prefixes) == 0 && len(hosts) == 0 {
		return o
	}
	extra := make(map[string]bool, len(o.extraHosts)+len(hosts))
	for h := range o.extraHosts {
		extra[h] = true
	}
	for _, h := range hosts {
		if h = normalizeHostName(h); h != "" {
			extra[h] = true
		}
	}
	o.extraHosts = extra
	resolved := make([]netip.Addr, 0, len(o.resolved)+len(addrs))
	resolved = append(resolved, o.resolved...)
	for _, a := range addrs {
		if !a.IsValid() {
			continue
		}
		if a = a.Unmap().WithZone(""); !slices.Contains(resolved, a) {
			resolved = append(resolved, a)
		}
	}
	all := make([]netip.Prefix, 0, len(o.prefixes)+len(prefixes))
	seen := make(map[netip.Prefix]bool, len(o.prefixes)+len(prefixes))
	for _, p := range o.prefixes {
		all, seen[p] = append(all, p), true
	}
	for _, p := range prefixes {
		if !p.IsValid() {
			continue
		}
		if p = p.Masked(); !seen[p] {
			all, seen[p] = append(all, p), true
		}
	}
	o.resolved, o.prefixes = resolved, all
	return o
}

// Match reports whether the reporting MTA of a notice is one of the sending
// sources, without resolving anything. With an empty configuration
// every reporting MTA is (4.4). Otherwise a host name is compared with the
// configured host names (lower-cased, without a trailing dot), and an IP
// literal (see mtaHost and trimIPv6Tag for the forms) with the configured
// prefixes and the resolved addresses after unmapping an IPv4-mapped IPv6
// address. An empty or unreadable reporting MTA does not match. During a run
// the grouping matches through sourceMatcher, which also resolves names.
func (o SendingSources) Match(reportingMTA string) bool {
	m := sourceMatcher{sources: o}
	matched, _ := m.match(context.Background(), reportingMTA, false)
	return matched
}

// matchMTA reports whether a reporting MTA is one of the sending sources
// as text or as an IP literal, resolving nothing: a host name equal
// to one of the host names (lower-cased, without a trailing dot; those of
// SPF records given to withResolved included), an IP literal in a prefix or
// among the resolved addresses (unmapped first). Unlike Match, an empty
// configuration matches nothing here, and so does an empty or unreadable
// reporting MTA.
func (o SendingSources) matchMTA(reportingMTA string) bool {
	host := mtaHost(reportingMTA)
	if host == "" {
		return false
	}
	if addr, err := netip.ParseAddr(trimIPv6Tag(host)); err == nil {
		return o.matchAddr(addr.Unmap().WithZone(""))
	}
	return o.matchHost(normalizeHostName(host))
}

// Bounds of the work on the Received headers of one returned original
// (2.6). Past one of them the headers are not judged, and the backscatter
// rule does not take the notice (fail-safe): an original made of many or
// huge headers full of addresses would otherwise make the comparison with
// many prefixes cost seconds for every message and pass. Ordinary mail stays
// far below them (MTAs refuse mail with more hops than about 100 as a loop).
const (
	maxReceivedLines     = 100     // Received headers of the original
	maxReceivedLineBytes = 8 << 10 // bytes of one Received header (folded onto one line)
	maxReceivedAddrs     = 64      // distinct IP literals of one Received header
)

// MatchReceived reports whether one Received header of a returned message
// (folded onto one line) shows one of the sending sources (2.6), without
// resolving anything and without any network. An IP literal of the line
// matches when it lies in a configured prefix (an IPv4-mapped IPv6 address
// is unmapped first; the families never match each other) or is one of the
// resolved addresses given to NewSendingSources; a host name matches when it is
// one of the configured host names as text (lower-cased, without a trailing
// dot). receivedSources says which IP literals and host names of the line
// are looked at. A line past the bounds (maxReceivedLineBytes,
// maxReceivedAddrs) is not judged and counts as showing a sending source, so that the
// rule that asks does not take the notice. With an empty configuration
// nothing matches, unlike Match: the rule that asks does not apply then (it
// needs a host name anyway).
func (o SendingSources) MatchReceived(line string) bool {
	matched, judged := o.matchReceivedLines([]string{line})
	return matched || !judged
}

// matchReceivedLines reports whether any of the Received headers of a
// returned original shows one of the sending sources (see
// MatchReceived), and whether the headers could be judged at all: judged is
// false when there are more than maxReceivedLines of them or one is past the
// bounds of receivedSources. Every distinct address is compared once. With
// an empty configuration nothing matches.
func (o SendingSources) matchReceivedLines(lines []string) (matched, judged bool) {
	if o.Empty() {
		return false, true
	}
	if len(lines) > maxReceivedLines {
		return false, false
	}
	seen := map[netip.Addr]bool{}
	for _, line := range lines {
		addrs, hosts, ok := receivedSources(line)
		if !ok {
			return false, false
		}
		for _, a := range addrs {
			if seen[a] {
				continue
			}
			seen[a] = true
			if o.matchAddr(a) {
				return true, true
			}
		}
		for _, h := range hosts {
			if o.matchHost(h) {
				return true, true
			}
		}
	}
	return false, true
}

// matchAddr reports whether an unmapped address lies in a configured prefix
// (Prefix.Contains never matches across the address families) or is one of
// the resolved addresses given to NewSendingSources.
func (o SendingSources) matchAddr(addr netip.Addr) bool {
	for _, p := range o.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return slices.Contains(o.resolved, addr)
}

// receivedToken is one word of a Received header and the depth of the
// comments (parentheses) it is written in; 0 is outside any comment.
type receivedToken struct {
	text  string
	depth int
}

// receivedTokens splits a Received header into words: white space, commas
// and semicolons separate them, and a parenthesis both separates and opens
// or closes a comment. Brackets are kept inside their word ("[192.0.2.25]",
// "[IPv6:2001:db8::25]").
func receivedTokens(line string) []receivedToken {
	var tokens []receivedToken
	depth, start := 0, -1
	flush := func(end int) {
		if start >= 0 {
			tokens = append(tokens, receivedToken{text: line[start:end], depth: depth})
			start = -1
		}
	}
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '(':
			flush(i)
			depth++
		case ')':
			flush(i)
			if depth > 0 {
				depth--
			}
		case ' ', '\t', '\r', '\n', ',', ';':
			flush(i)
		default:
			if start < 0 {
				start = i
			}
		}
	}
	flush(len(line))
	return tokens
}

// receivedSources returns the IP literals (unmapped) and the host names
// (lower-cased, without a trailing dot) a Received header names for its
// hops, in the forms MTAs write:
//
//   - every IP literal in brackets, anywhere in the line ("[203.0.113.5]",
//     "[IPv6:2001:db8::25]", "[::ffff:203.0.113.5]");
//   - every word inside a comment that is an IP literal as a whole
//     ("(192.0.2.30)" of Exchange, "(IPv6:2001:db8::25)",
//     "(2001:db8::10)");
//   - the word after "from" and the word after "by" (outside comments): an
//     IP literal ("by 2001:db8::1", bare or in brackets) or a host name (the
//     name the sending server introduced itself with, and the receiving
//     server);
//   - the first word of the comment right after the "from" word, the name
//     the receiving server found for the sending address ("(mail.example.jp
//     [203.0.113.5])" of Postfix);
//   - the name a comment gives as the HELO name ("helo=mail.example.jp" of
//     Exim, "HELO mail.example.jp" of qmail).
//
// Other words (the queue id, the protocol, the recipient, the time stamp)
// are not looked at. The addresses are returned without repeats. ok is
// false, and nothing is returned, for a line longer than
// maxReceivedLineBytes (not even split into words) or one that names more
// than maxReceivedAddrs distinct addresses: such a line is not judged.
// Unreadable input yields nothing; it never fails.
func receivedSources(line string) (addrs []netip.Addr, hosts []string, ok bool) {
	if len(line) > maxReceivedLineBytes {
		return nil, nil, false
	}
	tooMany := false
	addAddr := func(a netip.Addr) {
		if tooMany || slices.Contains(addrs, a) {
			return
		}
		if len(addrs) == maxReceivedAddrs {
			tooMany = true
			return
		}
		addrs = append(addrs, a)
	}
	addHost := func(s string) {
		if s = normalizeHostName(s); s != "" && !strings.ContainsAny(s, "[]") {
			hosts = append(hosts, s)
		}
	}
	// addHop takes the word after "from" or "by": an IP literal or a name.
	addHop := func(s string) {
		if a, ok := ipLiteral(s); ok {
			addAddr(a)
			return
		}
		addHost(s)
	}
	tokens := receivedTokens(line)
	for i, t := range tokens {
		if tooMany {
			return nil, nil, false
		}
		for _, inner := range bracketed(t.text) {
			if a, ok := ipLiteral(inner); ok {
				addAddr(a)
			}
		}
		if t.depth > 0 {
			if a, ok := ipLiteral(t.text); ok {
				addAddr(a)
			}
			lower := strings.ToLower(t.text)
			switch {
			case strings.HasPrefix(lower, "helo="):
				addHop(t.text[len("helo="):])
			case (lower == "helo" || lower == "ehlo") && i+1 < len(tokens) && tokens[i+1].depth == t.depth:
				addHop(tokens[i+1].text)
			}
			continue
		}
		if i+1 >= len(tokens) || tokens[i+1].depth != 0 {
			continue
		}
		switch {
		case strings.EqualFold(t.text, "by"):
			addHop(tokens[i+1].text)
		case strings.EqualFold(t.text, "from"):
			addHop(tokens[i+1].text)
			if i+2 < len(tokens) && tokens[i+2].depth == 1 {
				addHop(tokens[i+2].text)
			}
		}
	}
	if tooMany {
		return nil, nil, false
	}
	return addrs, hosts, true
}

// bracketed returns the contents of every [...] pair of a word.
func bracketed(s string) []string {
	var out []string
	for {
		open := strings.IndexByte(s, '[')
		if open < 0 {
			return out
		}
		end := strings.IndexByte(s[open+1:], ']')
		if end < 0 {
			return out
		}
		out = append(out, s[open+1:open+1+end])
		s = s[open+1+end+1:]
	}
}

// ipLiteral reads a word of a Received header as an IP literal: bare, in
// brackets or parentheses, with the "IPv6:" tag or a port (see mtaHost and
// trimIPv6Tag). The address is unmapped and without a zone.
func ipLiteral(s string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(trimIPv6Tag(mtaHost(s)))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// parseSourcePrefix reads one entry of sending_source_ipv4 / sending_source_ipv6: a
// CIDR, or a bare address taken as a single-address prefix.
func parseSourcePrefix(v string) (netip.Prefix, bool) {
	if strings.Contains(v, "/") {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return netip.Prefix{}, false
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap().WithZone("")
	return netip.PrefixFrom(a, a.BitLen()), true
}

// normalizeHostName lower-cases a host name and removes its trailing dot.
func normalizeHostName(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// mtaHost reduces the reporting MTA of a notice to the bare host name or IP
// literal: a comment after it ("mx1.example.jp (Postfix)") is dropped, then
// surrounding parentheses ("(192.0.2.25)"), the brackets of an address
// literal ("[192.0.2.25]", "[IPv6:2001:db8::25]") and a trailing port
// ("[192.0.2.25]:25", "192.0.2.25:25", "mx1.example.jp:25"). A bare IPv6
// address keeps its colons; it never carries a port without brackets.
// "" when nothing usable is left.
func mtaHost(v string) string {
	s := strings.TrimSpace(v)
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = s[1 : len(s)-1]
	}
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 || (end+1 < len(s) && !isPort(s[end+1:])) {
			return ""
		}
		return s[1:end]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 && i == strings.LastIndexByte(s, ':') && isPort(s[i:]) {
		return s[:i]
	}
	return s
}

// isPort reports whether s is a port suffix (":" followed by digits).
func isPort(s string) bool {
	if len(s) < 2 || s[0] != ':' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// trimIPv6Tag removes the "IPv6:" tag of an RFC 5321 address literal
// ("IPv6:2001:db8::25"); the tag is matched case-insensitively.
func trimIPv6Tag(s string) string {
	const tag = "ipv6:"
	if len(s) > len(tag) && strings.EqualFold(s[:len(tag)], tag) {
		return s[len(tag):]
	}
	return s
}

// WithSendingSources gives a GroupMailbox or Reindex run the sending sources
// (the settings sending_source_ipv4 / sending_source_ipv6 / sending_source_hosts and
// the SMTP host of the mailbox of the run, with those of the other
// monitored mail addresses through WithOtherHosts). The
// classification uses them for the backscatter rule (design document "mail
// classification" 2.6), whether the DNS lookup failures are verified or
// not, and the verification of WithDNS uses them in place of the sources
// given there to tell the reporting MTAs that are sending servers (4.4). Without the option the
// backscatter rule does not apply.
func WithSendingSources(sources SendingSources) GroupOption {
	return func(cfg *groupConfig) {
		cfg.sources, cfg.sourcesGiven = sources, true
	}
}
