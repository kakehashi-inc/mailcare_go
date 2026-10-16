package mailengine

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"mailcare/app/models"
)

// DNSVerifier verifies the name of a detected DNS lookup failure (design
// document "mail classification" 4.4): f is the failure the diagnostic
// reports, recipientDomain the domain of the failed recipient and
// noticeTime when the notice was received (a stored result checked from a
// day before it to DNSLiveMaxAge after it may be reused). direct allows the
// verification to ask the
// authoritative servers of the name directly, besides the resolvers: the
// grouping sets it only for a notice that clearly concerns mail sent from
// the sending sources (dnsVerification.aboutSentMail), since a forged notice chooses both the
// name and the recipient domain, and so the servers that would be asked.
// ok = false means the name was not verified (outside the conditions, over
// the limit of the run, or no resolver answered). The implementation lives
// in app/modules/dnscheck; the tests replace it.
type DNSVerifier interface {
	Verify(ctx context.Context, f DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (DNSOutcome, bool)
}

// DNSVerifyStatus tells how the verification of the name of one notice
// ended, for the progress line of the run (dnsVerification.reportSummary).
type DNSVerifyStatus int

const (
	// DNSStatusInconclusive: no resolver gave a usable answer in time
	// about the name or the MX targets of the recipient domain (or the run
	// was canceled), or the name exists but whether the reporting MTA is
	// a sending server could not be told.
	DNSStatusInconclusive DNSVerifyStatus = iota
	// DNSStatusVerified: looked up now.
	DNSStatusVerified
	// DNSStatusReused: a stored outcome, or one found earlier in the run.
	DNSStatusReused
	// DNSStatusOverBudget: the limits of the run (network time, number of
	// names, number of recipient domains whose MX targets are looked up)
	// were reached; a later run verifies the name.
	DNSStatusOverBudget
	// DNSStatusNotApplicable: the name may not be looked up for the notice
	// (neither in the domain of the recipient nor one of its MX targets) or
	// is no usable name.
	DNSStatusNotApplicable
	// DNSStatusTooOld: nothing stored for the notice, and it is older than
	// DNSLiveMaxAge (also when only a lookup of its reporting MTA, not
	// allowed for it either, could have told whether that is a sending
	// server).
	DNSStatusTooOld
	// DNSStatusCheckOff: nothing stored for the notice, and the DNS check
	// is off.
	DNSStatusCheckOff
)

// DNSStatusVerifier is a DNSVerifier that also tells how a verification
// ended (the outcome is "" when the name was not verified). The grouping
// uses it when the verifier implements it (the Checker of
// app/modules/dnscheck does); with any other verifier a name that was not
// verified counts as inconclusive.
type DNSStatusVerifier interface {
	VerifyStatus(ctx context.Context, f DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (DNSOutcome, DNSVerifyStatus)
}

// HostResolver resolves the host names that tell whether a notice came from
// one of the sending sources (4.4). ResolveHost resolves one of the source
// host names and tells whether it was answered (SourceHostResolver).
// ResolveReporter resolves the reporting MTA
// a notice names (untrusted: the implementation bounds what it looks up);
// answered is false when the lookup was not made (a name refused as no host
// name of two labels or more, the limits of the run reached, no live
// lookups at all) or not answered (no resolver answered, ctx ended), and
// true when it was answered, with addresses or without any. The grouping
// asks only about a notice recent enough for live lookups
// (LiveCheckAllowed). The implementation lives in app/modules/dnscheck (its
// Checker is also the DNSVerifier); the tests replace it.
type HostResolver interface {
	SourceHostResolver
	ResolveReporter(ctx context.Context, host string) (addrs []netip.Addr, answered bool)
}

// sourceMatcher tells which reporting MTAs are sending servers during one
// run (4.4). Without a resolver it is SendingSources.Match. With one, a
// reporting MTA given as a host name that is none of the source host names
// is resolved
// (ResolveReporter), and its addresses, like an IP literal, are compared
// with the prefixes and with the addresses the run looks up (sourceLookups:
// the addresses of the source host names and the SPF records of the monitored
// domains; the lookups of the run when given, else the host names resolved
// through resolver). Those are looked up on the first need only and kept
// for the run, so a run without a verified DNS lookup failure looks nothing
// up for it. The zero value matches everything (the empty configuration).
type sourceMatcher struct {
	sources  SendingSources
	resolver HostResolver   // nil: reporting MTA names are compared as text only
	lookups  *sourceLookups // nil: made from resolver on first need
}

// match reports whether the reporting MTA is one of the sending sources
// (matched) and whether that could be told at all (known). With an empty
// configuration every reporting MTA is a sending server. Otherwise an IP
// literal is compared with the source addresses (matchAddrs), and a host
// name as text with the source host names and the host names of the SPF
// records; a host
// name the text does not match is resolved (ResolveReporter) when live
// (the notice is recent enough, LiveCheckAllowed) and its addresses are
// compared like an IP literal. Only addresses that were found and are none
// of the sending sources make the reporting MTA an outside server. known is
// false, the
// question left open, when the reporting MTA is empty or unreadable, when
// it is a host name that could not be resolved (no resolver, not live,
// refused as no host name of two labels or more, not answered) or was
// answered without any address (NXDOMAIN, no data: a name that does not
// resolve tells nothing about whose server wrote it), and when its
// addresses (or its IP literal) matched nothing while the lookups of the
// run are incomplete (a source host name not answered, an SPF record not
// evaluated completely: an address of a sending source may be missing). The
// limits of
// the run, no answer or a cancellation would otherwise make the notice
// external depending on the order of the notices, and an odd name would
// hide a failure of the resolver of a sending server. A cancellation of ctx
// leaves what was
// looked up unremembered.
func (m *sourceMatcher) match(ctx context.Context, reportingMTA string, live bool) (matched, known bool) {
	if m.sources.Empty() {
		return true, true
	}
	host := mtaHost(reportingMTA)
	if host == "" {
		return false, false
	}
	if addr, err := netip.ParseAddr(trimIPv6Tag(host)); err == nil {
		return m.matchAddrs(ctx, []netip.Addr{addr})
	}
	if m.sources.matchMTA(host) {
		return true, true
	}
	// The host names of the SPF records are known only once looked up.
	name := normalizeHostName(host)
	if m.lookups.readsSPF() && slices.Contains(m.extra(ctx).hosts, name) {
		return true, true
	}
	if m.resolver == nil || !live {
		return false, false
	}
	addrs, answered := m.resolver.ResolveReporter(ctx, name)
	if !answered || len(addrs) == 0 {
		return false, false
	}
	return m.matchAddrs(ctx, addrs)
}

// matchAddrs reports whether any of the addresses of a reporting MTA is one
// of the sending sources: in a configured prefix, one of the given resolved addresses, or
// one the run looks up (sourceLookups: only when nothing else matched). known
// is false when none matched while the lookups of the run are incomplete
// (sourceExtra.complete), and when there is no address at all.
func (m *sourceMatcher) matchAddrs(ctx context.Context, addrs []netip.Addr) (matched, known bool) {
	if len(addrs) == 0 {
		return false, false
	}
	unmapped := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if m.sources.matchAddr(a) {
			return true, true
		}
		unmapped = append(unmapped, a)
	}
	extra := m.extra(ctx)
	if slices.ContainsFunc(unmapped, extra.contains) {
		return true, true
	}
	return false, extra.complete()
}

// extra returns what the run looks up, on the first call of the run
// (nothing without lookups or a resolver of the source host names).
func (m *sourceMatcher) extra(ctx context.Context) sourceExtra {
	if m.lookups == nil && m.resolver != nil {
		m.lookups = newSourceLookups(m.resolver, m.sources, nil, nil)
	}
	return m.lookups.get(ctx)
}

// WithDNS verifies the DNS lookup failures the notices report (design
// document "mail classification" 4.4) with v, and tells which reporting
// MTAs are sending servers by sources, unless the run is also given
// WithSendingSources, whose sources are used instead (in either order of
// the options), so both judgements see the same servers. sources here
// serves the verification only: the
// backscatter rule takes its sources from WithSendingSources alone. When v is
// also a HostResolver it resolves the names for the verification: a
// reporting MTA given as a host name is compared by its addresses too (see
// sourceMatcher). A nil verifier leaves the verification off: every detected
// failure stays not verified, as without the option; the sources of
// WithSendingSources stay in effect.
func WithDNS(v DNSVerifier, sources SendingSources) GroupOption {
	return func(cfg *groupConfig) {
		cfg.verifier, cfg.dnsSources = v, sources
	}
}

// newDNSVerification returns the verification of one run with verifier v,
// the sending sources and the lookups of the run (nil: the source host names
// resolved through v when it is a HostResolver). nil without a verifier.
func newDNSVerification(v DNSVerifier, sources SendingSources, lookups *sourceLookups) *dnsVerification {
	if v == nil {
		return nil
	}
	resolver, _ := v.(HostResolver)
	return &dnsVerification{verifier: v, matcher: sourceMatcher{sources: sources, resolver: resolver, lookups: lookups}}
}

// dnsVerification verifies the DNS lookup failures of the notices of one
// run and counts the notices whose name was sent to the verifier, by how
// each one ended. A nil value verifies nothing.
type dnsVerification struct {
	verifier DNSVerifier
	matcher  sourceMatcher
	checked  int                 // notices whose name was sent to the verifier
	statuses [dnsStatusCount]int // of those, how each one ended (see verdict), by DNSVerifyStatus
}

// dnsStatusCount is the number of DNSVerifyStatus values.
const dnsStatusCount = int(DNSStatusCheckOff) + 1

// verdict verifies the DNS lookup failure the diagnostic of a bounce
// reports, if any, and returns what CategorizeWith needs: the outcome and
// whether the reporting MTA is a sending server. The latter is
// decided only for the outcome exists, the one category it changes, so the
// names it may resolve are looked up for nothing else. pm is the notice,
// whose Received headers (not those of the returned original) tell whether
// the verification may ask
// authoritative servers directly (aboutSentMail). It is
// called before the transaction of the message is opened, since the
// verifier and the resolver may use the network. The zero verdict (not
// verified) is returned without a verifier, without a detected failure,
// when the verifier did not verify the name, and when the name exists but
// whether the reporting MTA is a sending server could not be told (sourceMatcher.match):
// the notice then counts as not verified too (inconclusive, or too old when
// only a lookup of its reporting MTA name could have told). Every notice
// whose name reaches the verifier counts once in statuses.
func (d *dnsVerification) verdict(ctx context.Context, b *models.Bounce, pm *ParsedMessage, noticeTime time.Time) DNSVerdict {
	if d == nil || b == nil {
		return DNSVerdict{}
	}
	failure, found := ParseDNSFailure(b.Diagnostic)
	if !found {
		return DNSVerdict{}
	}
	d.checked++
	outcome, status := d.verify(ctx, failure, b.RecipientDomain, noticeTime, d.aboutSentMail(ctx, pm))
	if outcome == "" {
		d.statuses[status]++
		return DNSVerdict{}
	}
	v := DNSVerdict{Outcome: outcome}
	if outcome == DNSOutcomeExists {
		live := LiveCheckAllowed(noticeTime, time.Now())
		matched, known := d.matcher.match(ctx, b.ReportingMTA, live)
		if !known {
			status = DNSStatusInconclusive
			if !live && d.matcher.resolver != nil && isHostNameMTA(b.ReportingMTA) {
				status = DNSStatusTooOld
			}
			d.statuses[status]++
			return DNSVerdict{}
		}
		v.FromSendingSource = matched
	}
	d.statuses[status]++
	return v
}

// verify asks the verifier about a failure and returns the outcome ("" =
// not verified) and how the verification ended: the status the verifier
// tells (DNSStatusVerifier), else verified or inconclusive. A status that
// does not fit the outcome is corrected, so every notice counts once.
func (d *dnsVerification) verify(ctx context.Context, f DNSFailure, recipientDomain string, noticeTime time.Time, direct bool) (DNSOutcome, DNSVerifyStatus) {
	var outcome DNSOutcome
	var status DNSVerifyStatus
	if sv, ok := d.verifier.(DNSStatusVerifier); ok {
		outcome, status = sv.VerifyStatus(ctx, f, recipientDomain, noticeTime, direct)
	} else if o, ok := d.verifier.Verify(ctx, f, recipientDomain, noticeTime, direct); ok {
		outcome, status = o, DNSStatusVerified
	}
	verified := status == DNSStatusVerified || status == DNSStatusReused
	switch {
	case outcome != "" && !verified:
		status = DNSStatusVerified
	case outcome == "" && (verified || status < 0 || int(status) >= dnsStatusCount):
		status = DNSStatusInconclusive
	}
	return outcome, status
}

// isHostNameMTA reports whether a reporting MTA is given as a host name
// (not empty, not an IP literal).
func isHostNameMTA(reportingMTA string) bool {
	host := mtaHost(reportingMTA)
	if host == "" {
		return false
	}
	_, err := netip.ParseAddr(trimIPv6Tag(host))
	return err != nil
}

// maxDirectHops bounds the Received headers of a notice, from the top, that
// aboutSentMail reads.
const maxDirectHops = 8

// aboutSentMail reports whether a notice clearly concerns mail sent from the
// sending sources, told by a signal its sender cannot forge: the Received
// headers the inbound servers of the mailbox added to the notice when it
// arrived (pm.Headers, topmost
// first; whoever sends a notice can add headers only below them). At most
// the maxDirectHops topmost ones are read (noticeEntry): a hop whose from
// clause records no address is passed over, and so is one whose connecting
// address is internal (internalHop: an internal relay); the first hop with
// any other connecting address decides. The notice concerns mail sent from
// the sending sources when that address is one of the sending sources as an
// address: in a configured prefix, or, when the lookups of the run
// (sourceLookups, made now on their first need) are complete, among the
// addresses of the source host names and the prefixes of the SPF records.
// A sending server then handed the notice in: it generated the notice, or
// relayed it to the inbound servers of the mailbox (a hosted mailbox
// records the sending server as the connecting address). A host name is never compared as text here, and
// the content of the notice (its reporting MTA, the Received headers and
// the DKIM signature of the returned original) is not looked at: whoever
// ever received mail from the sending sources can copy it. Nothing configured for the
// mailbox (SendingSources.Empty), no such hop within the bound, a hop that
// cannot be judged (receivedFromAddr) or incomplete lookups that the
// address would need: false. Only a true answer lets the verification ask
// the authoritative servers of the name directly (DNSVerifier): a forged
// notice chooses the name and the recipient domain, and asking the name
// servers of its author's domain from this server would reveal its address
// and let the notice choose where the queries go.
func (d *dnsVerification) aboutSentMail(ctx context.Context, pm *ParsedMessage) bool {
	sources := d.matcher.sources
	if sources.Empty() || pm == nil {
		return false
	}
	addr, ok := noticeEntry(pm.Headers["Received"])
	if !ok {
		return false
	}
	if sources.matchAddr(addr) {
		return true
	}
	extra := d.matcher.extra(ctx)
	return extra.complete() && extra.contains(addr)
}

// noticeEntry returns the connecting address of the first hop of a notice,
// from the top, that is not internal (internalHop), among the maxDirectHops
// topmost Received headers (received: their values one per line, topmost
// first, as ParsedMessage.Headers holds them). ok is false when there is no
// such hop within the bound, or a header before it cannot be judged.
func noticeEntry(received string) (netip.Addr, bool) {
	if received == "" {
		return netip.Addr{}, false
	}
	for i, line := range strings.Split(received, "\n") {
		if i == maxDirectHops {
			break
		}
		addr, found, ok := receivedFromAddr(line)
		if !ok {
			return netip.Addr{}, false
		}
		if found && !internalHop(addr) {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

// sharedAddressSpace is 100.64.0.0/10 (RFC 6598, carrier-grade NAT).
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// internalHop reports whether a connecting address belongs to a relay of an
// internal network: loopback, private (RFC 1918, IPv6 unique local),
// shared (100.64.0.0/10) or link-local. Whoever sends a notice cannot add a
// hop above the ones the inbound servers of the mailbox added, so these
// are passed over.
func internalHop(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || sharedAddressSpace.Contains(a)
}

// receivedFromAddr returns the connecting address a Received header (folded
// onto one line) records in its from clause: the words from "from" up to the
// next clause (by, via, with, id or for), outside comments, when "from"
// comes first. The address is an IP literal in a comment of the clause
// (Postfix and Sendmail "(mail.example.jp [192.0.2.5])", Exim "([192.0.2.5]:25
// helo=...)", qmail and Exchange "(192.0.2.5)", "(IPv6:2001:db8::5)") or,
// when no comment holds one, the word after "from" (Exim "from [192.0.2.5]
// (helo=...)"), unmapped. The argument of a helo= word and the word after
// HELO or EHLO in a comment are never taken: the client chose them (so is
// the word after "from" in the Postfix form, which the address in its
// comment then outranks). found is false when there is no from clause or
// it records no address; ok is false when the header cannot be judged:
// longer than maxReceivedLineBytes, or with two different addresses in the
// comments of its from clause.
func receivedFromAddr(line string) (addr netip.Addr, found, ok bool) {
	if len(line) > maxReceivedLineBytes {
		return netip.Addr{}, false, false
	}
	tokens := receivedTokens(line)
	start := -1
	for i, t := range tokens {
		if t.depth != 0 {
			continue
		}
		if strings.EqualFold(t.text, "from") {
			start = i
		}
		break
	}
	if start < 0 {
		return netip.Addr{}, false, true
	}
	var word netip.Addr
	var inComments []netip.Addr
	for i := start + 1; i < len(tokens); i++ {
		t := tokens[i]
		if t.depth == 0 {
			if isReceivedClause(t.text) {
				break
			}
			if i == start+1 {
				if a, ok := ipLiteral(t.text); ok {
					word = a
				}
			}
			continue
		}
		lower := strings.ToLower(t.text)
		switch {
		case strings.HasPrefix(lower, "helo=") || strings.HasPrefix(lower, "ehlo="):
			continue
		case lower == "helo" || lower == "ehlo":
			if i+1 < len(tokens) && tokens[i+1].depth == t.depth {
				i++
			}
			continue
		}
		candidates := bracketed(t.text)
		if a, ok := ipLiteral(t.text); ok {
			candidates = []string{a.String()}
		}
		for _, c := range candidates {
			a, ok := ipLiteral(c)
			if !ok || slices.Contains(inComments, a) {
				continue
			}
			inComments = append(inComments, a)
		}
	}
	switch {
	case len(inComments) > 1:
		return netip.Addr{}, false, false
	case len(inComments) == 1:
		return inComments[0], true, true
	case word.IsValid():
		return word, true, true
	}
	return netip.Addr{}, false, true
}

// isReceivedClause reports whether a word outside comments starts another
// clause of a Received header than from (RFC 5321 4.4).
func isReceivedClause(word string) bool {
	switch strings.ToLower(word) {
	case "by", "via", "with", "id", "for":
		return true
	}
	return false
}

// reportSummary adds the progress line of the verification when the name
// of any notice was sent to the verifier during the run: how many notices
// and how each one ended (the counts add up to the number of notices; the
// ones not checked because the DNS check is off are named only when there
// are any).
func (d *dnsVerification) reportSummary(progress Progress) {
	if d == nil || d.checked == 0 {
		return
	}
	s := d.statuses
	line := fmt.Sprintf("dns: %d notice(s): %d verified, %d reused from stored results, %d over budget (run again), "+
		"%d not applicable, %d inconclusive, %d too old for a live check",
		d.checked, s[DNSStatusVerified], s[DNSStatusReused], s[DNSStatusOverBudget],
		s[DNSStatusNotApplicable], s[DNSStatusInconclusive], s[DNSStatusTooOld])
	if n := s[DNSStatusCheckOff]; n > 0 {
		line += fmt.Sprintf(", %d not checked (the DNS check is off)", n)
	}
	report(progress, line)
}

// DNSLiveMaxAge is how old a notice (its noticeTime) may be for the network
// to be asked about it: the live verification of the name it reports
// (DNSVerifier: an older notice uses a stored result only, else stays not
// verified) and the lookup of its reporting MTA (sourceMatcher.match: an older
// notice whose reporting MTA only a lookup could tell stays not verified).
// Today's DNS says little about a lookup failure of days ago, and a full
// reclassify must not turn the old notices into new alerts by it.
const DNSLiveMaxAge = 48 * time.Hour

// LiveCheckAllowed reports whether a notice received at noticeTime is
// recent enough at now for live lookups (DNSLiveMaxAge); a time that is
// zero or after now counts as now. The grouping and the verifier
// (app/modules/dnscheck) both decide by it.
func LiveCheckAllowed(noticeTime, now time.Time) bool {
	if noticeTime.IsZero() || noticeTime.After(now) {
		return true
	}
	return now.Sub(noticeTime) <= DNSLiveMaxAge
}

// noticeTime is the time a notice was received, used to pick a stored
// verification result: the IMAP INTERNALDATE when known, else the date of
// the message.
func noticeTime(msg *models.Message) time.Time {
	if msg.ReceivedAt.Valid && !msg.ReceivedAt.Time.IsZero() {
		return msg.ReceivedAt.Time
	}
	return msg.Date
}
