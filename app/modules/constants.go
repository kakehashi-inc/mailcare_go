package modules

import (
	"time"

	"mailcare/app/modules/agent"
)

// --- Application ---

const (
	// AppName is the executable name, also used as the CLI name and in status
	// output.
	AppName = "mailcare"
	// DBFileName is the master SQLite database file created inside the data
	// directory. Per-mailbox indexes live in MailsDirName (see mailengine).
	DBFileName = "mailcare.db"
	// MailsDirName is the directory (inside the data directory) that holds the
	// raw mail files, their decoded body sections and the per-mailbox index
	// databases:
	//   data/mails/<address>/<message_key>.eml
	//   data/mails/<address>/<message_key>-1.txt, <message_key>-2.txt, ...
	//   data/mails/<address>/<message_key>-1.html, <message_key>-2.html, ...
	//   data/mails/<address>.sqlite
	MailsDirName = "mails"
	// AgentDirName is the directory (inside the data directory) that holds the
	// agent workspaces, one directory per analysis run:
	//   data/agent/<address>/<group_key>/<report_id>/{PROMPT.md,RESULT.log,REPORT.md,AGENTS.md}
	// Together with the master database, MailsDirName and AgentDirName are
	// everything the data directory contains.
	AgentDirName = "agent"
)

// --- Token / session ---

const (
	// TokenPrefix is prepended to the random part of every login token.
	TokenPrefix = "mlc_"
	// TokenRandomBytes is the number of random bytes (hex-encoded) in a token.
	TokenRandomBytes = 20 // -> 40 hex chars, full token "mlc_" + 40 = 44 chars
	// CookieName is the Web session cookie.
	CookieName = "mlc_session"
	// SessionCookieTTLHours is the lifetime of a session that was opened
	// without "Stay logged in": a browser-session cookie (no Max-Age) whose
	// sealed expiry is this many hours away and which is never refreshed.
	SessionCookieTTLHours = 24
	// SessionRefreshInterval throttles sliding session-cookie re-issuance.
	SessionRefreshInterval = 24 * time.Hour
	// PasswordMinLength is the minimum accepted password length.
	PasswordMinLength = 8
)

// --- Roles ---

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// --- Server defaults ---

const (
	DefaultWebListenAddr = "0.0.0.0"
	DefaultWebPort       = 9790
	// DefaultCookieTTLHours is the lifetime of a "Stay logged in" session.
	DefaultCookieTTLHours = 720 // 30 days
)

// --- Mail check defaults ---

const (
	// DefaultCheckTimes is the default daily schedule (local time, HH:MM).
	DefaultCheckTimes = "06:00,12:00,18:00"
	// DefaultInitialDays is how far back the first check of a mailbox looks.
	DefaultInitialDays = 90
	// DefaultRecentDays is how far back every later check looks.
	DefaultRecentDays = 10
	// DefaultServerKeepDays is the server retention of a newly registered
	// mailbox: the days (from the mail's date) a notice classified with
	// certain evidence or a junk mail (mailengine.serverDeletionRules) stays
	// on the IMAP server before the daily cleanup deletes it there. 0 keeps every mail on the
	// server; a mailbox saved before the setting existed reads as 0. A new
	// mailbox gets at most the mail retention (mail_keep_days), and the
	// retention cannot be set above it (checkServerKeepDays).
	DefaultServerKeepDays = 60
	// MaxServerKeepDays bounds the server retention.
	MaxServerKeepDays = 3650
	// DefaultIMAPPort is the IMAPS port used when none is given.
	DefaultIMAPPort = 993
)

// --- IMAP security modes (mailboxes.imap_security) ---

const (
	IMAPSecuritySSL      = "ssl"      // implicit TLS (IMAPS, port 993)
	IMAPSecurityStartTLS = "starttls" // STARTTLS on a plain connection (port 143)
	IMAPSecurityNone     = "none"     // plaintext (local/test use only)
)

// --- Agent defaults ---
//
// The values live in app/modules/agent (which cannot import this package
// without a cycle); they are re-exported here so the rest of the app has one
// place to look.

