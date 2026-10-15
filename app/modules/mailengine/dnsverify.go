package mailengine

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
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
// grouping sets it only for a notice that clearly concerns mail we sent
// (dnsVerification.aboutOurMail), since a forged notice chooses both the
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
	// ours could not be told.
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
	// allowed for it either, could have told whether that is ours).
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
// one of our own sending servers (4.4). ResolveHost resolves one of our own
// host names and tells whether it was answered (OwnHostResolver).
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
	OwnHostResolver
	ResolveReporter(ctx context.Context, host string) (addrs []netip.Addr, answered bool)
}

// ownMatcher tells our own reporting MTAs during one run (4.4). Without a
// resolver it is OwnSources.Match. With one, a reporting MTA given as a
// host name that is none of the own host names is resolved
// (ResolveReporter), and its addresses, like an IP literal, are compared
// with the prefixes and with the addresses the run looks up (ownLookups:
// the addresses of the own host names and the SPF records of the monitored
// domains; the lookups of the run when given, else the host names resolved
// through resolver). Those are looked up on the first need only and kept
// for the run, so a run without a verified DNS lookup failure looks nothing
// up for it. The zero value matches everything (the empty configuration).
type ownMatcher struct {
	own      OwnSources
	resolver HostResolver // nil: reporting MTA names are compared as text only
	lookups  *ownLookups  // nil: made from resolver on first need
}

