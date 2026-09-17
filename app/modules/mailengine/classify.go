package mailengine

import (
	"regexp"
	"strings"
)

// Classification is the outcome of the bounce detection for one message
// (messages.is_bounce / bounce_kind / rule / body_source).
type Classification struct {
	IsBounce bool
	Kind     string // failed | delayed | auto_reply | other ("" when not a bounce)
	Rule     string // name of the matching rule ("" when not a bounce)
	// BodySource is the body the decision was made on: the primary body
	// ("text", else "html") or "html" when only the HTML body of a message
	// that also has a text body matched; "" when the message has no body.
	BodySource string
}

// classifyRule is one row of the rule table (design 5.3). Rules are evaluated
// in order and the first match wins. match returns the bounce kind ("" means
// the rule does not apply).
type classifyRule struct {
	name  string
	match func(pm *ParsedMessage, body string) string
}

// classifyRules is the rule table. Keep the order of the design document:
// structured DSNs first, then daemon senders, then auto-replies (before the
// subject patterns, so that an automatic reply quoting a bounce subject is
// not taken for the bounce), then subject patterns, daemon display names and
// finally the body wording.
var classifyRules = []classifyRule{
	{
		// 1. multipart/report; report-type=delivery-status
		name: "dsn_report",
		match: func(pm *ParsedMessage, body string) string {
			if pm.ContentType != "multipart/report" || pm.ReportType != "delivery-status" {
				if pm.DeliveryStatus == nil || len(pm.DeliveryStatus.Recipients) == 0 {
					return ""
				}
			}
			return kindFromDeliveryStatus(pm, body)
		},
	},
	{
		// 2. From / Return-Path local part is a mail daemon
		name: "daemon_sender",
		match: func(pm *ParsedMessage, body string) string {
			if !isDaemonAddress(pm.FromAddress) && !isDaemonAddress(pm.ReturnPath) {
				return ""
			}
			return kindFromText(pm.Subject, body)
		},
	},
	{
		// 3. Auto-Submitted: auto-replied / auto-generated (an automatic
		// reply that is not a DSN and not from a daemon) or an out-of-office
		// subject. Evaluated before the subject patterns: "Automatic reply:
		// Undeliverable: ..." is a reply, not the bounce it quotes.
		name: "auto_reply",
		match: func(pm *ParsedMessage, body string) string {
			if isAutoSubmitted(pm.AutoSubmitted) || autoReplySubjectRe.MatchString(pm.Subject) {
				return bounceKindAutoReply
			}
			return ""
		},
	},
	{
		// 4. Subject matches a known bounce pattern
		name: "subject_pattern",
		match: func(pm *ParsedMessage, body string) string {
			if !bounceSubjectRe.MatchString(pm.Subject) {
				return ""
			}
			if delayedSubjectRe.MatchString(pm.Subject) {
				return bounceKindDelayed
			}
			return bounceKindFailed
		},
	},
	{
		// 5. From display name of a mail delivery system: a daemon mail, but
		// a failure only when the subject or body says so ("Postmaster Team"
		// announcing maintenance is "other" and is not grouped).
		name: "daemon_display_name",
		match: func(pm *ParsedMessage, body string) string {
			if daemonDisplayNameRe.MatchString(pm.FromName) {
				return kindFromText(pm.Subject, body)
			}
			return ""
		},
	},
	{
		// 6. The body itself carries the wording of a non-delivery report
		// (used when neither the structure nor the sender nor the subject
		// gave it away, e.g. a notification relay forwarding an NDR; also
		// what makes an HTML-only bounce wording count).
		name: "body_pattern",
		match: func(pm *ParsedMessage, body string) string {
			if !bounceBodyRe.MatchString(body) {
				return ""
			}
			return kindFromText(pm.Subject, body)
		},
	},
}

