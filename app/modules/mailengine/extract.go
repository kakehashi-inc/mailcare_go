package mailengine

import (
	"regexp"
	"strings"

	"mailcare/app/models"
)

// ExtractBounce builds the bounces row of a message classified as failed or
// delayed. The delivery-status part is used first; what it does not
// provide is filled from the body text by the rule table below. exclude lists
// addresses that must not be taken as the failed recipient (the monitored
// address and the sender of the notice itself).
//
// The returned Bounce has no ID and no GroupKey; the caller (groupMessage)
// sets them from the message row and the group it files the bounce into.
func ExtractBounce(pm *ParsedMessage, kind string, exclude ...string) *models.Bounce {
	b := &models.Bounce{}
	if pm == nil {
		return b
	}
	f := fields{}
	if pm.DeliveryStatus != nil {
		f.fromDeliveryStatus(pm.DeliveryStatus, kind)
		b.ReportingMTA = pm.DeliveryStatus.ReportingMTA
	}
	// The primary body (every text section joined) first; whatever is still
	// empty is completed from the HTML sections rendered as text when the
	// message has both kinds (design 5.2).
	f.fromBody(unfold(pm.bodyForClassification()), pm.Headers, exclude)
	if secondary := pm.secondaryBody(); secondary != "" {
		f.fromBody(unfold(secondary), nil, exclude)
	}
	f.finish()

	b.Recipient = f.addr
	b.RecipientDomain = domainOf(f.addr)
	b.Action = f.action
	if b.Action == "" {
		switch kind {
		case bounceKindFailed, bounceKindDelayed:
			b.Action = kind
		}
	}
	b.StatusCode = f.status
	b.SMTPCode = f.code
	b.Diagnostic = f.diag
	b.RemoteMTA = f.host
	b.RemoteIP = f.ip
	if pm.OriginalMessage != nil {
		b.OriginalMessageID = pm.OriginalMessage.MessageID
		b.OriginalSubject = pm.OriginalMessage.Subject
		b.OriginalFrom = pm.OriginalMessage.From
		b.OriginalDate = models.NullTime(pm.OriginalMessage.Date)
	}
	templateSource := b.Diagnostic
	if templateSource == "" {
		templateSource = pm.Subject
	}
	b.DiagnosticTemplate = DiagnosticTemplate(templateSource)
	// The responsible party is a property of the group (design 5.4); the
	// bounce row does not repeat it.
	return b
}

// fields accumulates the extracted values; every rule only fills what is
// still empty.
type fields struct {
	addr, host, ip, code, status, diag, action string
}

func (f *fields) fill(name, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	switch name {
	case "addr":
		if f.addr == "" {
			f.addr = cleanAngleAddress(value)
		}
	case "host":
		if f.host == "" {
			f.host = strings.Trim(value, "[]().,;:")
		}
	case "ip":
		if f.ip == "" {
			f.ip = strings.Trim(value, "[]")
		}
	case "code":
		if f.code == "" {
			f.code = value
		}
	case "status":
		if f.status == "" {
			f.status = strings.TrimPrefix(value, "#")
		}
	case "diag":
		if f.diag == "" {
			f.diag = value
		}
	}
}

// fromDeliveryStatus takes the per-recipient block that matches the bounce
// kind (a failed block for failed notices, a delayed one for delays).
func (f *fields) fromDeliveryStatus(ds *DeliveryStatus, kind string) {
	if len(ds.Recipients) == 0 {
		return
	}
	pick := ds.Recipients[0]
	for _, r := range ds.Recipients {
		if r.Action == kind {
			pick = r
			break
		}
	}
	f.action = pick.Action
	if pick.OriginalRecipient != "" {
		f.fill("addr", pick.OriginalRecipient)
	} else {
		f.fill("addr", pick.FinalRecipient)
	}
	f.fill("status", pick.Status)
	diag := stripTypePrefix(pick.DiagnosticCode)
	f.fill("diag", diag)
	host, ip := splitMTA(pick.RemoteMTA)
	f.fill("host", host)
	f.fill("ip", ip)
}

// splitMTA separates "mx.example.com. (192.0.2.1, the server for ...)" or
// "mx.example.com[192.0.2.1]" into the host name and the IP address.
func splitMTA(mta string) (host, ip string) {
	mta = strings.TrimSpace(mta)
	if mta == "" {
		return "", ""
	}
	if m := anyIPRe.FindString(mta); m != "" && (strings.Count(m, ":") >= 2 || strings.Count(m, ".") == 3) {
		ip = m
	}
	host = mta
	if i := strings.IndexAny(host, " \t[("); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]().,;:")
	if host == ip {
		host = ""
	}
	return host, ip
}