// match reports whether the reporting MTA is one of our own sending
// servers (own) and whether that could be told at all (known). With an
// empty configuration every reporting MTA is ours. Otherwise an IP literal
// is compared with our addresses (matchAddrs), and a host name as text
// with the own host names and the host names of the SPF records; a host
// name the text does not match is resolved (ResolveReporter) when live
// (the notice is recent enough, LiveCheckAllowed) and its addresses are
// compared like an IP literal. Only addresses that were found and are none
// of ours make the reporting MTA someone else's. known is false, the
// question left open, when the reporting MTA is empty or unreadable, when
// it is a host name that could not be resolved (no resolver, not live,
// refused as no host name of two labels or more, not answered) or was
// answered without any address (NXDOMAIN, no data: a name that does not
// resolve tells nothing about whose server wrote it), and when its
// addresses (or its IP literal) matched nothing while the lookups of the
// run are incomplete (an own host name not answered, an SPF record not
// evaluated completely: an address of ours may be missing). The limits of
// the run, no answer or a cancellation would otherwise make the notice
// external depending on the order of the notices, and an odd name would
// hide a failure of our own resolver. A cancellation of ctx leaves what was
// looked up unremembered.
func (m *ownMatcher) match(ctx context.Context, reportingMTA string, live bool) (own, known bool) {
	if m.own.Empty() {
		return true, true
	}
	host := mtaHost(reportingMTA)
	if host == "" {
		return false, false
	}
	if addr, err := netip.ParseAddr(trimIPv6Tag(host)); err == nil {
		return m.matchAddrs(ctx, []netip.Addr{addr})
	}
	if m.own.matchMTA(host) {
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
// of ours: in a configured prefix, one of the given resolved addresses, or
// one the run looks up (ownLookups: only when nothing else matched). known
// is false when none matched while the lookups of the run are incomplete
// (ownExtra.complete), and when there is no address at all.
func (m *ownMatcher) matchAddrs(ctx context.Context, addrs []netip.Addr) (own, known bool) {
	if len(addrs) == 0 {
		return false, false
	}
	unmapped := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if m.own.matchAddr(a) {
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
// (nothing without lookups or a resolver of the own host names).
func (m *ownMatcher) extra(ctx context.Context) ownExtra {
	if m.lookups == nil && m.resolver != nil {
		m.lookups = newOwnLookups(m.resolver, m.own, nil, nil)
	}
	return m.lookups.get(ctx)
}

// WithDNS verifies the DNS lookup failures the notices report (design
// document "mail classification" 4.4) with v, and tells our own reporting
// MTAs by own, unless the run is also given WithOwnSources, whose sources
// are used instead (in either order of the options), so both judgements
// see the same servers. own here serves the verification only: the
// backscatter rule takes its sources from WithOwnSources alone. When v is
// also a HostResolver it resolves the names for the verification: a
// reporting MTA given as a host name is compared by its addresses too (see
// ownMatcher). A nil verifier leaves the verification off: every detected
// failure stays not verified, as without the option; the sources of
// WithOwnSources stay in effect.
func WithDNS(v DNSVerifier, own OwnSources) GroupOption {
	return func(cfg *groupConfig) {
		cfg.verifier, cfg.dnsOwn = v, own
	}
}

// newDNSVerification returns the verification of one run with verifier v,
// our own sources own and the lookups of the run (nil: the own host names
// resolved through v when it is a HostResolver). nil without a verifier.
func newDNSVerification(v DNSVerifier, own OwnSources, lookups *ownLookups) *dnsVerification {
	if v == nil {
		return nil
	}
	resolver, _ := v.(HostResolver)
	return &dnsVerification{verifier: v, own: ownMatcher{own: own, resolver: resolver, lookups: lookups}}
}

// dnsVerification verifies the DNS lookup failures of the notices of one
// run and counts the notices whose name was sent to the verifier, by how
// each one ended. A nil value verifies nothing.
type dnsVerification struct {
	verifier DNSVerifier
	own      ownMatcher
	checked  int                 // notices whose name was sent to the verifier
	statuses [dnsStatusCount]int // of those, how each one ended (see verdict), by DNSVerifyStatus
}

// dnsStatusCount is the number of DNSVerifyStatus values.
const dnsStatusCount = int(DNSStatusCheckOff) + 1

// verdict verifies the DNS lookup failure the diagnostic of a bounce
// reports, if any, and returns what CategorizeWith needs: the outcome and
// whether the reporting MTA is one of our own servers. The latter is
// decided only for the outcome exists, the one category it changes, so the
// names it may resolve are looked up for nothing else. pm is the notice
// and mailbox the monitored address it reached, which tell whether the
// verification may ask authoritative servers directly (aboutOurMail). It is
// called before the transaction of the message is opened, since the
// verifier and the resolver may use the network. The zero verdict (not
// verified) is returned without a verifier, without a detected failure,
// when the verifier did not verify the name, and when the name exists but
// whether the reporting MTA is ours could not be told (ownMatcher.match):
// the notice then counts as not verified too (inconclusive, or too old when
// only a lookup of its reporting MTA name could have told). Every notice
// whose name reaches the verifier counts once in statuses.
func (d *dnsVerification) verdict(ctx context.Context, b *models.Bounce, pm *ParsedMessage, mailbox string, noticeTime time.Time) DNSVerdict {
	if d == nil || b == nil {
		return DNSVerdict{}
	}
	failure, found := ParseDNSFailure(b.Diagnostic)
	if !found {
		return DNSVerdict{}
	}
	d.checked++
	outcome, status := d.verify(ctx, failure, b.RecipientDomain, noticeTime, d.aboutOurMail(b, pm, mailbox))
	if outcome == "" {
		d.statuses[status]++
		return DNSVerdict{}
	}
	v := DNSVerdict{Outcome: outcome}
	if outcome == DNSOutcomeExists {
		live := LiveCheckAllowed(noticeTime, time.Now())
		own, known := d.own.match(ctx, b.ReportingMTA, live)
		if !known {
			status = DNSStatusInconclusive
			if !live && d.own.resolver != nil && isHostNameMTA(b.ReportingMTA) {
				status = DNSStatusTooOld
			}
			d.statuses[status]++
			return DNSVerdict{}
		}
		v.OwnMTA = own
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

// aboutOurMail reports whether a notice clearly concerns mail we sent, told
// from the notice alone (no lookup, so the answer never depends on the
// order of the notices or on the network): sources are configured for the
// mailbox (OwnSources.Empty is false), and the reporting MTA is one of them
// as text or as an IP literal (OwnSources.matchMTA), or a Received header
// of the returned original shows one of them (matchReceivedLines, within
// its bounds: an original past them shows nothing), or the original carries
// a DKIM-Signature whose signing domain is of the organizational domain of
// the monitored address (not verified, like the exemption of the
// backscatter rule). Only then may the verification ask the authoritative
// servers of the name directly (DNSVerifier): a forged notice chooses the
// name and the recipient domain, and asking the name servers of its
// author's domain from this server would reveal its address and let the
// notice choose where the queries go.
func (d *dnsVerification) aboutOurMail(b *models.Bounce, pm *ParsedMessage, mailbox string) bool {
	own := d.own.own
	if own.Empty() {
		return false
	}
	if own.matchMTA(b.ReportingMTA) {
		return true
	}
	if pm == nil || pm.OriginalMessage == nil {
		return false
	}
	om := pm.OriginalMessage
	if shows, _ := own.matchReceivedLines(om.Received); shows {
		return true
	}
	if domain := newMailboxIdentity(mailbox).domain; domain != "" {
		for _, signer := range om.DKIMDomains {
			if organizationalDomain(signer) == domain {
				return true
			}
		}
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
// verified) and the lookup of its reporting MTA (ownMatcher.match: an older
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