var (
	// daemonLocalParts are the local parts that identify a mail daemon sender.
	daemonLocalParts = map[string]bool{"mailer-daemon": true, "postmaster": true, "mail-daemon": true}

	bounceSubjectRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`undelivered mail returned to sender`,
		`delivery status notification`,
		`mail delivery failed`,
		`mail delivery failure`,
		`returned mail`,
		`undeliverable`,
		`failure notice`,
		`delivery failure`,
		`delivery has failed`,
		`non-?delivery`,
		`mail system error`,
		`delayed mail`,
		`delivery delayed`,
		`warning: could not send`,
		`warning: message delayed`,
		`could not be delivered`,
		`wasn't delivered`,
		`was not delivered`,
		"\u914d\u4fe1\u4e0d\u80fd",                   // haishin funou: undeliverable
		"\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093", // haishin dekimasen: cannot deliver
		"\u9001\u4fe1\u3067\u304d\u307e\u305b\u3093", // soushin dekimasen: cannot send
		"\u914d\u4fe1\u306b\u5931\u6557",             // haishin ni shippai: delivery failed
		"\u914d\u4fe1\u9045\u5ef6",                   // haishin chien: delivery delayed
		"\u30e1\u30fc\u30eb\u3092\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093", // mail wo haishin dekimasen
	}, "|"))

	delayedSubjectRe = regexp.MustCompile("(?i)delay|warning|deferred|\u9045\u5ef6|still trying|not yet been delivered") // \u9045\u5ef6 = chien (delay)

	delayedBodyRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`has not yet been delivered`,
		`could not be delivered for`,
		`will keep trying`,
		`will continue to try`,
		`still trying`,
		`delivery (?:is |has been |was )?(?:temporarily )?delayed`,
		`message (?:is |has been )?delayed`,
		`delay(?:ed|ing) (?:in )?deliver`,
		`this is a delivery delay`,
		`temporarily deferred`,
		`action: delayed`,
		"\u9045\u5ef6", // chien: delay
		"\u307e\u3060\u914d\u4fe1\u3055\u308c\u3066\u3044\u307e\u305b\u3093", // mada haishin sarete imasen: not delivered yet
	}, "|"))

	failedBodyRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`permanent(?:ly)? (?:fatal )?error`,
		`could ?n[o']t be delivered`,
		`wasn't delivered`,
		`delivery (?:has )?failed`,
		`undeliverable`,
		`action: failed`,
		`user unknown`,
		`no such user`,
		`does not exist`,
		`mailbox unavailable`,
		`rejected`,
		`\b5\.[0-9]{1,3}\.[0-9]{1,3}\b`,
		`\b5[0-9]{2}[ -]`,
	}, "|"))

	autoReplySubjectRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`out of (?:the )?office`,
		`auto(?:matic)?[ -]?repl(?:y|ied)`,
		`autoreply`,
		`automatische antwort`,
		"r[e\u00e9]ponse automatique",
		"\u81ea\u52d5\u8fd4\u4fe1", // jidou henshin: automatic reply
		"\u81ea\u52d5\u5fdc\u7b54", // jidou outou: automatic response
		"\u4e0d\u5728(?:\u901a\u77e5|\u306e\u304a\u77e5\u3089\u305b|\u3067\u3059|\u4e2d)?", // fuzai: out of office
		"\u4f11\u6687\u4e2d", // kyuuka chuu: on vacation
		"\u5916\u51fa\u4e2d", // gaishutsu chuu: away
	}, "|"))

	// bounceBodyRe lists the phrases that only a non-delivery report carries
	// (strict on purpose: a normal mail quoting an error must not match).
	bounceBodyRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`delivery has failed to these recipients`,
		`the following recipient\(s\) cannot be reached`,
		`undelivered mail returned to sender`,
		`the following address(?:\(es\)|es)? failed`,
		`could not be delivered to (?:one or more|the following)`,
		`(?:message|mail) (?:wasn't|was not|couldn't be|could not be) delivered to`,
		`this is the mail system at host`,
		`remote server returned '[45][0-9]{2}`,
		`delivery status notification \((?:failure|delay)\)`,
		`delivery to the following recipients? (?:failed|was delayed)`,
		`your message did not reach some or all of the intended recipients`,
	}, "|"))

	daemonDisplayNameRe = regexp.MustCompile(`(?i)^\s*(?:mail delivery (?:sub)?system|mail delivery service|mail administrator|mailer[ -]?daemon|postmaster|internet mail delivery|delivery notification)\b`)
)