const (
	// DefaultAgentProvider is the agent CLI used for analysis when none is set.
	DefaultAgentProvider = agent.DefaultProvider
	// Output markers the agent must emit (rare enough not to appear in prose).
	AgentReportBegin = agent.ReportBegin
	AgentReportEnd   = agent.ReportEnd
	AgentMetaBegin   = agent.MetaBegin
	AgentMetaEnd     = agent.MetaEnd
	// AgentMaxEvidenceSamples bounds how many sample blocks (one per bounce
	// pattern) the prompt shows; the samples of the other patterns are in
	// their evidence files, named in the prompt.
	AgentMaxEvidenceSamples = agent.MaxEvidenceSamples
	// DefaultAgentKeepDays is how many days the workspace directory of an
	// analysis run (data/agent/<address>/<group_key>/<report_id>/) is kept;
	// older run directories are removed by the daily cleanup job. The setting
	// agent_keep_days overrides it within MinAgentKeepDays..MaxAgentKeepDays.
	DefaultAgentKeepDays = 30
	MinAgentKeepDays     = 1
	MaxAgentKeepDays     = 365
)

// --- Mail retention defaults ---

const (
	// DefaultMailKeepDays is how many days a fetched mail is kept, counted
	// from the date of the mail (messages.date: the Date header, else
	// INTERNALDATE, else the fetch time). Older mails are removed from the
	// mailbox directory (.eml and body section files) and from the index by
	// the daily cleanup job (mailengine.PruneMailbox); the groups they
	// belonged to are recounted and a group left empty is deleted with its
	// reports. The setting mail_keep_days overrides it within
	// MinMailKeepDays..MaxMailKeepDays.
	DefaultMailKeepDays = 180
	MinMailKeepDays     = 1
	MaxMailKeepDays     = 3650
	// DefaultCleanupTime is the local time of the daily cleanup job; the
	// setting cleanup_time overrides it.
	DefaultCleanupTime = "02:00"
)

// --- Job kinds and statuses (jobs table) ---

const (
	JobKindSync       = "sync"        // fetch, then group, then queue analysis (one mailbox; NULL expands to every enabled mailbox)
	JobKindFetch      = "fetch"       // download new mail and index it (no classification)
	JobKindGroup      = "group"       // classify and group the messages not grouped yet, then queue analysis
	JobKindAnalyze    = "analyze"     // run the agent on groups that need it
	JobKindReindex    = "reindex"     // rebuild the index from the raw files (fetch-equivalent + full grouping)
	JobKindReclassify = "reclassify"  // re-run classification and grouping over every message
	JobKindNotify     = "notify"      // send the alert summary mail to the notification recipients
	JobKindCleanup    = "cleanup"     // apply the retentions: server mails, local mails, stale temporary files, agent run directories, job history (one mailbox; NULL expands to every mailbox)
	JobKindProbeSend  = "probe_send"  // send a probe mail from every participating mailbox to the probe server (no mailbox; target: the send slot)
	JobKindProbeCheck = "probe_check" // look for the probe mails not arrived yet on the probe server and judge delays and failures (no mailbox)

	JobStatusQueued   = "queued"
	JobStatusRunning  = "running"
	JobStatusDone     = "done"
	JobStatusError    = "error"
	JobStatusCanceled = "canceled"
)

// --- Group states (per-mailbox groups.state) ---

const (
	GroupStateOpen     = "open"
	GroupStateResolved = "resolved"
	GroupStateIgnored  = "ignored"
)

// --- Group categories (per-mailbox groups.category) ---
//
// A category is the kind of problem a bounce group represents and decides the
// unit an administrator acts on (groups.unit_value), the party that decides
// the outcome (groups.authority) and whether the group is actionable by the
// mail administrator at all (groups.actionable). The rules that map a bounce
// to a category live in app/modules/mailengine/category.go.

