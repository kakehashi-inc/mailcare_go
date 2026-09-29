package mailengine

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/publicsuffix"
)

// Junk mail (bounce kind "junk"): phishing and spam sent to the monitored
// address. Junk is neither a notice nor ordinary mail: it is not grouped
// (nothing to act on) but deleted from the IMAP server after the server
// retention like the notices (design document "mail classification").
// Every signal below is enough on its own; the rules run after the rules
// with certain evidence of a notice, so a notice recognized with certainty
// never reaches them (a notice that only the weaker rules would recognize
// can). A misjudged mail only leaves the server after the retention and
// stays in the index until the mail retention.

// mailboxIdentity is what the junk rules know about the monitored address:
// the address, its organizational domain and the name part of that domain
// (the label before the public suffix: "example" of "example.co.jp").
type mailboxIdentity struct {
	address string
	domain  string
	name    string
}

// minMailboxNameLen is the shortest domain name part the display-name rule
// looks for; a shorter name ("ab") would match unrelated words.
const minMailboxNameLen = 4

func newMailboxIdentity(address string) mailboxIdentity {
	address = strings.ToLower(strings.TrimSpace(address))
	id := mailboxIdentity{address: address}
	if i := strings.LastIndexByte(address, '@'); i >= 0 {
		id.domain = organizationalDomain(address[i+1:])
	}
	if id.domain != "" {
		if suffix, _ := publicsuffix.PublicSuffix(id.domain); suffix != "" && suffix != id.domain {
			id.name = strings.TrimSuffix(id.domain, "."+suffix)
		}
	}
	if len(id.name) < minMailboxNameLen {
		id.name = ""
	}
	return id
}

// Rule names of the junk rules (messages.rule).
const (
	rulePhishingDisplayName = "phishing_display_name"
	rulePhishingForgedFrom  = "phishing_forged_from"
	rulePhishingLink        = "phishing_link"
	ruleSpamFlag            = "spam_flag"
)

// junkRules lists the junk rules in evaluation order (the server retention
// deletes what they match).
var junkRules = []string{rulePhishingDisplayName, rulePhishingForgedFrom, rulePhishingLink, ruleSpamFlag}

var (
	// displayAddressRe finds a mail address inside a display name.
	displayAddressRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@([a-z0-9](?:[a-z0-9\-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9\-]*[a-z0-9])?)+)`)
	// bodyURLRe finds http(s) URLs in a body section (text or raw HTML).
	bodyURLRe = regexp.MustCompile(`(?i)https?://[^\s"'<>()]+`)
	// dmarcFailRe matches the DMARC verdict "fail" in Authentication-Results.
	dmarcFailRe = regexp.MustCompile(`(?i)\bdmarc\s*=\s*fail\b`)
)

// fromDomain returns the organizational domain of the From address ("" when
// it has none).
func fromDomain(pm *ParsedMessage) string {
	if i := strings.LastIndexByte(pm.FromAddress, '@'); i >= 0 {
		return organizationalDomain(pm.FromAddress[i+1:])
	}
	return ""
}

// isICANNDomain reports whether name ends in a public suffix of the ICANN
// section (so "v1.2" or "file.txt" is not taken for a domain name).
func isICANNDomain(name string) bool {
	name = strings.ToLower(name)
	suffix, icann := publicsuffix.PublicSuffix(name)
	return icann && suffix != name
}

// phishingDisplayName is P1, the forged sender name: the display name of
// From names the monitored domain (or its name part) while the From address
// is of another organization, or it carries a mail address of an
// organization other than the From address's. Bare domain names in the
// display name are not looked at: company names such as "Acme Co.Ltd" look
// like domains.
func phishingDisplayName(pm *ParsedMessage, mb mailboxIdentity) bool {
	name := strings.ToLower(pm.FromName)
	from := fromDomain(pm)
	if name == "" || from == "" {
		return false
	}
	if mb.domain != "" && from != mb.domain {
		if containsWord(name, mb.domain) || (mb.name != "" && containsWord(name, mb.name)) {
			return true
		}
	}
	for _, m := range displayAddressRe.FindAllStringSubmatch(name, -1) {
		if org := organizationalDomain(m[1]); isICANNDomain(m[1]) && org != from {
			return true
		}
	}
	return false
}

// containsWord reports whether word appears in s on its own: not preceded
// or followed by a letter or a digit ("example" matches "example-support"
// and "example.jp mail", not "examples" or "counterexample"). Both are
// lower case.
func containsWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		if !isWordRune(lastRune(s[:start])) && !isWordRune(firstRune(s[end:])) {
			return true
		}
		i = start + 1
	}
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	if r == utf8.RuneError {
		return 0
	}
	return r
}

// phishingForgedFrom is P2, the forged From: the From address is of the
// monitored domain (a subdomain included) but the receiving server's
// verdict (the topmost Authentication-Results, added last) says DMARC
// failed.
func phishingForgedFrom(pm *ParsedMessage, mb mailboxIdentity) bool {
	if mb.domain == "" || fromDomain(pm) != mb.domain {
		return false
	}
	results := pm.Headers["Authentication-Results"]
	if results == "" {
		return false
	}
	topmost, _, _ := strings.Cut(results, "\n")
	return dmarcFailRe.MatchString(topmost)
}

// phishingLink is P3, a link that carries the monitored address: a URL in
// the body whose text (decoded or not) contains the address and whose host
// belongs neither to the monitored domain nor to the From address's
// organization (the typical fake login page that fills in the victim's
// address).
func phishingLink(pm *ParsedMessage, mb mailboxIdentity) bool {
	if mb.address == "" {
		return false
	}
	from := fromDomain(pm)
	for _, section := range append(append([]string{}, pm.TextSections...), pm.HTMLSections...) {
		for _, raw := range bodyURLRe.FindAllString(section, -1) {
			raw = strings.ReplaceAll(raw, "&amp;", "&")
			text := strings.ToLower(raw)
			if decoded, err := url.QueryUnescape(text); err == nil {
				text = decoded
			}
			if !strings.Contains(text, mb.address) {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() == "" {
				continue
			}
			host := organizationalDomain(u.Hostname())
			if host != mb.domain && host != from {
				return true
			}
		}
	}
	return false
}

// spamFlagged reports whether the mail carries a spam verdict header
// (X-Spam: yes, X-Spam-Flag: YES). The header may have been added by any
// server on the way, so it is no proof; it is used because a misjudged mail
// stays available until the retentions.
func spamFlagged(pm *ParsedMessage) bool {
	for _, key := range []string{"X-Spam", "X-Spam-Flag"} {
		for _, v := range strings.Split(pm.Headers[key], "\n") {
			v = strings.ToLower(strings.TrimSpace(v))
			if strings.HasPrefix(v, "yes") || strings.HasPrefix(v, "true") {
				return true
			}
		}
	}
	return false
}
