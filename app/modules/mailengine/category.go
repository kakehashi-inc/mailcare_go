package mailengine

import (
	"net/netip"
	"regexp"
	"strings"

	"mailcare/app/models"
)

// Category is the outcome of the category rule table for one bounce (design
// 5.4): the kind of problem, the unit an administrator acts on, the party
// that decides the outcome and whether the mail administrator can act at all.
type Category struct {
	Category    string
	UnitValue   string
	Authority   string
	Actionable  bool
	Responsible string
	Reason      string // name of the matching rule
}

// unitKind names where a category takes its unit_value from.
type unitKind int

const (
	unitSendingIP        unitKind = iota // our sending IP quoted in the diagnostic, else the reporting MTA
	unitSendingDomain                    // domain of the original sender, else the reporting MTA
	unitSenderAddress                    // original sender address, else a non-recipient address in the diagnostic, else the reporting MTA
	unitReportingMTA                     // our MTA that produced the notice, else the sending domain
	unitRecipientAddress                 // the failed recipient address, else its domain
	unitRecipientDomain                  // the failed recipient's domain
)

// authorityKind names where a category takes its authority from.
type authorityKind int

const (
	authorityNone            authorityKind = iota // recipient-side categories carry no authority
	authorityBlacklist                            // blacklist provider named in the diagnostic, else the recipient domain
	authorityRecipientDomain                      // the recipient domain
	authorityStatusTemplate                       // "<status> <diagnostic template>" (unknown_failure)
)

// categoryDef describes one category: the columns every group of that
// category gets. The rule table below decides which category applies.
type categoryDef struct {
	actionable  bool
	responsible string
	unit        unitKind
	authority   authorityKind
}

// categoryDefs is the category table of design 5.4.
var categoryDefs = map[string]categoryDef{
	categoryIPBlocked:       {true, responsibleSender, unitSendingIP, authorityBlacklist},
	categoryRateLimited:     {true, responsibleSender, unitSendingIP, authorityRecipientDomain},
	categoryAuthFailure:     {true, responsibleSender, unitSendingDomain, authorityRecipientDomain},
	categorySenderBlocked:   {true, responsibleSender, unitSenderAddress, authorityRecipientDomain},
	categoryContentRejected: {true, responsibleSender, unitSenderAddress, authorityRecipientDomain},
	categoryMessageTooLarge: {true, responsibleSender, unitSenderAddress, authorityRecipientDomain},
	categoryServerConfig:    {true, responsibleSender, unitReportingMTA, authorityRecipientDomain},
	categoryUnknownFailure:  {true, responsibleUnknown, unitRecipientDomain, authorityStatusTemplate},
	categoryUserUnknown:     {false, responsibleRecipient, unitRecipientAddress, authorityNone},
	categoryMailboxFull:     {false, responsibleRecipient, unitRecipientAddress, authorityNone},
	categoryMailboxDisabled: {false, responsibleRecipient, unitRecipientAddress, authorityNone},
	categoryDomainNotFound:  {false, responsibleDomain, unitRecipientDomain, authorityNone},
	categoryDeliveryDelay:   {false, responsibleDomain, unitRecipientDomain, authorityNone},
}

// categoryFacts is what the rules look at: the bounce row plus a few values
// derived once per bounce.
type categoryFacts struct {
	kind   string
	status string
	text   string // lower-cased diagnostic
	bounce *models.Bounce

	senderAddress string // address of the original sender ("" when unknown)
	senderDomain  string // its domain
	sendingIP     string // our sending IP quoted in the diagnostic
	blacklist     string // known blacklist provider named in the diagnostic
}

// categoryRule is one row of the rule table. Rules are evaluated in order and
// the first match wins.
type categoryRule struct {
	name     string
	category string
	match    func(f *categoryFacts) bool
}

