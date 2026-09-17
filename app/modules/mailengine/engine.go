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
//	PruneMailbox  remove the mails older than the retention (files and
//	              rows; the groups they belonged to are recounted)
//
// The package never touches the master database: it receives the mailbox row
// and the decrypted password from the caller and reports back through the
// returned results and the Progress callback.
//
// File layout inside mailsRoot (data/mails):
//
//	<address>/<message_key>.eml     the original message (RFC 5322, untouched)
//	<address>/<message_key>-1.txt   the first text/plain section with content (UTF-8)
//	<address>/<message_key>-2.txt   the second one, then -3, -4, ... in MIME order
//	<address>/<message_key>-1.html  the text/html sections, numbered the same way
//	<address>.sqlite                the index database (headers, section counts,
//	                                detection outcome, bounce details, groups, reports)
//
// Only sections with content get a file (messages.text_count / html_count
// say how many exist); there is no parsed sidecar, the .eml is parsed again
// whenever the engine needs more than the index row holds. <address> is the
// mail address made safe for every OS (SanitizeAddress).
//
// Files of the package:
//
//	engine.go    exported types and the contract (this file)
//	paths.go     address sanitizing, file and section paths, message keys, atomic writes
//	imap.go      IMAP connection, TestConnection and FetchMailbox
//	parse.go     ParsedMessage and the MIME parser
//	classify.go  the bounce classification rule table (design 5.3)
//	extract.go   extraction of bounce details (delivery-status and body text)
//	category.go  the category rule table and the unit / authority extractors
//	grouping.go  diagnostic template, group key/title and group maintenance
//	group.go     GroupMailbox (the grouping phase) and the per-message step
//	store.go     storing one message (section files + index row) shared by fetch/reindex
//	reindex.go   OpenIndex and Reindex
//	carryover.go what Reindex keeps from the previous index (sources, states, reports)
//	prune.go     PruneMailbox (the mail retention) and RemoveStaleTempFiles
package mailengine

import "time"

// Progress receives human-readable progress lines while a long operation runs.
type Progress func(msg string)

// FetchOptions tunes one FetchMailbox run.
type FetchOptions struct {
	// NotBefore, when set, is the earliest date the run searches for: the
	// window derived from initial_days / recent_days never reaches before
	// it. The caller passes now - mail_keep_days so that a fetch does not
	// download mail the daily cleanup would remove again.
	NotBefore time.Time
}

// FetchResult is the outcome of one FetchMailbox run.
type FetchResult struct {
	Fetched     int    // messages newly downloaded and indexed (classified = 0)
	Skipped     int    // messages skipped (duplicates)
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
	// Skipped counts the raw files that could not be indexed (unreadable, or
	// no free synthetic identity); each is reported as a progress line.
	Skipped int
}

// PruneResult is the outcome of PruneMailbox.
type PruneResult struct {
	Removed       int // messages removed (index rows with their files)
	RemovedGroups int // groups left without messages and removed with their reports
}

// Values mirrored from app/modules/constants.go. mailengine cannot import
// app/modules (modules depends on mailengine), so the handful of values the
// engine needs are repeated here and must be kept in sync.
const (
	defaultInitialDays = 90
	defaultRecentDays  = 30
	defaultIMAPPort    = 993
	defaultFolder      = "INBOX"

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