// Classify runs the rule table over a parsed message: first with the
// primary body (every text section joined, else the HTML sections rendered
// as text), then, when the message also carries HTML sections, with the
// HTML text. A match on either makes the message a bounce (design 5.2 / 5.3).
func Classify(pm *ParsedMessage) Classification {
	if pm == nil {
		return Classification{}
	}
	if c, ok := classifyWith(pm, pm.bodyForClassification()); ok {
		c.BodySource = pm.primarySource()
		return c
	}
	if secondary := pm.secondaryBody(); secondary != "" {
		if c, ok := classifyWith(pm, secondary); ok {
			c.BodySource = bodySourceHTML
			return c
		}
	}
	return Classification{BodySource: pm.primarySource()}
}

// classifyWith evaluates the rule table with one body text.
func classifyWith(pm *ParsedMessage, body string) (Classification, bool) {
	for _, rule := range classifyRules {
		if kind := rule.match(pm, body); kind != "" {
			return Classification{IsBounce: true, Kind: kind, Rule: rule.name}, true
		}
	}
	return Classification{}, false
}

// isAutoSubmitted reports whether an Auto-Submitted header value (lower
// case) marks an automatic reply or an automatically generated message
// ("auto-replied", "auto-generated", with or without a comment such as
// "(failure)"). "no" and an empty value do not.
func isAutoSubmitted(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "auto-replied") || strings.HasPrefix(value, "auto-generated")
}

// isDaemonAddress reports whether the local part of address is a mail daemon.
func isDaemonAddress(address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return false
	}
	local := address
	if i := strings.LastIndexByte(address, '@'); i >= 0 {
		local = address[:i]
	}
	// Some hosts add a suffix ("postmaster+bounce", "mailer-daemon-abc").
	for prefix := range daemonLocalParts {
		if local == prefix || strings.HasPrefix(local, prefix+"+") || strings.HasPrefix(local, prefix+"-") {
			return true
		}
	}
	return false
}

// kindFromDeliveryStatus derives failed / delayed / other from the DSN actions
// (normalized by the parser: failed / delayed / delivered / relayed /
// expanded or ""), falling back to the class of the status code. A DSN that
// only reports successes (delivered / relayed / expanded, or a 2.x.x
// status) is a daemon mail but not a failure, so it becomes "other" and is
// not grouped.
func kindFromDeliveryStatus(pm *ParsedMessage, body string) string {
	var failed, delayed, success int
	if pm.DeliveryStatus != nil {
		for _, r := range pm.DeliveryStatus.Recipients {
			switch r.Action {
			case "failed":
				failed++
			case "delayed":
				delayed++
			case "delivered", "relayed", "expanded":
				success++
			default:
				switch {
				case strings.HasPrefix(r.Status, "5"):
					failed++
				case strings.HasPrefix(r.Status, "4"):
					delayed++
				case strings.HasPrefix(r.Status, "2"):
					success++
				}
			}
		}
	}
	switch {
	case failed > 0:
		return bounceKindFailed
	case delayed > 0:
		return bounceKindDelayed
	case success > 0:
		return bounceKindOther
	}
	// No usable per-recipient action: fall back to the wording.
	return kindFromText(pm.Subject, body)
}

// kindFromText decides between delayed, failed and other from the subject
// and body wording. Failure wording wins over delay wording because delay
// notices rarely mention a permanent error while failure notices often say
// "delayed ... and will not be retried". A daemon mail whose subject and
// body carry neither failure nor delay wording (a success report, an
// announcement from postmaster) is "other": recorded, not grouped.
func kindFromText(subject, body string) string {
	if delayedSubjectRe.MatchString(subject) && !failedSubjectHint(subject) {
		return bounceKindDelayed
	}
	if failedBodyRe.MatchString(body) {
		return bounceKindFailed
	}
	if delayedBodyRe.MatchString(body) {
		return bounceKindDelayed
	}
	if failedSubjectHint(subject) || bounceSubjectRe.MatchString(subject) {
		return bounceKindFailed
	}
	return bounceKindOther
}

// failedSubjectHint reports whether a subject that also contains delay wording
// clearly announces a permanent failure ("Undelivered Mail Returned to Sender").
func failedSubjectHint(subject string) bool {
	return failedSubjectHintRe.MatchString(subject)
}

// The two escaped words are haishin funou (undeliverable) and haishin
// dekimasen (cannot deliver).
var failedSubjectHintRe = regexp.MustCompile("(?i)returned to sender|undeliverable|failure|failed|\u914d\u4fe1\u4e0d\u80fd|\u914d\u4fe1\u3067\u304d\u307e\u305b\u3093")