// categoryRules is the rule table. Order matters:
//
//  1. status codes that clearly name a recipient-side problem win over any
//     wording (a "blocked" in a 5.1.1 text is still an unknown user);
//  2. the actionable categories, most specific first (blacklist and rate
//     limit wording, then delays, authentication, sender, size, content,
//     server configuration);
//  3. the recipient-side categories by wording;
//  4. everything else is an unknown failure.
var categoryRules = []categoryRule{
	// --- 1. recipient-side status codes ---
	{"status_user_unknown", categoryUserUnknown, func(f *categoryFacts) bool {
		return statusIn(f.status, "5.1.1", "5.1.0", "5.1.3", "5.1.6", "5.1.10")
	}},
	{"status_mailbox_full", categoryMailboxFull, func(f *categoryFacts) bool {
		return statusIn(f.status, "4.2.2", "5.2.2")
	}},
	{"status_mailbox_disabled", categoryMailboxDisabled, func(f *categoryFacts) bool {
		return statusIn(f.status, "5.2.1")
	}},
	{"status_domain_not_found", categoryDomainNotFound, func(f *categoryFacts) bool {
		return statusIn(f.status, "5.1.2", "5.4.4", "5.4.6")
	}},

	// --- 2. actionable categories ---
	{"ip_blocked", categoryIPBlocked, func(f *categoryFacts) bool {
		if f.senderRejection() {
			return false // "sender address rejected" texts belong to sender_blocked
		}
		if f.blacklist != "" || ipBlockedRe.MatchString(f.text) {
			return true
		}
		// A policy status (x.7.0 / x.7.1) that quotes our IP and speaks of
		// blocking, without rate-limit or content wording.
		return policyStatusRe.MatchString(f.status) && f.sendingIP != "" && blockWordRe.MatchString(f.text) &&
			!rateLimitedRe.MatchString(f.text) && !contentRejectedRe.MatchString(f.text)
	}},
	{"rate_limited", categoryRateLimited, func(f *categoryFacts) bool {
		if rateLimitedRe.MatchString(f.text) {
			return true
		}
		// 4.7.0 without blocking wording (checked above) is a throttle,
		// unless it is about TLS / the session itself.
		return f.status == "4.7.0" && !serverConfigRe.MatchString(f.text)
	}},
	{"delayed_kind", categoryDeliveryDelay, func(f *categoryFacts) bool {
		return f.kind == bounceKindDelayed
	}},
	{"auth_failure", categoryAuthFailure, func(f *categoryFacts) bool {
		if authStatusRe.MatchString(f.status) {
			return true
		}
		if !authFailureRe.MatchString(f.text) {
			return false
		}
		// "not authorized to relay" / "HELO command rejected" are server
		// configuration problems, not sender authentication.
		return !relayRe.MatchString(f.text) && !heloRejectedRe.MatchString(f.text)
	}},
	{"sender_blocked", categorySenderBlocked, func(f *categoryFacts) bool {
		return f.senderRejection()
	}},
	{"message_too_large", categoryMessageTooLarge, func(f *categoryFacts) bool {
		return statusIn(f.status, "5.2.3", "5.3.4") || messageTooLargeRe.MatchString(f.text)
	}},
	{"content_rejected", categoryContentRejected, func(f *categoryFacts) bool {
		return contentRejectedRe.MatchString(f.text)
	}},
	{"server_config", categoryServerConfig, func(f *categoryFacts) bool {
		return statusClass(f.status, "5.5.", "5.6.") || f.status == "5.3.0" || serverConfigRe.MatchString(f.text)
	}},

	// --- 3. recipient-side wording ---
	{"user_unknown", categoryUserUnknown, func(f *categoryFacts) bool {
		return userUnknownRe.MatchString(f.text)
	}},
	{"mailbox_full", categoryMailboxFull, func(f *categoryFacts) bool {
		return mailboxFullRe.MatchString(f.text)
	}},
	{"mailbox_disabled", categoryMailboxDisabled, func(f *categoryFacts) bool {
		return mailboxDisabledRe.MatchString(f.text)
	}},
	{"domain_not_found", categoryDomainNotFound, func(f *categoryFacts) bool {
		return domainNotFoundRe.MatchString(f.text)
	}},
	{"delivery_delay", categoryDeliveryDelay, func(f *categoryFacts) bool {
		return statusClass(f.status, "4.4.") || deliveryDelayRe.MatchString(f.text) || strings.HasPrefix(f.status, "4.")
	}},

	// --- 4. fallback ---
	{"unknown_failure", categoryUnknownFailure, func(f *categoryFacts) bool { return true }},
}