const (
	// Actionable by the mail administrator (shown in Alerts).
	CategoryIPBlocked          = "ip_blocked"           // unit: sending IP, authority: blacklist or recipient domain
	CategoryRateLimited        = "rate_limited"         // unit: sending IP/server, authority: recipient domain
	CategorySenderBlocked      = "sender_blocked"       // unit: sender address, authority: recipient domain
	CategoryAuthFailure        = "auth_failure"         // unit: sending domain (SPF/DKIM/DMARC/PTR), authority: recipient domain
	CategoryContentRejected    = "content_rejected"     // unit: sender address, authority: recipient domain
	CategoryMessageTooLarge    = "message_too_large"    // unit: sender address, authority: recipient domain
	CategoryServerConfig       = "server_config"        // unit: the sending side's reporting MTA, authority: recipient domain
	CategoryDNSResolverFailure = "dns_resolver_failure" // unit: the sending side's reporting MTA (its resolver failed on a name that exists)
	CategoryDNSLookupFailure   = "dns_lookup_failure"   // unit: recipient domain (a temporary DNS lookup failure not verified yet)
	CategoryUnknownFailure     = "unknown_failure"      // unit: recipient domain, authority: status code + diagnostic template
	// Not actionable by the mail administrator (recipient side; excluded from Alerts by default).
	CategoryUserUnknown        = "user_unknown"         // unit: recipient address
	CategoryMailboxFull        = "mailbox_full"         // unit: recipient address
	CategoryMailboxDisabled    = "mailbox_disabled"     // unit: recipient address
	CategoryDomainNotFound     = "domain_not_found"     // unit: recipient domain
	CategoryDNSFailureExternal = "dns_failure_external" // unit: recipient domain (its DNS or a third party's resolver failed)
	CategoryDeliveryDelay      = "delivery_delay"       // unit: recipient domain
	// DMARC (records of aggregate reports that failed DMARC; actionable, unit: the domain of the record).
	CategoryDMARCSPFMissing       = "dmarc_spf_missing"
	CategoryDMARCDKIMFailed       = "dmarc_dkim_failed"
	CategoryDMARCNotAuthenticated = "dmarc_not_authenticated"
)

// KnownCategories lists every group category (actionable ones first).
func KnownCategories() []string {
	return []string{
		CategoryIPBlocked, CategoryRateLimited, CategorySenderBlocked, CategoryAuthFailure, CategoryContentRejected,
		CategoryMessageTooLarge, CategoryServerConfig, CategoryDNSResolverFailure, CategoryDNSLookupFailure, CategoryUnknownFailure,
		CategoryDMARCSPFMissing, CategoryDMARCDKIMFailed, CategoryDMARCNotAuthenticated,
		CategoryUserUnknown, CategoryMailboxFull, CategoryMailboxDisabled, CategoryDomainNotFound, CategoryDNSFailureExternal,
		CategoryDeliveryDelay,
	}
}

// IsKnownCategory reports whether s is one of the group categories.
func IsKnownCategory(s string) bool {
	for _, c := range KnownCategories() {
		if c == s {
			return true
		}
	}
	return false
}

// --- Responsible parties (who should act on a group) ---

const (
	ResponsibleSender    = "sender"    // the sending-side mail administrator (sending server / sending domain)
	ResponsibleRecipient = "recipient" // the owner of the recipient address (list maintainer)
	ResponsibleDomain    = "domain"    // the recipient domain / its DNS or MX admin
	ResponsibleUnknown   = "unknown"
)

// --- Bounce kinds (per-mailbox messages.bounce_kind) ---

const (
	BounceKindFailed    = "failed"     // permanent failure (5.x.x)
	BounceKindDelayed   = "delayed"    // temporary failure / delay notice (4.x.x)
	BounceKindAutoReply = "auto_reply" // vacation / out-of-office
	BounceKindOther     = "other"      // daemon mail of another kind
	BounceKindReport    = "report"     // DMARC aggregate report
	BounceKindJunk      = "junk"       // phishing / spam (is_bounce = 0)
)

// --- Settings keys ---
//
// Keys of the settings table. A key is stored only when its value differs from
// the code default, so a later change to a default takes effect by itself.