// bodyRule is one row of the body extraction table. The regular expression
// uses the named groups addr, host, ip, code, status and diag; every group
// that matched fills the corresponding field when it is still empty.
type bodyRule struct {
	name string
	re   *regexp.Regexp
}

const (
	reAddr   = `(?P<addr>[A-Za-z0-9._%+\-=/!#$&'*?^{}|~]+@[A-Za-z0-9.\-]+\.[A-Za-z0-9\-]+)`
	reIP     = `(?P<ip>(?:[0-9]{1,3}\.){3}[0-9]{1,3}|[0-9a-fA-F:]*:[0-9a-fA-F:.]+)`
	reHost   = `(?P<host>[A-Za-z0-9][A-Za-z0-9.\-]*[A-Za-z0-9])`
	reReply  = `(?P<diag>(?P<code>[245][0-9]{2})[ \-](?:(?P<status>[245]\.[0-9]{1,3}\.[0-9]{1,3})\s*)?[^\n]*)`
	reStatus = `(?P<status>[245]\.[0-9]{1,3}\.[0-9]{1,3})`
)

// bodyRules are evaluated in order; each fills only the still-empty fields, so
// the specific MTA formats come first and generic catch-alls last.
var bodyRules = []bodyRule{
	// Postfix: "<addr>: host mx.example.com[192.0.2.1] said: 550 5.1.1 ... (in reply to RCPT TO command)"
	{"postfix_remote", regexp.MustCompile(`(?m)^\s*<?` + reAddr + `>?(?:\s*\(expanded from <[^>]*>\))?:\s*host\s+` + reHost + `\[` + reIP + `\]\s+said:\s*` + reReply)},
	// Postfix: "<addr>: connect to mx.example.com[192.0.2.1]:25: Connection timed out"
	{"postfix_connect", regexp.MustCompile(`(?m)^\s*<?` + reAddr + `>?:\s*(?P<diag>connect to\s+` + reHost + `\[` + reIP + `\][^\n]*)`)},
	// Postfix local: "<addr>: unknown user: \"foo\"" / "<addr>: Host or domain name not found. Name service error for name=..."
	{"postfix_local", regexp.MustCompile(`(?m)^\s*<` + reAddr + `>(?:\s*\(expanded from <[^>]*>\))?:[ \t]*(?P<diag>[^\n]+)`)},
	// Exim: "SMTP error from remote mail server after RCPT TO:<addr>: host mx.example.com [192.0.2.1]: 550 ..."
	{"exim_remote", regexp.MustCompile(`SMTP error from remote mail server after (?:RCPT TO:<` + reAddr + `>|[^:\n]*):\s*(?:host\s+` + reHost + `\s*\[` + reIP + `\]:\s*)?` + reReply)},
	// Exim: "The following address(es) failed:\n\n  addr\n    reason"
	{"exim_failed_list", regexp.MustCompile(`(?s)The following address(?:\(es\)|es)? failed:\s*` + reAddr + `[^\n]*\n\s*(?P<diag>[^\n]+)`)},
	// Exim delay: "The address(es) to which the message has not yet been delivered ... :\n\n  addr"
	{"exim_delayed_list", regexp.MustCompile(`(?s)has not yet been delivered[^\n]*:\s*` + reAddr)},
	// qmail: "<addr>:\n192.0.2.1 does not like recipient.\nRemote host said: 550 ..."
	{"qmail_remote", regexp.MustCompile(`(?s)<` + reAddr + `>:\s*\n(?:` + reIP + ` does not like recipient\.\s*\n)?Remote host said:\s*` + reReply)},
	// qmail local: "<addr>:\nSorry, no mailbox here by that name. (#5.1.1)"
	{"qmail_local", regexp.MustCompile(`(?s)<` + reAddr + `>:\s*\n(?P<diag>[^\n]*?\(#` + reStatus + `\)[^\n]*)`)},
	// Sendmail: "----- The following addresses had permanent fatal errors -----\n<addr>\n    (reason: 550 5.1.1 ...)"
	{"sendmail_list", regexp.MustCompile(`(?s)The following addresses had (?:permanent fatal|transient non-fatal) errors\s*-+\s*<?` + reAddr + `>?(?:\s*\(expanded from:[^)]*\))?(?:\s*\(reason:\s*(?P<diag>[^)\n]*)\))?`)},
	// Sendmail transcript: "while talking to mx.example.com.:" ... "<<< 550 5.1.1 ..."
	{"sendmail_transcript", regexp.MustCompile(`(?s)while talking to ` + reHost + `\.?:.*?<<< ` + reReply)},
	// Exchange / Office 365: "Remote Server returned '550 5.1.1 RESOLVER.ADR.RecipNotFound; not found'"
	{"exchange_remote_server", regexp.MustCompile(`Remote Server(?: at ` + reHost + `(?: \(` + reIP + `\))?)? returned '` + reReply + `'`)},
	// Office 365: "Your message to addr couldn't be delivered."
	{"o365_recipient", regexp.MustCompile(`Your message to ` + reAddr + ` couldn'?t be delivered`)},
	// Exchange legacy: "Delivery has failed to these recipients or groups:\n\nName (addr)"
	{"exchange_recipient_list", regexp.MustCompile(`(?s)(?:Delivery has failed to these recipients or groups|The following recipient\(s\) cannot be reached):\s*(?:[^\n(]*\()?` + reAddr + `\)?`)},
	// Exchange legacy diagnostic: "#5.1.1 smtp;550 5.1.1 ..." / "#< #5.1.1 smtp; ... >"
	{"exchange_legacy_status", regexp.MustCompile(`#` + reStatus + `(?:\s*smtp;\s*` + reReply + `)?`)},
	// Gmail: "Your message wasn't delivered to addr because ..."
	{"gmail_recipient", regexp.MustCompile(`message (?:wasn't|was not|couldn't be) delivered to ` + reAddr)},
	// Gmail: "The response from the remote server was:\n550 5.1.1 ..." / "The response was:\n\n550 ..."
	{"gmail_response", regexp.MustCompile(`(?s)The response (?:from the remote server )?was:\s*` + reReply)},
	// Japanese MTA wording: "<could not deliver to>: addr" / "addr <could not be delivered>".
	// The escaped words are haishin dekimasen (cannot deliver), haishin funou
	// (undeliverable), haishin ni shippai (delivery failed) and todokimasen
	// (did not arrive); the address follows or precedes them on the same line.
	{"jp_failed_recipient", regexp.MustCompile("(?:\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093|\u914d\u4fe1\u4e0d\u80fd|\u914d\u4fe1\u306b\u5931\u6557|\u5c4a\u304d\u307e\u305b\u3093)[^\n]{0,40}?<?" + reAddr + ">?")},
	{"jp_failed_recipient_before", regexp.MustCompile("<?" + reAddr + ">?[^\n]{0,60}(?:\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093|\u914d\u4fe1\u4e0d\u80fd|\u914d\u4fe1\u306b\u5931\u6557|\u5c4a\u304d\u307e\u305b\u3093)")},
	// Generic: "Final-Recipient: rfc822; addr" that ended up in the text part
	{"text_final_recipient", regexp.MustCompile(`(?i)Final-Recipient:\s*(?:rfc822;)?\s*` + reAddr)},
	// Generic: a quoted SMTP reply with an extended status code anywhere in the text
	{"generic_reply", regexp.MustCompile(`(?m)(?:^|[:\s])` + `(?P<diag>(?P<code>[245][0-9]{2})[ \-]` + reStatus + `[^\n]*)`)},
	// Generic: an extended status code on its own
	{"generic_status", regexp.MustCompile(`(?:^|[^0-9.])` + reStatus + `(?:[^0-9.]|$)`)},
}

