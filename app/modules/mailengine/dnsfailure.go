package mailengine

import (
	"regexp"
	"strings"
)

// DNSFailure is a failed DNS lookup reported by a notice (design document
// "mail classification" 4.4): the name the reporting MTA looked up (""
// when the form names none: the recipient domain is meant), the record type
// it names (information only: the verification does not depend on it) and
// whether the MTA treated the failure as temporary.
type DNSFailure struct {
	Name      string // lower-cased, without the trailing dot; "" when the form names none
	QType     string // the record type the notice names (upper case), "" when it names none
	Temporary bool   // "try again", "lookup did not complete" and the like; false for a definite answer
}

// DNSOutcome is the result of looking a reported name up again (4.4),
// whatever record type the notice names: can mail for the name be
// delivered somewhere (the addresses of its MX targets, or of the name
// itself)? The empty value means the name was not verified (checking
// disabled, outside the conditions, or the answers cut short).
type DNSOutcome string

const (
	DNSOutcomeExists   DNSOutcome = "exists"    // every answering resolver obtained an address to deliver to
	DNSOutcomeNotFound DNSOutcome = "not_found" // every answering resolver returned NXDOMAIN
	DNSOutcomeBroken   DNSOutcome = "broken"    // the name exists without an address to deliver to, every resolver failed, or the answers disagree
)

// verified reports whether the outcome is one of the verification results;
// the empty value and any unknown value count as not verified.
func (o DNSOutcome) verified() bool {
	switch o {
	case DNSOutcomeExists, DNSOutcomeNotFound, DNSOutcomeBroken:
		return true
	}
	return false
}

// DNSVerdict is what the caller learned about the DNS failure of a notice
// before categorizing it (CategorizeWith): the outcome of the verification
// and whether the notice came from one of the sending sources. The zero
// value means not verified.
type DNSVerdict struct {
	Outcome           DNSOutcome
	FromSendingSource bool // the reporting MTA is one of the sending sources (the grouping decides it for exists only)
}

var (
	// Postfix (src/dns/dns_lookup.c and dns_strerror.c): "Name service
	// error for name=example.jp type=MX: Host not found, try again"; the
	// reason is the text of the resolver error (Host not found, Host not
	// found, try again, Non-recoverable error, Host found but no data record
	// of requested type) or "Malformed or unexpected name server reply".
	postfixDNSErrorRe = regexp.MustCompile(`(?i)name service error for name=(\S+)\s+type=([a-z0-9]+)\s*:[ \t]*([^\n]*)`)
	// Postfix: "Domain example.jp does not accept mail (nullMX)".
	postfixNullMXRe = regexp.MustCompile(`(?i)domain (\S+) does not accept mail \(nullmx\)`)
	// Sendmail (src/err.c): "Name server: example.jp.: host not found" and
	// the other resolver errors, inside "Host unknown (...)" or after
	// "Deferred: ".
	sendmailNameServerRe = regexp.MustCompile(`(?i)name server:\s*(\S+?)\s*:\s*(host not found|host name lookup failure|non-recoverable error|no data known)`)
	// qmail: "Sorry, I couldn't find any host named example.jp. (#5.1.2)".
	qmailHostNamedRe = regexp.MustCompile(`(?i)sorry, i couldn't find any host named (\S+?)\.? \(#5\.1\.2\)`)
	// Exchange / Microsoft 365: "550 5.4.310 DNS domain example.jp does not
	// exist [Message=InfoDomainNonexistent]".
	exchangeDomainRe = regexp.MustCompile(`(?i)dns domain (\S+) does not exist`)
	// Gmail: "DNS Error: 12345 DNS type 'mx' lookup of example.jp responded
	// with code NXDOMAIN" (also SERVFAIL and the other codes).
	gmailTypedRe = regexp.MustCompile(`(?i)dns type '([a-z0-9]+)' lookup of (\S+) responded with code ([a-z]+)`)

	// deliveryQTypes are the record types an MTA looks up to deliver mail
	// (MX, then A and AAAA of the targets or of the name itself). A failed
	// lookup of another type (e.g. the TXT record an SPF check of the
	// sender reads, quoted in a rejection) concerns no delivery.
	deliveryQTypes = map[string]bool{"MX": true, "A": true, "AAAA": true}

	// foldedLineRe joins a header line folded inside a diagnostic.
	foldedLineRe = regexp.MustCompile(`\r?\n[ \t]+`)

	// Reasons of the Postfix form: the temporary ones ("try again", and
	// the wordings of other resolver libraries), and the definite ones.
	dnsTemporaryReasonRe = regexp.MustCompile(`try again|server failure|temporary failure|timed out|malformed or unexpected name server reply`)
	dnsDefiniteReasonRe  = regexp.MustCompile(`host not found|host found but no data record|non-recoverable error`)
)