// Wording tables. Every expression is matched against the lower-cased
// diagnostic text; keep them grouped by category so that a new phrase has an
// obvious place to go.
var (
	// ip_blocked: blacklist listings, reputation blocks and the Gmail / Outlook
	// "your IP" notices. Provider names are matched by blacklistProviders.
	ipBlockedRe = regexp.MustCompile(strings.Join([]string{
		`blacklist`, `black[ -]list`, `block[ -]?list`, `\bdnsbl\b`, `\brbls?\b`,
		`(?:is|are|currently|been) listed`, `listed (?:at|in|on|by)`,
		`blocked (?:using|by|due to|because)`,
		`(?:\bip\b|address|host|server|network|connection|client)[^.\n]{0,40}(?:has been |is |was |are |currently )?(?:temporarily )?blocked`,
		`\bbanned\b`, `poor reputation`, `bad reputation`, `low reputation`, `reputation (?:of|for) (?:your|the|this) ip`,
		`ip[^.\n]{0,30}reputation`, `unsolicited mail originating from your ip`,
		`our system has detected[^.]{0,80}(?:your ip|ip address)`,
		`access denied[^\n]{0,30}\bip\b`, `part of their network`,
	}, "|"))
	// blockWordRe is the weaker signal used together with a policy status and
	// a quoted IP.
	blockWordRe    = regexp.MustCompile(`blocked|refused|denied|rejected|not allowed`)
	policyStatusRe = regexp.MustCompile(`^[45]\.7\.[01]$`)

	// rate_limited: throttling and "try again later".
	rateLimitedRe = regexp.MustCompile(strings.Join([]string{
		`too many (?:messages|connections|recipients|emails|requests|concurrent)`, `too many\b`,
		`rate ?limit`, `try again later`, `throttl`, `temporarily deferred`,
		`receiving mail at a rate`, `too fast`, `too frequent`, `sending rate`, `messages per (?:hour|minute|day|second)`,
		`exceeded[^\n]{0,30}(?:rate|limit of messages|connections)`, `connection rate`, `slow down`,
		`unusual rate`,
	}, "|"))

	// auth_failure: SPF / DKIM / DMARC / PTR / HELO checks.
	authFailureRe = regexp.MustCompile(strings.Join([]string{
		`\bspf\b`, `\bdkim\b`, `\bdmarc\b`, `not authorized`, `unauthorized`, `unauthenticated`, `not authenticated`,
		`authentication (?:fail|required|error)`, `reverse dns`, `\brdns\b`, `\bptr\b`, `\bhelo\b`, `\behlo\b`,
		`sender id`, `does not designate`, `sender policy`, `domain owner`,
	}, "|"))
	authStatusRe   = regexp.MustCompile(`^5\.7\.2[3-7]$`)
	relayRe        = regexp.MustCompile(`relay`)
	heloRejectedRe = regexp.MustCompile(`(?:helo|ehlo) command rejected`)

	// sender_blocked: the envelope sender / from address is refused.
	senderBlockedRe = regexp.MustCompile(strings.Join([]string{
		`sender(?: address| domain| e-?mail| email address)?[^\n]{0,40}(?:rejected|blocked|denied|not allowed|refused|listed|banned|verify failed|verification failed)`,
		`(?:rejected|blocked|denied|banned|bad|invalid|unknown|forged)\s+(?:sender|envelope[ -]from|mail from)`,
		`(?:envelope[ -]from|mail from|from address|return-path)[^\n]{0,40}(?:rejected|blocked|denied|refused)`,
		`blocked using [^\s;]*(?:dbl|uribl|surbl)`,
	}, "|"))

	// message_too_large: size limits.
	messageTooLargeRe = regexp.MustCompile(strings.Join([]string{
		`too large`, `too big`, `message size`, `size limit`, `size exceed`,
		`exceeds? (?:the )?(?:maximum|max|allowed|fixed|size|limit|message)`, `max(?:imum)? (?:message |allowed )?size`,
		`message length`, `content too long`,
	}, "|"))

	// content_rejected: the message itself was refused.
	contentRejectedRe = regexp.MustCompile(strings.Join([]string{
		`\bspam\b`, `spammy`, `\bcontent\b`, `\bvirus`, `malware`, `attachment`, `message rejected`,
		`rejected (?:due to|for|by) (?:policy|content|security)`, `policy rejection`, `our system has detected`,
		`suspicious`, `unsolicited`, `\bjunk\b`, `phish`, `\burl\b`, `likely (?:spam|unsolicited)`,
		`quarantine`, `filtered`, `message (?:looks|appears)`,
	}, "|"))

	// server_config: relaying, TLS and protocol trouble on our side.
	serverConfigRe = regexp.MustCompile(strings.Join([]string{
		`relay`, `\btls\b`, `starttls`, `encryption required`, `(?:helo|ehlo) command rejected`, `need fully-qualified`,
		`command (?:rejected|not recognized|unrecognized|not implemented)`, `syntax error`, `protocol error`, `bad sequence`,
		`invalid command`, `certificate`, `cipher`, `handshake`, `not accepting mail`, `pipelining`,
	}, "|"))

	// user_unknown / mailbox_full / mailbox_disabled / domain_not_found /
	// delivery_delay: recipient-side wording.
	userUnknownRe = regexp.MustCompile(strings.Join([]string{
		`user unknown`, `unknown user`, `no such user`, `no such recipient`, `no such address`, `recipient not found`,
		`recipnotfound`, `does not exist`, `doesn't exist`, `invalid recipient`, `recipient address rejected`,
		`unknown recipient`, `no mailbox here`, `mailbox not found`, `address not found`, `not our customer`,
		`user not found`, `invalid mailbox`, `mailbox unavailable`, `unknown address`, `bad destination mailbox`,
		`invalid address`, `address unknown`, `no valid recipients`,
	}, "|"))
	mailboxFullRe = regexp.MustCompile(strings.Join([]string{
		`over quota`, `quota exceeded`, `exceeded[^\n]{0,20}quota`, `out of storage`, `mailbox (?:is )?full`,
		`storage limit`, `insufficient (?:system )?storage`, `no space`, `disk full`, `mailbox size limit`,
		`mailbox exceeds`, `overquota`,
	}, "|"))
	mailboxDisabledRe = regexp.MustCompile(strings.Join([]string{
		`disabled`, `inactive`, `suspended`, `deactivated`, `not accepting messages`,
		`account (?:is |has been )?(?:closed|locked|expired)`,
	}, "|"))
	domainNotFoundRe = regexp.MustCompile(strings.Join([]string{
		`domain not found`, `domain does not exist`, `host or domain name not found`, `no mx`, `no such domain`,
		`unrouteable`, `unroutable`, `name service error`, `dns error`, `nxdomain`, `domain lookup failed`,
		`hostname lookup failed`, `does not accept mail`, `null mx`, `no route to host`, `host not found`,
	}, "|"))
	deliveryDelayRe = regexp.MustCompile(strings.Join([]string{
		`connection timed out`, `timed out`, `greylist`, `grey list`, `not yet been delivered`, `will keep trying`,
		`connection refused`, `temporarily`, `deferred`,
	}, "|"))
)