const (
	// SettingSecretKey holds the master key (32 random bytes, hex encoded)
	// that encrypts the stored passwords and seals the Web session cookies.
	// It is generated on first use and is the one setting that is always
	// stored; the CLI never shows it and never lets it be set (secret.go).
	SettingSecretKey     = "secret_key"
	SettingWebListen     = "web_listen"
	SettingWebPort       = "web_port"
	SettingCheckTimes    = "check_times"
	SettingAgentProvider = "agent_provider"
	SettingAgentModel    = "agent_model" // model passed to the agent CLI ("" / absent = the CLI default)
	// SettingAgentReasoningEffort is the reasoning level passed to the agent
	// CLI ("" / absent = the setting of the CLI).
	SettingAgentReasoningEffort = "agent_reasoning_effort"
	SettingAgentEnabled         = "agent_enabled"   // "1" (default) or "0"
	SettingAgentKeepDays        = "agent_keep_days" // days an agent run directory is kept (DefaultAgentKeepDays)
	SettingMailKeepDays         = "mail_keep_days"  // days a fetched mail is kept (DefaultMailKeepDays)
	SettingCookieTTLHours       = "cookie_ttl_hours"
	SettingWorkers              = "workers"
	// Notification mail (SMTP) settings. The password is stored encrypted with
	// the master key (see secret.go).
	SettingSMTPHost        = "smtp_host"
	SettingSMTPPort        = "smtp_port"
	SettingSMTPSecurity    = "smtp_security" // ssl | starttls | none
	SettingSMTPUsername    = "smtp_username"
	SettingSMTPPasswordEnc = "smtp_password_enc"
	SettingSMTPFrom        = "smtp_from"
	SettingPublicBaseURL   = "public_base_url" // e.g. https://mailcare.example.com (links in mails)
	SettingNotifyEnabled   = "notify_enabled"  // "1" or "0" (default)
	SettingNotifyTime      = "notify_time"     // comma-separated HH:MM list, local (one time is a list of one)
	SettingNotifyInterval  = "notify_interval_days"
	SettingNotifyUserIDs   = "notify_user_ids" // comma-separated users.id
	SettingNotifyLastSent  = "notify_last_sent_at"
	// SettingRecheckDays holds the re-check days of the categories whose
	// value differs from the default (comma-separated "category=days"; see
	// mailengine.RecheckDays and ResolveRecheckDays).
	SettingRecheckDays = "recheck_days"
	// SettingCleanupTime is the local time (HH:MM) at which the scheduler
	// queues the daily cleanup job (DefaultCleanupTime).
	SettingCleanupTime = "cleanup_time"
	// SettingCleanupLastRunDate is the local date (YYYY-MM-DD) of the last
	// cleanup occurrence the scheduler queued (internal; see scheduler.go).
	SettingCleanupLastRunDate = "cleanup_last_run_date"
	// SettingAgentLimitedUntil is when the agent CLI's usage limit is lifted
	// (RFC 3339, internal): set when an analysis is refused by the limit and
	// the CLI named the time; until then no analysis launches the CLI, and
	// AgentLimitResumeMargin after it the scheduler queues the analysis of
	// the groups waiting for it (see scheduler.go).
	SettingAgentLimitedUntil = "agent_limited_until"
	// The sending sources beyond the SMTP hosts of the mailboxes
	// (newline-separated; absent = none): IPv4 addresses and CIDRs, IPv6
	// addresses and CIDRs, and host names. ResolveSendingSources adds the SMTP
	// hosts of the mailboxes to the host names.
	SettingSendingSourceIPv4  = "sending_source_ipv4"
	SettingSendingSourceIPv6  = "sending_source_ipv6"
	SettingSendingSourceHosts = "sending_source_hosts"
	// Verification of DNS lookup failures by looking the name up again.
	SettingDNSCheckEnabled = "dns_check_enabled" // "1" (default) or "0"
	// SettingDNSCheckResolvers lists the resolver IPs queried
	// (newline-separated). An absent row means DefaultDNSCheckResolvers; a
	// row holding "" is an explicitly empty list.
	SettingDNSCheckResolvers          = "dns_check_resolvers"
	SettingDNSCheckSystemResolverOnly = "dns_check_system_resolver_only" // "1" or "0" (default)
	// Delivery probe (probe.go): the probe server (IMAP) the probe mails are
	// sent to, the send times and the two thresholds. The password is stored
	// encrypted with the master key (see secret.go).
	SettingProbeEnabled        = "probe_enabled" // "1" or "0" (default)
	SettingProbeAddress        = "probe_address" // the recipient address of the probe mails
	SettingProbeIMAPHost       = "probe_imap_host"
	SettingProbeIMAPPort       = "probe_imap_port"
	SettingProbeIMAPSecurity   = "probe_imap_security" // ssl | starttls | none
	SettingProbeIMAPUsername   = "probe_imap_username"
	SettingProbeIMAPPassword   = "probe_imap_password_enc"
	SettingProbeIMAPFolder     = "probe_imap_folder"
	SettingProbeSendTimes      = "probe_send_times"      // comma-separated HH:MM (DefaultProbeSendTimes)
	SettingProbeDelayMinutes   = "probe_delay_minutes"   // minutes without arrival that make a probe mail delayed
	SettingProbeFailureMinutes = "probe_failure_minutes" // minutes without arrival that make a probe mail a failure
	// SettingProbeLastSendAt is when the latest probe send finished (RFC
	// 3339, internal): the arrival checks are counted from it.
	SettingProbeLastSendAt = "probe_last_send_at"
	// SettingProbeNextCheckAt is when the scheduler queues the next arrival
	// check (RFC 3339, internal; absent = no check pending).
	SettingProbeNextCheckAt = "probe_next_check_at"
	// SettingProbeLastCheckAt and SettingProbeLastCheckError record the
	// outcome of the latest arrival check (internal).
	SettingProbeLastCheckAt    = "probe_last_check_at"
	SettingProbeLastCheckError = "probe_last_check_error"
	// SettingProbeLastDeleteError is why the latest arrival check could not
	// delete the verified probe mails from the probe server (internal; ""
	// once a check deletes them).
	SettingProbeLastDeleteError = "probe_last_delete_error"
	// SettingProbeCheckFailures counts the arrival checks that failed in a
	// row and SettingProbeCheckFailingSince is when the first of them ran
	// (internal; both removed by the next successful check): the retries
	// back off, and SettingProbeUnreadableNotifiedAt records the one mail
	// sent once the probe server stayed unreadable for the failure threshold.
	SettingProbeCheckFailures        = "probe_check_failures"
	SettingProbeCheckFailingSince    = "probe_check_failing_since"
	SettingProbeUnreadableNotifiedAt = "probe_unreadable_notified_at"
	// SettingProbeWideSweepAt is the time of the daily sweep of older
	// versions; no longer used, a check that read the probe server removes
	// it.
	SettingProbeWideSweepAt = "probe_wide_sweep_at"
	// SettingProbeSubjectTag is the identifier at the start of the subject
	// of the probe mails ("[<identifier>:<token>]"; DefaultProbeSubjectTag).
	SettingProbeSubjectTag = "probe_subject_tag"
)