var (
	anyIPRe           = regexp.MustCompile(`(?:[0-9]{1,3}\.){3}[0-9]{1,3}|[0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{0,4}){2,7}`)
	bracketIPRe       = regexp.MustCompile(`[\[(]` + reIP + `[\])]`)
	leadingCodeRe     = regexp.MustCompile(`^\s*(?P<code>[245][0-9]{2})[ \-]`)
	statusInTextRe    = regexp.MustCompile(`(?:^|[^0-9.])(?P<status>[245]\.[0-9]{1,3}\.[0-9]{1,3})(?:[^0-9.]|$)`)
	codeInTextRe      = regexp.MustCompile(`(?:^|[^0-9.])(?P<code>[245][0-9]{2})(?:[^0-9.]|$)`)
	inReplyToRe       = regexp.MustCompile(`\s*\(in reply to [^)]*\)\s*$`)
	failedRecipientRe = regexp.MustCompile(reAddr)
)

// fromBody applies the rule table to the body text and the X-Failed-Recipients
// header.
func (f *fields) fromBody(body string, headers map[string]string, exclude []string) {
	if f.addr == "" {
		if v := headers["X-Failed-Recipients"]; v != "" {
			if m := failedRecipientRe.FindString(v); m != "" && !excluded(m, exclude) {
				f.fill("addr", m)
			}
		}
	}
	if body == "" {
		return
	}
	for _, rule := range bodyRules {
		m := rule.re.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		for i, name := range rule.re.SubexpNames() {
			if name == "" || m[i] == "" {
				continue
			}
			if name == "addr" && excluded(m[i], exclude) {
				continue
			}
			f.fill(name, m[i])
		}
	}
}