// blacklistProviders maps the names of well-known blacklist / reputation
// services to their registrable domain (the authority of an ip_blocked
// group). The pattern is matched against the lower-cased diagnostic, so a
// bare name, a zone ("zen.spamhaus.org") or a URL all match.
var blacklistProviders = []struct {
	pattern   *regexp.Regexp
	authority string
}{
	{regexp.MustCompile(`spamhaus`), "spamhaus.org"},
	{regexp.MustCompile(`spamcop`), "spamcop.net"},
	{regexp.MustCompile(`sorbs`), "sorbs.net"},
	{regexp.MustCompile(`barracuda`), "barracudacentral.org"},
	{regexp.MustCompile(`uceprotect`), "uceprotect.net"},
	{regexp.MustCompile(`proofpoint`), "proofpoint.com"},
	{regexp.MustCompile(`cloudmark`), "cloudmark.com"},
	{regexp.MustCompile(`invaluement|ivmsip|ivmuri`), "invaluement.com"},
	{regexp.MustCompile(`abuseat|\bcbl\b`), "abuseat.org"},
	{regexp.MustCompile(`mailspike`), "mailspike.net"},
	{regexp.MustCompile(`spamrats`), "spamrats.com"},
	{regexp.MustCompile(`senderscore|returnpath|return path`), "senderscore.org"},
	{regexp.MustCompile(`surbl`), "surbl.org"},
	{regexp.MustCompile(`uribl`), "uribl.com"},
	{regexp.MustCompile(`\bpsbl\b`), "psbl.org"},
	{regexp.MustCompile(`nixspam`), "nixspam.org"},
	{regexp.MustCompile(`blocklist\.de`), "blocklist.de"},
	{regexp.MustCompile(`mail-abuse|mailabuse`), "mail-abuse.com"},
	{regexp.MustCompile(`lashback`), "lashback.com"},
	{regexp.MustCompile(`junkemailfilter|hostkarma`), "junkemailfilter.com"},
	{regexp.MustCompile(`trendmicro|trend micro`), "trendmicro.com"},
	{regexp.MustCompile(`mimecast`), "mimecast.com"},
	{regexp.MustCompile(`talos|senderbase|ironport`), "talosintelligence.com"},
	{regexp.MustCompile(`spameatingmonkey`), "spameatingmonkey.net"},
}