// --- Delivery probe defaults ---

const (
	// DefaultProbeSendTimes is the default schedule of the probe mails
	// (local time, HH:MM).
	DefaultProbeSendTimes = "07:00,11:00,15:00,19:00"
	// DefaultProbeDelayMinutes is how long a probe mail may take to arrive
	// before it counts as delayed; while probe mails are missing the arrival
	// is checked every this many minutes after the latest send, up to the
	// failure threshold.
	DefaultProbeDelayMinutes = 15
	// DefaultProbeFailureMinutes is how long a probe mail may take to arrive
	// before it counts as a failure; after that the arrival is checked every
	// this many minutes.
	DefaultProbeFailureMinutes = 60
	MinProbeMinutes            = 1
	MaxProbeMinutes            = 7 * 24 * 60
	// ProbeCheckCoalesce is how close the moments missing probe mails reach
	// their next level may be to be checked together (probeLevelCheckAt).
	ProbeCheckCoalesce = time.Minute
	// ProbeCheckRetryInterval is the first retry of an arrival check that
	// could not read the probe server; every further failure doubles it, up
	// to the failure threshold.
	ProbeCheckRetryInterval = 10 * time.Minute
	// ProbeLeftoverMinAge is how long a probe mail that is not a verified
	// one of this MailCare stays on the probe server at least (another
	// MailCare sharing the server may still look for it).
	ProbeLeftoverMinAge = 24 * time.Hour
	// DefaultProbeSubjectTag is the default identifier of the probe mails.
	DefaultProbeSubjectTag = "MC"
	// MaxProbeSendTimes and MinProbeSendGapMinutes bound the send times: at
	// most this many a day, at least this many minutes apart.
	MaxProbeSendTimes      = 24
	MinProbeSendGapMinutes = 15
	// MinProbeRecordKeepDays is the shortest time the daily cleanup keeps the
	// probe mail records, even when mail_keep_days is shorter.
	MinProbeRecordKeepDays = 7
	// ProbeHeader carries the token of a probe mail as a header as well.
	ProbeHeader = "X-MailCare-Probe"
	// ProbeSubjectMark is the part of the subject form of the probe mails
	// ("[<identifier>:<token>] Probe ...", probeSubject) every check lists
	// the probe mailbox by, whatever the identifier.
	ProbeSubjectMark = "] Probe "
)

