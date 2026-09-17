package agent

import "strings"

// Category names of bounce groups (same literals as app/modules/constants.go;
// this package must not import app/modules).
const (
	CategoryIPBlocked       = "ip_blocked"
	CategoryRateLimited     = "rate_limited"
	CategorySenderBlocked   = "sender_blocked"
	CategoryAuthFailure     = "auth_failure"
	CategoryContentRejected = "content_rejected"
	CategoryMessageTooLarge = "message_too_large"
	CategoryServerConfig    = "server_config"
	CategoryUnknownFailure  = "unknown_failure"
	CategoryUserUnknown     = "user_unknown"
	CategoryMailboxFull     = "mailbox_full"
	CategoryMailboxDisabled = "mailbox_disabled"
	CategoryDomainNotFound  = "domain_not_found"
	CategoryDeliveryDelay   = "delivery_delay"
)

// CategoryInfo is the English glossary entry of one group category as the
// prompt explains it to the agent.
type CategoryInfo struct {
	// Description is a one-line explanation of what the category means.
	Description string
	// Unit says what groups.unit_value is for this category (the thing the
	// mail administrator acts on, or the recipient-side thing at fault).
	Unit string
	// Authority says what groups.authority is for this category (the party
	// that decides the outcome); "" when the category has no authority.
	Authority string
	// Guidance is the focus of the recommended actions for this category,
	// written from the mail administrator's point of view.
	Guidance string
	// Actionable is true when the mail administrator can fix the problem.
	Actionable bool
}