// notAuthorityDomains are registrable domains that appear in diagnostics as
// help pages of the receiving service, never as a blacklist provider; the
// authority then stays the recipient domain.
var notAuthorityDomains = map[string]bool{
	"google.com": true, "googlemail.com": true, "gmail.com": true, "microsoft.com": true, "outlook.com": true,
	"office.com": true, "office365.com": true, "live.com": true, "hotmail.com": true, "msn.com": true,
	"yahoo.com": true, "yahoo.co.jp": true, "yahoo.net": true, "aol.com": true, "apple.com": true, "icloud.com": true,
	"me.com": true, "mail.ru": true, "yandex.ru": true, "docomo.ne.jp": true, "ezweb.ne.jp": true, "au.com": true,
	"softbank.ne.jp": true, "softbank.jp": true, "nifty.com": true, "biglobe.ne.jp": true, "ocn.ne.jp": true,
	"so-net.ne.jp": true, "ietf.org": true, "rfc-editor.org": true,
}

// listingSignalRe tells that a hostname or URL in the text names the list the
// IP is on (used for the authority of providers not in blacklistProviders).
var listingSignalRe = regexp.MustCompile(`blacklist|black[ -]list|block[ -]?list|dnsbl|\brbl\b|listed|blocked using|blocked by|reputation`)

// Categorize runs the rule table over one extracted bounce and derives the
// unit and authority of its group.
func Categorize(b *models.Bounce, kind string) Category {
	if b == nil {
		b = &models.Bounce{}
	}
	f := newCategoryFacts(b, kind)
	for _, rule := range categoryRules {
		if !rule.match(f) {
			continue
		}
		def := categoryDefs[rule.category]
		return Category{
			Category:    rule.category,
			UnitValue:   f.unit(def.unit),
			Authority:   f.authority(def.authority),
			Actionable:  def.actionable,
			Responsible: def.responsible,
			Reason:      rule.name,
		}
	}
	// Unreachable: the last rule always matches.
	return Category{Category: categoryUnknownFailure, Responsible: responsibleUnknown, Actionable: true}
}

func newCategoryFacts(b *models.Bounce, kind string) *categoryFacts {
	f := &categoryFacts{
		kind:   kind,
		status: strings.TrimSpace(b.StatusCode),
		text:   strings.ToLower(b.Diagnostic),
		bounce: b,
	}
	f.senderAddress = strings.ToLower(addressIn(b.OriginalFrom))
	f.senderDomain = domainOf(f.senderAddress)
	f.sendingIP = extractSendingIP(f.text, strings.ToLower(b.RemoteIP))
	f.blacklist = knownBlacklist(f.text)
	return f
}

// senderRejection reports whether the diagnostic refuses the sender address
// itself (design 5.4 sender_blocked): explicit "sender ... rejected" wording,
// or a policy status that quotes the original sender's address.
func (f *categoryFacts) senderRejection() bool {
	if senderBlockedRe.MatchString(f.text) {
		return true
	}
	return f.senderAddress != "" && policyStatusRe.MatchString(f.status) && strings.Contains(f.text, f.senderAddress)
}

