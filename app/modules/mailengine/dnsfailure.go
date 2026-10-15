package mailengine

import (
	"regexp"
	"strings"
)

// DNSFailure is a failed DNS lookup reported by a notice (design document
// "mail classification" 4.4): the name the reporting MTA looked up, the
// record type and whether the MTA treated the failure as temporary.
type DNSFailure struct {
	Name      string // lower-cased, without the trailing dot
	QType     string // MX / A / AAAA; "" for the Sendmail forms, which name no type
	Temporary bool   // "try again" and the like; false for a plain "host not found"
}

// DNSOutcome is the result of looking a reported name up again (4.4). The
// empty value means the name was not verified (checking disabled, outside
// the conditions, or no resolver answered).
type DNSOutcome string

const (
	DNSOutcomeExists   DNSOutcome = "exists"    // every answering resolver returned an answer
	DNSOutcomeNotFound DNSOutcome = "not_found" // every answering resolver returned NXDOMAIN
	DNSOutcomeBroken   DNSOutcome = "broken"    // some answered, others failed or disagreed
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
// and whether the notice came from one of our own sending servers. The zero
// value means not verified.
type DNSVerdict struct {
	Outcome DNSOutcome
	OwnMTA  bool // the reporting MTA is one of our own sending servers (the grouping decides it for exists only)
}

var (
	// Postfix: "Name service error for name=example.jp type=MX: Host not found, try again".
	postfixDNSErrorRe = regexp.MustCompile(`(?i)name service error for name=(\S+)\s+type=([a-z0-9]+)\s*:[ \t]*([^\n]*)`)
	// Sendmail: "Host unknown (Name server: example.jp.: host not found)".
	sendmailHostUnknownRe = regexp.MustCompile(`(?i)host unknown\s*\(\s*name server:\s*(\S+?)\s*:\s*host not found\s*\)`)
	// Sendmail: "Deferred: Name server: example.jp.: host name lookup failure".
	sendmailLookupFailureRe = regexp.MustCompile(`(?i)name server:\s*(\S+?)\s*:\s*host name lookup failure`)

	// foldedLineRe joins a header line folded inside a diagnostic.
	foldedLineRe = regexp.MustCompile(`\r?\n[ \t]+`)

	// Reasons of the Postfix form. "Host found but no data record of
	// requested type" (the domain exists without that record) is no lookup
	// failure; it matches neither expression and is not detected.
	dnsTemporaryReasonRe = regexp.MustCompile(`try again|server failure|temporary failure|timed out`)
	dnsHostNotFoundRe    = regexp.MustCompile(`host not found`)
)

// dnsQTypes are the record types a DNS failure is detected for (the values
// of dns_checks.qtype).
var dnsQTypes = map[string]bool{"MX": true, "A": true, "AAAA": true}

// ParseDNSFailure detects a failed DNS lookup in a diagnostic (design
// document "mail classification" 4.4) without using the network: the
// Postfix "Name service error for name=<name> type=<type>: <reason>" form
// and the Sendmail "Host unknown (Name server: <name>: host not found)" and
// "Name server: <name>: host name lookup failure" forms. A form whose name is
// not a valid host name, a Postfix type other than MX / A / AAAA and a
// Postfix reason that is no lookup failure are not detected. The Postfix
// form is looked for first, then the Sendmail forms; the first detected
// failure wins.
func ParseDNSFailure(diagnostic string) (DNSFailure, bool) {
	text := foldedLineRe.ReplaceAllString(diagnostic, " ")
	for _, m := range postfixDNSErrorRe.FindAllStringSubmatch(text, -1) {
		name, ok := normalizeDNSName(m[1])
		qtype := strings.ToUpper(m[2])
		if !ok || !dnsQTypes[qtype] {
			continue
		}
		reason := strings.ToLower(dnsReason(m[3]))
		switch {
		case dnsTemporaryReasonRe.MatchString(reason):
			return DNSFailure{Name: name, QType: qtype, Temporary: true}, true
		case dnsHostNotFoundRe.MatchString(reason):
			return DNSFailure{Name: name, QType: qtype}, true
		}
	}
	for _, form := range []struct {
		re        *regexp.Regexp
		temporary bool
	}{
		{sendmailHostUnknownRe, false},
		{sendmailLookupFailureRe, true},
	} {
		for _, m := range form.re.FindAllStringSubmatch(text, -1) {
			if name, ok := normalizeDNSName(m[1]); ok {
				return DNSFailure{Name: name, Temporary: form.temporary}, true
			}
		}
	}
	return DNSFailure{}, false
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