// CategoryGlossary maps a category name to its glossary entry. The prompt
// uses it to lead the group summary with the category and to steer the
// requested actions; the Web UI has its own translations.
var CategoryGlossary = map[string]CategoryInfo{
	CategoryIPBlocked: {
		Description: "Our sending IP is listed on a DNS blacklist or blocked by the recipient's MTA because of its reputation.",
		Unit:        "the sending IP address that is blocked",
		Authority:   "the blacklist provider that lists the IP, or the recipient domain when no list is named",
		Guidance:    "give the delisting steps for the named blacklist (its lookup and removal request page, what to check before requesting removal), how to find and stop the traffic that caused the listing, and how to verify the IP is clean again",
		Actionable:  true,
	},
	CategoryRateLimited: {
		Description: "The recipient domain is throttling or temporarily deferring mail from our sending IP or server (too many messages or connections).",
		Unit:        "the sending IP or server that is being throttled",
		Authority:   "the recipient domain that applies the limit",
		Guidance:    "give the sending rate, connection and queue settings to adjust on our MTA for that domain, and how to confirm the deferrals stop",
		Actionable:  true,
	},
	CategoryAuthFailure: {
		Description: "The recipient domain rejects our mail because sender authentication fails (SPF, DKIM, DMARC, reverse DNS / PTR or HELO checks).",
		Unit:        "our sending domain whose authentication records are checked",
		Authority:   "the recipient domain that enforces the check",
		Guidance:    "give the SPF, DKIM, DMARC, PTR or HELO fixes to make on the sending domain and MTA, exactly which record or setting to change, and how to verify the fix",
		Actionable:  true,
	},
	CategorySenderBlocked: {
		Description: "The recipient domain rejects mail from our sender address (the address itself is blocked, denied or has a poor reputation).",
		Unit:        "our sender address that is rejected",
		Authority:   "the recipient domain that blocks the address",
		Guidance:    "give the steps to restore the reputation of that sender address (stop the offending traffic, request unblocking from the recipient domain, consider a different sender address) and how to verify the block is lifted",
		Actionable:  true,
	},
	CategoryContentRejected: {
		Description: "The recipient domain rejects the message because of its content (spam score, virus or malware detection, attachment type or policy).",
		Unit:        "our sender address whose messages are rejected",
		Authority:   "the recipient domain that applies the content policy",
		Guidance:    "identify what in the message content or attachments triggers the rejection and give the changes to make to the sent mail (content, attachments, links, sending practice) and how to verify",
		Actionable:  true,
	},
	CategoryMessageTooLarge: {
		Description: "The recipient domain rejects the message because it exceeds its size limit.",
		Unit:        "our sender address that sends the oversized messages",
		Authority:   "the recipient domain that enforces the size limit",
		Guidance:    "give the size limit to respect for that domain and how to reduce or split the messages (attachments, links to files) sent by that sender address",
		Actionable:  true,
	},
	CategoryServerConfig: {
		Description: "Our MTA cannot deliver because of its own configuration (relay denied, TLS or STARTTLS problems, HELO rejected, protocol or command errors).",
		Unit:        "our MTA (the reporting MTA) whose configuration is at fault",
		Authority:   "the recipient domain that rejects the session",
		Guidance:    "give the MTA configuration changes to make on our server (relay, TLS certificates and protocols, HELO name, DNS) and how to verify delivery works again",
		Actionable:  true,
	},
	CategoryUnknownFailure: {
		Description: "A permanent failure that matches no known pattern; the cause has to be read from the diagnostic text itself.",
		Unit:        "the recipient domain that reported the failure",
		Authority:   "the status code and diagnostic template of the failure",
		Guidance:    "determine the actual cause from the notices, say which of the known categories it resembles if any, and give the concrete actions the mail administrator can take",
		Actionable:  true,
	},
	CategoryUserUnknown: {
		Description: "The recipient address does not exist (user unknown, no such user, recipient not found).",
		Unit:        "the recipient address that does not exist",
		Guidance:    "tell the owner of the recipient address list to remove or correct the address; nothing on our mail server needs to change",
		Actionable:  false,
	},
	CategoryMailboxFull: {
		Description: "The recipient mailbox is over quota or out of storage.",
		Unit:        "the recipient address whose mailbox is full",
		Guidance:    "tell the recipient (or the owner of the address list) that the mailbox must be emptied; nothing on our mail server needs to change",
		Actionable:  false,
	},
	CategoryMailboxDisabled: {
		Description: "The recipient mailbox is disabled, inactive or suspended.",
		Unit:        "the recipient address whose mailbox is disabled",
		Guidance:    "tell the owner of the recipient address list that the mailbox is disabled and should be removed or replaced; nothing on our mail server needs to change",
		Actionable:  false,
	},
	CategoryDomainNotFound: {
		Description: "The recipient domain does not exist or has no usable MX record, so the mail cannot be routed.",
		Unit:        "the recipient domain that cannot be resolved",
		Guidance:    "tell the owner of the recipient address list that the domain is invalid (typo or expired domain) and should be corrected; nothing on our mail server needs to change",
		Actionable:  false,
	},
	CategoryDeliveryDelay: {
		Description: "Delivery to the recipient domain is delayed (connection timeouts, greylisting, temporary failures); the mail is still queued.",
		Unit:        "the recipient domain that is slow or unreachable",
		Guidance:    "say whether the delay looks temporary and what to tell the recipient domain's administrator if it persists; nothing on our mail server needs to change unless the notices show otherwise",
		Actionable:  false,
	},
}

// unknownCategory is used for groups whose category is empty or not in the
// glossary (an index written before categories existed, or a newer rule).
var unknownCategory = CategoryInfo{
	Description: "Category not classified by MailCare; determine the kind of problem from the notices.",
	Unit:        "the thing the mail administrator has to act on, as far as MailCare could tell",
	Authority:   "the party that decides the outcome, as far as MailCare could tell",
	Guidance:    "determine the kind of problem from the notices and give the concrete actions the mail administrator can take",
	Actionable:  true,
}

// categoryInfoFor returns the glossary entry of a category name (trimmed and
// lower-cased) and whether it was found.
func categoryInfoFor(name string) (CategoryInfo, bool) {
	info, ok := CategoryGlossary[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return unknownCategory, false
	}
	return info, true
}
