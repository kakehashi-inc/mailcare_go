// Package mailengine fetches mail over IMAP, stores every message as raw files
// under the mails directory, parses and classifies bounce (mail daemon)
// notices, bundles them into category-based groups and maintains the
// per-mailbox index database (see app/models/mailindex.go).
//
// The work is split into independent phases the caller (app/modules/jobs.go)
// runs separately or in sequence:
//
//	FetchMailbox  download + parse + store + index rows (classified = 0)
//	GroupMailbox  classify + extract + categorize + group the rows not yet
//	              processed (full = true redoes every message)
//	Reindex       rebuild the index from the raw files (fetch-equivalent
//	              import followed by a full grouping, states carried over)
//
// The package never touches the master database: it receives the mailbox row
// and the decrypted password from the caller and reports back through the
// returned results and the Progress callback.
//
// File layout inside mailsRoot (data/mails):
//
//	<address>/<message_key>.eml   the original message (RFC 5322)
//	<address>/<message_key>.txt   the decoded text body ("" when none)
//	<address>/<message_key>.html  the decoded HTML body (only when present)
//	<address>/<message_key>.json  parsed headers/metadata (ParsedMessage)
//	<address>.sqlite              the index database
//
// <address> is the mail address made safe for every OS (SanitizeAddress).
//
// Files of the package:
//
//	engine.go    exported types and the contract (this file)
//	paths.go     address sanitizing, file paths, message keys, atomic writes
//	imap.go      IMAP connection, TestConnection and FetchMailbox
//	parse.go     ParsedMessage and the MIME parser
//	classify.go  the bounce classification rule table (design 5.3)
//	extract.go   extraction of bounce details (delivery-status and body text)
//	category.go  the category rule table and the unit / authority extractors
//	grouping.go  diagnostic template, group key/title and group maintenance
//	group.go     GroupMailbox (the grouping phase) and the per-message step
//	store.go     storing one message (files + index row) shared by fetch/reindex
//	reindex.go   OpenIndex and Reindex
//	carryover.go what Reindex keeps from the previous index
package mailengine

// Progress receives human-readable progress lines while a long operation runs.
type Progress func(msg string)

// FetchResult is the outcome of one FetchMailbox run.
type FetchResult struct {
	Fetched     int    // messages newly downloaded and indexed (classified = 0)
	Skipped     int    // messages skipped (too large / duplicates)
	UIDValidity uint32 // the folder's UIDVALIDITY seen during the run
}

// GroupResult is the outcome of one GroupMailbox run.
type GroupResult struct {
	Processed int // messages classified during the run
	Bounces   int // of which detected as bounce notices (auto-replies included)
	Groups    int // groups in the index after the run
	// GroupsTouched lists the actionable groups whose message count grew
	// during the run (new or existing); the caller schedules their analysis.
	GroupsTouched []string
}

// ReindexResult is the outcome of Reindex.
type ReindexResult struct {
	Messages int
	Bounces  int
	Groups   int
}

// Values mirrored from app/modules/constants.go. mailengine cannot import
// app/modules (modules depends on mailengine), so the handful of values the
// engine needs are repeated here and must be kept in sync.
const (
	defaultInitialDays = 90
	defaultRecentDays  = 30
	defaultIMAPPort    = 993
	defaultFolder      = "INBOX"
	maxMessageSize     = 20 * 1024 * 1024

	imapSecuritySSL      = "ssl"
	imapSecurityStartTLS = "starttls"
	imapSecurityNone     = "none"

	bounceKindFailed    = "failed"
	bounceKindDelayed   = "delayed"
	bounceKindAutoReply = "auto_reply"
	bounceKindOther     = "other"

	responsibleSender    = "sender"
	responsibleRecipient = "recipient"
	responsibleDomain    = "domain"
	responsibleUnknown   = "unknown"

	// Group categories (groups.category). The actionable ones name a problem
	// the mail administrator can act on; the others are recipient-side.
	categoryIPBlocked       = "ip_blocked"
	categoryRateLimited     = "rate_limited"
	categorySenderBlocked   = "sender_blocked"
	categoryAuthFailure     = "auth_failure"
	categoryContentRejected = "content_rejected"
	categoryMessageTooLarge = "message_too_large"
	categoryServerConfig    = "server_config"
	categoryUnknownFailure  = "unknown_failure"
	categoryUserUnknown     = "user_unknown"
	categoryMailboxFull     = "mailbox_full"
	categoryMailboxDisabled = "mailbox_disabled"
	categoryDomainNotFound  = "domain_not_found"
	categoryDeliveryDelay   = "delivery_delay"
)

// progressInterval is how many messages the long loops process between two
// progress lines.
const progressInterval = 50

// report calls the progress callback when one was given.
func report(progress Progress, msg string) {
	if progress != nil {
		progress(msg)
	}
}
