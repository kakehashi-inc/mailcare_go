package mailengine

import (
	"html"
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
// Every signal below is enough by itself; the rules run after the rules
// with certain evidence of a notice, so a notice recognized with certainty
// never reaches them (a notice that only the weaker rules would recognize
// can). The fake notices (F1, F2) run right after the structured reports
// instead: they present themselves as a daemon's mail, which the daemon
// sender rule would take for a bounce, and they are told apart by the
// structure a genuine report carries. The return notice of a message never
// sent from the sending sources (backscatter, classify.go) runs before
// everything: the notice is
// genuine and carries that structure, so it would be taken for a bounce
// first. A misjudged mail only leaves the server after the retention and
// stays in the index until the mail retention.

// mailboxIdentity is what the junk rules know about the monitored address:
// the address, its organizational domain, the name part of that domain (the
// label before the public suffix: "example" of "example.co.jp") and the
// sending sources (the backscatter rule; the zero value is the empty
// configuration, under which that rule does not apply).
type mailboxIdentity struct {
	address string
	domain  string
	name    string
	sources SendingSources
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
	ruleBackscatter         = "backscatter"
	ruleFakeNoticeLink      = "fake_notice_link"
	ruleFakeNoticeAuth      = "fake_notice_auth"
	rulePhishingDisplayName = "phishing_display_name"
	rulePhishingForgedFrom  = "phishing_forged_from"
	rulePhishingLink        = "phishing_link"
	ruleSpamFlag            = "spam_flag"
)

// junkRules lists the junk rules in evaluation order (the server retention
// deletes what they match).
var junkRules = []string{ruleBackscatter, ruleFakeNoticeLink, ruleFakeNoticeAuth, rulePhishingDisplayName, rulePhishingForgedFrom, rulePhishingLink, ruleSpamFlag}

var (
	// displayAddressRe finds a mail address inside a display name.
	displayAddressRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@([a-z0-9](?:[a-z0-9\-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9\-]*[a-z0-9])?)+)`)
	// bodyURLRe finds http(s) URLs in a body section (text or raw HTML).
	bodyURLRe = regexp.MustCompile(`(?i)https?://[^\s"'<>()]+`)
	// htmlLinkRe finds the href of an <a> or <area> tag: the attribute on
	// alone (not "data-href"), its value in double or single quotes or
	// unquoted (the groups 1, 2 and 3).
	htmlLinkRe = regexp.MustCompile(`(?i)<(?:a|area)\s(?:[^>]*?\s)?href\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	// smtpResponseLineRe matches a line of a text report that quotes the
	// response of a remote server ("Remote host said: 554 5.7.1 ...",
	// "SMTP error from remote mail server ...", "<<< 550-5.7.1 ..."): the
	// words that introduce a response, an SMTP reply code with an enhanced
	// status code, an enhanced status code alone, or a reply code at
	// the start of the line (after the "<<<" of a Sendmail transcript).
	smtpResponseLineRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`said:`,
		`smtp error from remote`,
		`remote host said`,
		`response:`,
		`reply:`,
		`\b[45]\d\d[ -]\d\.\d{1,3}\.\d{1,3}\b`,
		`\b[245]\.\d{1,3}\.\d{1,3}\b`,
		`^\s*(?:<<<\s*)?[45]\d\d[ -]`,
	}, "|"))
	// dmarcFailRe matches the DMARC verdict "fail" in Authentication-Results.
	dmarcFailRe = regexp.MustCompile(`(?i)\bdmarc\s*=\s*fail\b`)

	// genuineNoticeBodyRe is the wording that spares a report from F2: the
	// wording of rule 6 (bounceBodyRe) and the fixed sentences of genuine
	// reports that rule 6 does not list. They are kept out of rule 6 itself,
	// which makes a mail a target the server retention deletes. Like rule 6
	// it is strict: whole sentences of real reports, never a single word.
	genuineNoticeBodyRe = regexp.MustCompile(`(?i)(?:` + bounceBodyRe.String() + `)|` + strings.Join([]string{
		// The fixed preamble of qmail-send (its reports carry neither a
		// delivery-status part nor an attached original).
		`this is the qmail-send program at`,
		`i'm afraid i wasn't able to deliver your message to the following addresses`,
		// Japanese reports state the failure about the recipients:
		// "(ika no) atesaki ni (messeeji wo) haishin dekimasen deshita" (could
		// not deliver (the message) to the (following) address; soushin,
		// send, as well) and "(tsugi no) jushinsha mataha guruupu e no haishin
		// ni shippai shimashita" (delivery to the (following) recipients or
		// groups failed, Exchange). Bare words such as haishin funou
		// (undeliverable), futatsu (not delivered) or haishin ni shippai
		// (delivery failed) are left out: fake notices use them as their lure
		// ("3 undeliverable messages").
		"(?:\u5b9b\u5148|\u53d7\u4fe1\u8005|\u30a2\u30c9\u30ec\u30b9)[\u306b\u3078]" + // atesaki / jushinsha / adoresu + ni / e
			"(?:\u30e1\u30c3\u30bb\u30fc\u30b8\u3092|\u30e1\u30fc\u30eb\u3092)?" + // messeeji wo / meeru wo
			"(?:\u914d\u4fe1|\u9001\u4fe1)\u3067\u304d\u307e\u305b\u3093\u3067\u3057\u305f", // haishin / soushin dekimasen deshita
		"(?:\u5b9b\u5148|\u53d7\u4fe1\u8005|\u30b0\u30eb\u30fc\u30d7)\u3078\u306e" + // atesaki / jushinsha / guruupu + e no
			"(?:\u30e1\u30c3\u30bb\u30fc\u30b8\u306e)?" + // messeeji no
			"\u914d\u4fe1\u306b\u5931\u6557\u3057\u307e\u3057\u305f", // haishin ni shippai shimashita
	}, "|"))

	// inlineCopyMarkerRe finds the line after which a text report quotes the
	// returned message inline; the links below it are the original
	// message's, not the report's.
	inlineCopyMarkerRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`below this line is a copy of the message`,                 // qmail
		`this is a copy of the message, including all the headers`, // Exim
		`original message follows`,                                 // Sendmail and others
		`-----\s*original message\s*-----`,                         // Outlook
	}, "|"))

	// replyForwardSubjectRe matches the prefix a mail client puts before the
	// subject of a reply or a forward: "Re:", "Fw:", "Fwd:" in any case (a
	// counter such as "Re[2]:" too), the Japanese henshin (reply) and tensou
	// (forward) followed by a colon or in brackets, with an ASCII or a
	// full-width colon. Repeated prefixes start with one of them.
	replyForwardSubjectRe = regexp.MustCompile(`(?i)^\s*(?:` + strings.Join([]string{
		`(?:re|fwd?|` + "\u8fd4\u4fe1|\u8ee2\u9001" + `)\s*(?:\[\d+\])?\s*[:` + "\uff1a" + `]`,  // re / fw / fwd / henshin / tensou + colon
		`[\[` + "\u3010" + `]\s*(?:` + "\u8fd4\u4fe1|\u8ee2\u9001" + `)\s*[\]` + "\u3011" + `]`, // [henshin] / [tensou] in ASCII or lenticular brackets
	}, "|") + `)`)
)

// fromDomain returns the organizational domain of the From address ("" when
// it has none).
func fromDomain(pm *ParsedMessage) string {
	if i := strings.LastIndexByte(pm.FromAddress, '@'); i >= 0 {
		return organizationalDomain(pm.FromAddress[i+1:])
	}
	return ""
}

// returnPathDomain returns the organizational domain of the Return-Path
// address ("" for the null sender <> or an address without a domain).
func returnPathDomain(pm *ParsedMessage) string {
	if i := strings.LastIndexByte(pm.ReturnPath, '@'); i >= 0 {
		return organizationalDomain(pm.ReturnPath[i+1:])
	}
	return ""
}

// topmostDMARCFailed reports whether the receiving server's verdict (the
// topmost Authentication-Results, added last) says DMARC failed.
func topmostDMARCFailed(pm *ParsedMessage) bool {
	results := pm.Headers["Authentication-Results"]
	if results == "" {
		return false
	}
	topmost, _, _ := strings.Cut(results, "\n")
	return dmarcFailRe.MatchString(topmost)
}

// bodyLink is an http(s) URL of a body section and the organizational
// domain of its host.
type bodyLink struct {
	url    string // as written, the HTML entity "&amp;" restored
	domain string
}

// bodySections returns every body section: the text sections, then the raw
// HTML ones.
func bodySections(pm *ParsedMessage) []string {
	return append(append([]string{}, pm.TextSections...), pm.HTMLSections...)
}

// bodyLinks returns the http(s) URLs of the sections in order, wherever
// they stand (the text of P3; F1 counts fewer, see noticeLinks). A URL whose
// host cannot be read is left out.
func bodyLinks(sections []string) []bodyLink {
	var links []bodyLink
	for _, section := range sections {
		for _, raw := range bodyURLRe.FindAllString(section, -1) {
			raw = strings.ReplaceAll(raw, "&amp;", "&")
			u, err := url.Parse(raw)
			if err != nil || u.Hostname() == "" {
				continue
			}
			if domain := organizationalDomain(u.Hostname()); domain != "" {
				links = append(links, bodyLink{url: raw, domain: domain})
			}
		}
	}
	return links
}

// The fake notices (F1, F2): phishing that presents itself as a
// non-delivery report. Nearly every genuine report carries a delivery-status
// part or the returned original message; the fake ones carry neither.
//
// Classify runs the rule table twice (the primary body, then the HTML text).
// The fake-notice rules do not read the body of the pass: they look at the
// headers, the structure, every body section and both bodies, so both passes
// give the same answer. In particular F2 never takes a genuine report whose
// report wording is only in its HTML part for junk on the first pass.

// fakeNoticeCandidate is what F1 and F2 share: the message looks like a
// non-delivery report (looksLikeNotice), has none of a genuine report's
// structure, is not marked as machine-sent by an Auto-Submitted header other
// than "no" (an automatic reply quoting "Undeliverable" is left to the
// auto-reply rules),
// and its subject is neither a reply or forward (a person passing a report
// on, with the report's links in the quote) nor an out-of-office subject
// (the auto_reply_subject rule: an automatic reply without the header).
// Those are left to the later rules. The subject exclusions do not apply to
// a message from a daemon address, which is no person's forward or reply.
// A forger who is not using a daemon address and adds such a prefix evades F1
// and F2; that is accepted.
func fakeNoticeCandidate(pm *ParsedMessage) bool {
	if pm.AutoSubmitted != "" && !strings.HasPrefix(pm.AutoSubmitted, "no") {
		return false
	}
	if personalSubject(pm) {
		return false
	}
	return !hasNoticeStructure(pm) && looksLikeNotice(pm)
}

// personalSubject reports whether the subject marks a mail a person passes
// on or an automatic reply: a reply or forward prefix, or an out-of-office
// subject (the auto_reply_subject rule). The fake-notice rules and the
// backscatter rule leave such mail to the later rules. The exclusions are
// for mail that people write: a message from a daemon address (From or
// Return-Path) is not a person's forward or out-of-office reply, so for it
// the subject never counts and a forger gains nothing by writing "Fwd:"
// into the subject.
func personalSubject(pm *ParsedMessage) bool {
	if isDaemonAddress(pm.FromAddress) || isDaemonAddress(pm.ReturnPath) {
		return false
	}
	return replyForwardSubjectRe.MatchString(pm.Subject) || autoReplySubjectRe.MatchString(pm.Subject)
}

// hasNoticeStructure reports whether the message carries the structure of a
// genuine report: a delivery-status part (rule 1 takes those with
// recipients; a part without any still counts), a multipart/report
// container, or the returned original message (message/rfc822 or
// text/rfc822-headers).
func hasNoticeStructure(pm *ParsedMessage) bool {
	return pm.DeliveryStatus != nil || pm.ContentType == "multipart/report" || pm.OriginalMessage != nil
}

// looksLikeNotice reports whether the message presents itself as a
// non-delivery report, the test F1, F2 and the backscatter rule share: a
// daemon From or Return-Path (rule 2), the report wording (rule 6) in the
// text body or the HTML text, or a mail delivery system display name (rule
// 5) together with a bounce subject (rule 4). The display name alone or the
// subject alone does not count: an announcement of a provider's "Mail
// Administrator" or a carrier's "parcel delivery has failed" carries a
// link to the site of the sender and would be junk. A phishing mail that only borrows
// the display name (no daemon address, no bounce subject, no report wording)
// is therefore left to the later rules (P1-P3, then rule 5 keeps it as a
// notice on the server); that is accepted.
func looksLikeNotice(pm *ParsedMessage) bool {
	return isDaemonAddress(pm.FromAddress) || isDaemonAddress(pm.ReturnPath) || hasNoticeWording(pm) ||
		(daemonDisplayNameRe.MatchString(pm.FromName) && bounceSubjectRe.MatchString(pm.Subject))
}

// hasNoticeWording reports whether the text body or the HTML text carries
// the wording only a non-delivery report carries (rule 6).
func hasNoticeWording(pm *ParsedMessage) bool {
	return bounceBodyRe.MatchString(pm.textBodyJoined()) || bounceBodyRe.MatchString(pm.htmlTextBody())
}

// hasGenuineNoticeWording is hasNoticeWording with the wider wording that
// spares a report from F2 (genuineNoticeBodyRe).
func hasGenuineNoticeWording(pm *ParsedMessage) bool {
	return genuineNoticeBodyRe.MatchString(pm.textBodyJoined()) || genuineNoticeBodyRe.MatchString(pm.htmlTextBody())
}

// beforeInlineCopy returns a body section up to the first inline copy of
// the returned message (inlineCopyMarkerRe), the whole section when it
// quotes none.
func beforeInlineCopy(section string) string {
	if loc := inlineCopyMarkerRe.FindStringIndex(section); loc != nil {
		return section[:loc[0]]
	}
	return section
}

// fakeNoticeLink is F1, the fake notice with a link: a candidate (see
// fakeNoticeCandidate) with a link (noticeLinks) to an organization other
// than the From address's, the Return-Path's and the monitored domain (the
// fake "release held messages" page). Only the text above an inline copy of
// the returned message counts: a genuine text report (qmail) quotes the
// original below a marker, and the links of the original are the sender's.
// A forger who writes such a marker above the link, or who puts the link on
// a line that looks like a quoted SMTP response (see noticeLinks), evades
// F1; that is accepted (F2 still applies when the mail failed DMARC).
func fakeNoticeLink(pm *ParsedMessage, mb mailboxIdentity) bool {
	if !fakeNoticeCandidate(pm) {
		return false
	}
	from, returnPath := fromDomain(pm), returnPathDomain(pm)
	for _, link := range noticeLinks(pm) {
		if link.domain != from && link.domain != returnPath && link.domain != mb.domain {
			return true
		}
	}
	return false
}

// noticeLinks returns the links F1 counts: the ones the author of a notice
// put there for the reader to follow, above an inline copy of the returned
// message (beforeInlineCopy).
//
//   - In a text section, every http(s) URL except those on a line that
//     quotes the response of a remote server (smtpResponseLineRe): a genuine
//     report quotes the rejection of the receiving server word for word, and
//     a blocklist names its lookup page there ("blocked using
//     zen.spamhaus.org; https://www.spamhaus.org/query/ip/..."). The line is
//     judged with its URLs blanked out, so a URL never makes the line it is on
//     look like a response ("https://evil.example/5.1.1"). A forger who
//     writes the link on a line with such words or codes evades F1; that is
//     accepted.
//   - In an HTML section, only the href of the <a> and <area> tags
//     (comments, scripts, styles and the head dropped first), with its
//     character references decoded (&amp;, also &#104;ttps:). The markup
//     itself is never scanned: the namespaces, the document type, the
//     images and the style sheets of a genuine HTML report name other
//     organizations' sites ("http://www.w3.org/1999/xhtml") that nobody is
//     asked to open. A URL written as text in an HTML section does not count
//     either.
//
// Only absolute http(s) URLs and network-path references ("//host/path",
// which a mail client opens as an http(s) link) count; relative links,
// mailto: and other schemes have no other organization's host.
func noticeLinks(pm *ParsedMessage) []bodyLink {
	var links []bodyLink
	for _, section := range pm.TextSections {
		for _, line := range strings.Split(beforeInlineCopy(section), "\n") {
			if !quotesSMTPResponse(line) {
				links = append(links, bodyLinks([]string{line})...)
			}
		}
	}
	for _, section := range pm.HTMLSections {
		markup := beforeInlineCopy(htmlDropRe.ReplaceAllString(section, " "))
		for _, m := range htmlLinkRe.FindAllStringSubmatch(markup, -1) {
			if link, ok := hrefLink(m[1] + m[2] + m[3]); ok {
				links = append(links, link)
			}
		}
	}
	return links
}

// quotesSMTPResponse reports whether a line of a text section quotes the
// response of a remote server (smtpResponseLineRe), judged without its URLs.
func quotesSMTPResponse(line string) bool {
	return smtpResponseLineRe.MatchString(bodyURLRe.ReplaceAllString(line, " "))
}

// hrefLink reads the value of an href attribute as a link, as a browser
// would follow it: the character references decoded, the white space around
// it and the tabs and line breaks in it removed, and a network-path
// reference taken as https. ok is false for anything but an http(s) URL
// with a readable host.
func hrefLink(href string) (bodyLink, bool) {
	href = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, strings.TrimSpace(html.UnescapeString(href)))
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return bodyLink{}, false
	}
	domain := organizationalDomain(u.Hostname())
	if domain == "" {
		return bodyLink{}, false
	}
	return bodyLink{url: href, domain: domain}, true
}

// fakeNoticeAuth is F2, the fake notice that failed DMARC: a candidate whose
// topmost Authentication-Results says dmarc=fail, whose From is not of the
// monitored domain (the reports of the monitored server often fail DMARC)
// and whose body lacks the wording of a genuine report (genuineNoticeBodyRe:
// genuine reports of small servers without DKIM, qmail and Japanese MTAs
// among them, can fail DMARC too).
func fakeNoticeAuth(pm *ParsedMessage, mb mailboxIdentity) bool {
	if !fakeNoticeCandidate(pm) || !topmostDMARCFailed(pm) {
		return false
	}
	if mb.domain != "" && fromDomain(pm) == mb.domain {
		return false
	}
	return !hasGenuineNoticeWording(pm)
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

// containsWord reports whether word appears in s as a whole word: not preceded
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
	return topmostDMARCFailed(pm)
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
	for _, link := range bodyLinks(bodySections(pm)) {
		text := strings.ToLower(link.url)
		if decoded, err := url.QueryUnescape(text); err == nil {
			text = decoded
		}
		if strings.Contains(text, mb.address) && link.domain != mb.domain && link.domain != from {
			return true
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