// --- Notification defaults ---

const (
	DefaultSMTPPort     = 587
	DefaultSMTPSecurity = IMAPSecurityStartTLS // the security values are shared with IMAP
	// DefaultNotifyTimes is the notification schedule when notify_time is
	// not set (local time, HH:MM, as stored; the owner's value).
	DefaultNotifyTimes        = "09:00,17:00"
	DefaultNotifyIntervalDays = 1
	MinNotifyIntervalDays     = 1
	MaxNotifyIntervalDays     = 7
	// NotifyMailSubjectPrefix starts the subject of every notification mail.
	NotifyMailSubjectPrefix = "[MailCare] "
)

// --- DNS lookup failure verification defaults ---

const (
	// MaxSendingSourceEntries bounds each of the sending source lists
	// (sending_source_ipv4 / sending_source_ipv6 / sending_source_hosts), counted after
	// normalization and de-duplication.
	MaxSendingSourceEntries = 500
	// MaxDNSCheckResolvers bounds dns_check_resolvers: every resolver gets a
	// control query and the queries of every name verified (each within
	// the per-query timeout), and a handful of public resolvers already
	// gives independent answers (a value of the settings screen).
	MaxDNSCheckResolvers = 10
	// DefaultDNSCheckEnabled is whether DNS lookup failures are verified when
	// dns_check_enabled is not set.
	DefaultDNSCheckEnabled = true
	// DefaultDNSCheckSystemResolverOnly is whether only the OS resolver is
	// used when dns_check_system_resolver_only is not set.
	DefaultDNSCheckSystemResolverOnly = false
)

// DefaultDNSCheckResolvers returns the resolvers queried when
// dns_check_resolvers is not set. Go has no slice constants, so every call
// returns a fresh slice that the caller may change.
func DefaultDNSCheckResolvers() []string {
	return []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}
}

// --- Job workers ---

const (
	// DefaultWorkers is the number of jobs that run at once. Jobs that touch
	// the same IMAP server, the same mailbox index or the agent CLI are
	// serialized regardless of this number (see app/modules/jobs.go).
	DefaultWorkers = 2
	MaxWorkers     = 16
)

// --- Polling / timing intervals ---

const (
	// JobPollInterval paces the job worker between claim attempts.
	JobPollInterval = 2 * time.Second
	// SchedulerTick is how often the scheduler re-evaluates the check times.
	SchedulerTick = 30 * time.Second
	// AgentLimitResumeMargin is how long after the announced end of the
	// agent's usage limit the scheduler waits before it queues the analysis
	// of the waiting groups (the clock of the CLI and the end the limit states may differ).
	AgentLimitResumeMargin = 10 * time.Minute
	// CLIJobPollInterval is how often the CLI polls a job it submitted to a
	// running server.
	CLIJobPollInterval = time.Second
)