// finish derives the remaining values from what was collected: the status
// and SMTP code hidden inside the diagnostic, the IP inside the MTA name and
// a cleaned-up diagnostic text.
func (f *fields) finish() {
	f.diag = strings.TrimSpace(inReplyToRe.ReplaceAllString(f.diag, ""))
	f.diag = strings.Join(strings.Fields(f.diag), " ")
	if f.status == "" {
		if m := statusInTextRe.FindStringSubmatch(f.diag); m != nil {
			f.status = m[1]
		}
	}
	if f.code == "" {
		if m := leadingCodeRe.FindStringSubmatch(f.diag); m != nil {
			f.code = m[1]
		} else if m := codeInTextRe.FindStringSubmatch(f.diag); m != nil {
			f.code = m[1]
		}
	}
	// No SMTP reply code is synthesized from the status class: smtp_code is
	// shown verbatim and stays "" when the notice carries no 3-digit reply.
	if f.ip == "" {
		if m := bracketIPRe.FindStringSubmatch(f.host); m != nil {
			f.ip = m[1]
		} else if m := bracketIPRe.FindStringSubmatch(f.diag); m != nil {
			f.ip = m[1]
		}
	}
	if f.host != "" {
		f.host = strings.ToLower(strings.TrimSuffix(bracketIPRe.ReplaceAllString(f.host, ""), "."))
		f.host = strings.TrimSpace(f.host)
	}
	f.addr = strings.TrimSpace(strings.Trim(f.addr, "<>"))
}

// excluded reports whether addr is one of the addresses to ignore.
func excluded(addr string, exclude []string) bool {
	addr = strings.ToLower(cleanAngleAddress(addr))
	for _, e := range exclude {
		if e != "" && strings.EqualFold(strings.TrimSpace(e), addr) {
			return true
		}
	}
	return false
}

// domainOf returns the lower-cased domain of an address ("" when none).
func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i < len(addr)-1 {
		return strings.ToLower(strings.Trim(addr[i+1:], ".>"))
	}
	return ""
}

// unfold joins indented continuation lines to the previous line so that
// wrapped MTA messages ("<addr>: host ... said: 550\n    5.1.1 ...") match
// single-line patterns. Blank lines are preserved as paragraph breaks.
func unfold(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if len(out) > 0 && trimmed != "" && (line[0] == ' ' || line[0] == '\t') && out[len(out)-1] != "" {
			prev := out[len(out)-1]
			// Keep list-style blocks (address on its own line followed by an
			// indented reason) on separate lines: those are matched with \s*.
			if strings.HasSuffix(prev, ":") || strings.HasSuffix(prev, "said:") || strings.Contains(prev, " said: ") ||
				strings.Contains(prev, "returned '") || strings.HasSuffix(prev, ",") {
				out[len(out)-1] = prev + " " + strings.TrimSpace(trimmed)
				continue
			}
			if strings.HasSuffix(prev, "-") || bareCodeLineRe.MatchString(prev) {
				out[len(out)-1] = prev + " " + strings.TrimSpace(trimmed)
				continue
			}
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// bareCodeLineRe matches a line that ends with an SMTP reply code, i.e. the
// remainder of the reply was wrapped to the next line.
var bareCodeLineRe = regexp.MustCompile(`(?:said:|:)\s*[245][0-9]{2}(?:[ \-][245]\.[0-9]{1,3}\.[0-9]{1,3})?\s*$`)