// unit resolves the unit_value of a category, lower-cased, with the fallbacks
// of design 5.4.
func (f *categoryFacts) unit(kind unitKind) string {
	b := f.bounce
	var candidates []string
	switch kind {
	case unitSendingIP:
		candidates = []string{f.sendingIP, b.ReportingMTA}
	case unitSendingDomain:
		candidates = []string{f.senderDomain, b.ReportingMTA}
	case unitSenderAddress:
		candidates = []string{f.senderAddress, otherAddressIn(f.text, b.Recipient), f.senderDomain, b.ReportingMTA}
	case unitReportingMTA:
		candidates = []string{b.ReportingMTA, f.senderDomain}
	case unitRecipientAddress:
		candidates = []string{b.Recipient, b.RecipientDomain}
	case unitRecipientDomain:
		candidates = []string{b.RecipientDomain}
	}
	// Final fallback shared by every kind, so that a group never shows an
	// empty unit: the recipient domain, then the remote MTA's host name,
	// then a literal "unknown".
	candidates = append(candidates, b.RecipientDomain, b.RemoteMTA, unitUnknown)
	for _, c := range candidates {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			return c
		}
	}
	return unitUnknown
}

// unitUnknown is the unit_value written when nothing at all could be
// extracted for the unit of a group.
const unitUnknown = "unknown"

// authority resolves the authority of a category (lower-cased).
func (f *categoryFacts) authority(kind authorityKind) string {
	b := f.bounce
	switch kind {
	case authorityBlacklist:
		if f.blacklist != "" {
			return f.blacklist
		}
		if a := listingAuthority(f.text, b.RecipientDomain, f.senderDomain, b.ReportingMTA, b.RemoteMTA); a != "" {
			return a
		}
		return strings.ToLower(b.RecipientDomain)
	case authorityRecipientDomain:
		return strings.ToLower(b.RecipientDomain)
	case authorityStatusTemplate:
		return strings.TrimSpace(strings.ToLower(f.status + " " + b.DiagnosticTemplate))
	}
	return ""
}

// statusIn reports whether status is one of the given codes.
func statusIn(status string, codes ...string) bool {
	for _, c := range codes {
		if status == c {
			return true
		}
	}
	return false
}

// statusClass reports whether status starts with one of the given prefixes
// ("5.5." matches every 5.5.x).
func statusClass(status string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(status, p) {
			return true
		}
	}
	return false
}

// --- extractors ---

const (
	ipv4Pattern = `(?:[0-9]{1,3}\.){3}[0-9]{1,3}`
	ipv6Pattern = `[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}`
	ipPattern   = `(?:` + ipv4Pattern + `|` + ipv6Pattern + `)`
)

var (
	// "[203.0.113.5]", "(203.0.113.5)" and Gmail's "[203.0.113.5      19]".
	bracketedSendingIPRe = regexp.MustCompile(`[\[(]\s*(` + ipPattern + `)(?:\s+[0-9]+)?\s*[\])]`)
	// "from 203.0.113.5", "your IP 203.0.113.5", "IP address: 203.0.113.5".
	keywordSendingIPRe = regexp.MustCompile(`(?:\bip\b(?: address)?|address|host|from|your|originating|client|connection|server|source|sender)\W{0,3}(?:is\s+|of\s+|at\s+)?\[?(` + ipPattern + `)`)
	anySendingIPRe     = regexp.MustCompile(ipPattern)
	// Hostnames (and the hosts of URLs) in the text; the leading group keeps
	// the character before the name so that e-mail domains can be skipped.
	textHostRe = regexp.MustCompile(`(^|[^a-z0-9@.\-])((?:[a-z0-9](?:[a-z0-9\-]{0,62}[a-z0-9])?\.){1,}[a-z][a-z0-9\-]{1,62})(?:[^a-z0-9.\-@]|$)`)
	// URLs are reduced to their host before the hostname scan so that path
	// segments ("troubleshooting.aspx") are never taken for hostnames.
	textURLRe  = regexp.MustCompile(`[a-z]+://([^\s/;,)\]>'"]+)[^\s;,)\]>'"]*`)
	textAddrRe = regexp.MustCompile(`[a-z0-9._%+\-=/!#$&'*?^{}|~]+@[a-z0-9.\-]+\.[a-z0-9\-]+`)
)