// dnsUnnamedForms are the forms that name no name (the recipient domain is
// meant), with whether each is temporary, in the order they are looked for:
// Exim (src/routers/dnslookup.c), qmail, Exchange / Microsoft 365, Gmail,
// and the null MX reply of Sendmail. A form marked notRemote counts only
// when the diagnostic is not the reply of a remote server (remoteReply):
// "DNS query failed" in the reply of another server is about the lookups of
// that server. Exim's "Unrouteable address" is not among them: Exim also gives
// it for an unknown local user, so it tells nothing about DNS.
var dnsUnnamedForms = []struct {
	re        *regexp.Regexp
	temporary bool
	notRemote bool
}{
	{regexp.MustCompile(`(?i)all relevant mx records point to non-existent hosts`), false, false},
	{regexp.MustCompile(`(?i)an mx or srv record indicated no smtp service`), false, false},
	{regexp.MustCompile(`(?i)host lookup did not complete`), true, false},
	{regexp.MustCompile(`(?i)missing mx, or all mxs point to missing a records`), true, false},
	{regexp.MustCompile(`(?i)sorry, i couldn't find any host by that name\.? \(#4\.1\.2\)`), true, false},
	{regexp.MustCompile(`(?i)cname lookup failed temporarily\.? \(#4\.4\.3\)`), true, false},
	{regexp.MustCompile(`(?i)smtpsend\.dns\.nonexistentdomain`), false, false},
	{regexp.MustCompile(`(?i)dns query failed`), true, true},
	{regexp.MustCompile(`(?i)dns error: domain name not found`), false, false},
	{regexp.MustCompile(`(?i)dns error: dns server returned answer with no data`), false, false},
	{regexp.MustCompile(`(?i)dns error: could not contact dns servers`), true, false},
	{regexp.MustCompile(`(?i)host does not accept mail: mx 0 \.`), false, false},
}

// ParseDNSFailure detects a failed DNS lookup in a diagnostic (design
// document "mail classification" 4.4) without using the network. The forms
// that name the name are looked for first: Postfix ("Name service error for
// name=<name> type=<type>: <reason>" and the null MX "Domain <name> does not
// accept mail (nullMX)"), Sendmail ("Name server: <name>: host not found"
// and the other resolver errors), qmail ("Sorry, I couldn't find any host
// named <name>. (#5.1.2)"), Exchange ("DNS domain <name> does not exist")
// and Gmail ("DNS type '<type>' lookup of <name> responded with code
// <code>"); then the forms without a name (dnsUnnamedForms), which mean the
// recipient domain (Name ""). A form with a record type counts only for the
// types of a delivery (deliveryQTypes). A name that is not a valid host name
// and a Postfix reason that is no lookup failure are not detected. The
// first detected failure wins. remoteMTA is the remote MTA the notice names
// for the recipient ("" when none): with one, the diagnostic is the reply of
// that server.
func ParseDNSFailure(diagnostic, remoteMTA string) (DNSFailure, bool) {
	text := foldedLineRe.ReplaceAllString(diagnostic, " ")
	for _, m := range postfixDNSErrorRe.FindAllStringSubmatch(text, -1) {
		name, ok := normalizeDNSName(m[1])
		if !ok {
			continue
		}
		qtype := strings.ToUpper(m[2])
		if !deliveryQTypes[qtype] {
			continue
		}
		reason := strings.ToLower(dnsReason(m[3]))
		switch {
		case dnsTemporaryReasonRe.MatchString(reason):
			return DNSFailure{Name: name, QType: qtype, Temporary: true}, true
		case dnsDefiniteReasonRe.MatchString(reason):
			return DNSFailure{Name: name, QType: qtype}, true
		}
	}
	for _, m := range postfixNullMXRe.FindAllStringSubmatch(text, -1) {
		if name, ok := normalizeDNSName(m[1]); ok {
			return DNSFailure{Name: name, QType: "MX"}, true
		}
	}
	for _, m := range sendmailNameServerRe.FindAllStringSubmatch(text, -1) {
		if name, ok := normalizeDNSName(m[1]); ok {
			return DNSFailure{Name: name, Temporary: strings.EqualFold(m[2], "host name lookup failure")}, true
		}
	}
	for _, re := range []*regexp.Regexp{qmailHostNamedRe, exchangeDomainRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if name, ok := normalizeDNSName(m[1]); ok {
				return DNSFailure{Name: name}, true
			}
		}
	}
	for _, m := range gmailTypedRe.FindAllStringSubmatch(text, -1) {
		if name, ok := normalizeDNSName(m[2]); ok && deliveryQTypes[strings.ToUpper(m[1])] {
			code := strings.ToUpper(m[3])
			return DNSFailure{Name: name, QType: strings.ToUpper(m[1]), Temporary: code != "NXDOMAIN" && code != "NOERROR"}, true
		}
	}
	remote := remoteReply(text, remoteMTA)
	for _, form := range dnsUnnamedForms {
		if form.notRemote && remote {
			continue
		}
		if form.re.MatchString(text) {
			return DNSFailure{Temporary: form.temporary}, true
		}
	}
	return DNSFailure{}, false
}

// remoteReplyRe finds the wording that quotes the reply of a remote server:
// Postfix "... said: ...", Exim "SMTP error from remote mail server".
var remoteReplyRe = regexp.MustCompile(`(?i)\bsaid:|smtp error from remote`)

// remoteReply reports whether a diagnostic is the reply of a remote server:
// the notice names the remote MTA, or the wording quotes its reply.
func remoteReply(text, remoteMTA string) bool {
	return strings.TrimSpace(remoteMTA) != "" || remoteReplyRe.MatchString(text)
}

// dnsReason cuts the reason of the Postfix form at the end of its phrase, so
// that wording of a following sentence is not read as part of it.
func dnsReason(s string) string {
	if i := strings.IndexAny(s, ".;()<>[]"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// normalizeDNSName lower-cases a name quoted by a notice, removes its
// trailing dot and validates it as a fully qualified host name: at least two
// labels of letters, digits and hyphens (1-63 characters, no hyphen at
// either end), at most 253 characters, and a top-level label that is not
// all digits (an IP literal is no name).
func normalizeDNSName(s string) (string, bool) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if name == "" || len(name) > 253 {
		return "", false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", false
			}
		}
	}
	if !strings.ContainsAny(labels[len(labels)-1], "abcdefghijklmnopqrstuvwxyz") {
		return "", false
	}
	return name, true
}
