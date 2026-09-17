// Package mailengine fetches mail over IMAP, stores every message as raw files
// under the mails directory, parses and classifies bounce (mail daemon)
// notices, bundles similar bounces into groups and maintains the per-mailbox
// index database (see app/models/mailindex.go).
//
// The package never touches the master database: it receives the mailbox row
// and the decrypted password from the caller (app/modules/jobs.go) and reports
// back through the returned results and the Progress callback.
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
//	imap.go      IMAP connection, TestConnection and CheckMailbox
//	parse.go     ParsedMessage and the MIME parser
//	classify.go  the bounce classification rule table
//	extract.go   extraction of bounce details (delivery-status and body text)
//	grouping.go  diagnostic template, group key/title and group maintenance
//	store.go     storing one message (files + index rows) shared by check/reindex
//	reindex.go   OpenIndex, Reindex and Reclassify
package mailengine

// Progress receives human-readable progress lines while a long operation runs.
type Progress func(msg string)

// CheckResult is the outcome of one CheckMailbox run.
type CheckResult struct {
	Fetched       int      // messages newly downloaded and indexed
	Bounces       int      // of which classified as bounce notices
	Skipped       int      // messages skipped (too large / unparsable)
	UIDValidity   uint32   // the folder's UIDVALIDITY seen during the run
	GroupsTouched []string // group keys that gained messages (need analysis)
}

// ReindexResult is the outcome of Reindex / Reclassify.
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
)

// progressInterval is how many messages Reindex / Reclassify process between
// two progress lines.
const progressInterval = 50

// report calls the progress callback when one was given.
func report(progress Progress, msg string) {
	if progress != nil {
		progress(msg)
	}
}