// extractSendingIP finds our sending IP in a diagnostic: a bracketed IP
// first, then an IP next to words such as "from" or "your IP", then any IP
// literal. The remote MTA's own IP is never taken. "" when none.
func extractSendingIP(text, remoteIP string) string {
	var candidates []string
	for _, m := range bracketedSendingIPRe.FindAllStringSubmatch(text, -1) {
		candidates = append(candidates, m[1])
	}
	for _, m := range keywordSendingIPRe.FindAllStringSubmatch(text, -1) {
		candidates = append(candidates, m[1])
	}
	candidates = append(candidates, anySendingIPRe.FindAllString(text, -1)...)
	for _, c := range candidates {
		addr, err := netip.ParseAddr(c)
		if err != nil || addr.IsLoopback() || addr.IsUnspecified() {
			continue
		}
		ip := addr.String()
		if ip == remoteIP {
			continue
		}
		return ip
	}
	return ""
}

// knownBlacklist returns the authority of the first well-known provider named
// in the text ("" when none).
func knownBlacklist(text string) string {
	for _, p := range blacklistProviders {
		if p.pattern.MatchString(text) {
			return p.authority
		}
	}
	return ""
}

// listingAuthority returns the registrable domain of a hostname or URL in a
// text that speaks of a listing, skipping the recipient's, the sender's and
// the MTAs' domains and the help pages of large mail services.
func listingAuthority(text string, skipHosts ...string) string {
	if !listingSignalRe.MatchString(text) {
		return ""
	}
	skip := map[string]bool{}
	for _, h := range skipHosts {
		if d := registrableDomain(strings.ToLower(strings.TrimSpace(h))); d != "" {
			skip[d] = true
		}
	}
	text = textURLRe.ReplaceAllString(text, " $1 ")
	for _, m := range textHostRe.FindAllStringSubmatch(text, -1) {
		d := registrableDomain(m[2])
		if d == "" || skip[d] || notAuthorityDomains[d] || fileExtensions[d[strings.LastIndexByte(d, '.')+1:]] {
			continue
		}
		return d
	}
	return ""
}

// fileExtensions are "TLDs" that only occur as file names in bare URLs
// ("mail/troubleshooting.aspx"); such matches are not hostnames.
var fileExtensions = map[string]bool{
	"aspx": true, "asp": true, "html": true, "htm": true, "shtml": true, "php": true, "jsp": true, "cgi": true,
	"txt": true, "pdf": true, "cfm": true, "do": true, "action": true,
}

// publicSecondLevel lists the second-level labels under two-letter country
// TLDs that are public suffixes ("co.jp", "co.uk", "com.au"): the registrable
// domain then keeps three labels.
var publicSecondLevel = map[string]bool{
	"co": true, "ne": true, "or": true, "ac": true, "go": true, "ad": true, "ed": true, "gr": true, "lg": true,
	"com": true, "net": true, "org": true, "gov": true, "edu": true, "mil": true,
}

// registrableDomain reduces a hostname to its registrable domain
// ("www.spamhaus.org" -> "spamhaus.org", "mx.example.co.jp" -> "example.co.jp").
func registrableDomain(host string) string {
	host = strings.Trim(strings.ToLower(host), ".")
	if host == "" {
		return ""
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	n := 2
	if len(labels) >= 3 && len(labels[len(labels)-1]) == 2 && publicSecondLevel[labels[len(labels)-2]] {
		n = 3
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

// addressIn returns the first e-mail address in a header value such as
// "Name <user@example.jp>" ("" when none).
func addressIn(v string) string {
	return textAddrRe.FindString(strings.ToLower(v))
}

// otherAddressIn returns the first address in the text that is not the
// failed recipient (the sender quoted by "<sender>: Sender address rejected").
func otherAddressIn(text, recipient string) string {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	for _, a := range textAddrRe.FindAllString(text, -1) {
		if a != recipient {
			return a
		}
	}
	return ""
}
