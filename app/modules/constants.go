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
	// without "remember me": a browser-session cookie (no Max-Age) whose
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
	// ReadHeaderTimeout bounds how long a client may take to send its request
	// headers (protects the server from slowloris-style connections).
	ReadHeaderTimeout = 10 * time.Second
	// HTTPReadTimeout bounds the whole request read (headers and body).
	HTTPReadTimeout = 30 * time.Second
	// HTTPWriteTimeout bounds the response write; no endpoint waits for a
	// job, so a response never takes long on purpose.
	HTTPWriteTimeout = 5 * time.Minute
	// HTTPIdleTimeout closes keep-alive connections idle for that long.
	HTTPIdleTimeout = 2 * time.Minute
	// ShutdownGraceTimeout bounds how long a graceful shutdown waits for the HTTP
	// server to drain in-flight requests before giving up.
	ShutdownGraceTimeout  = 5 * time.Second
	DefaultCookieTTLHours = 720 // 30 days
)

// --- Mail check defaults ---

const (
	// DefaultCheckTimes is the default daily schedule (local time, HH:MM).
	DefaultCheckTimes = "06:00,12:00,18:00"
	// DefaultInitialDays is how far back the first check of a mailbox looks.
	DefaultInitialDays = 90
	// DefaultRecentDays is how far back every later check looks.
	DefaultRecentDays = 30
	// DefaultIMAPPort is the IMAPS port used when none is given.
	DefaultIMAPPort = 993
	// IMAPTimeout bounds one IMAP network operation.
	IMAPTimeout = 60 * time.Second
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
	// AgentTimeout is a safety-net backstop on one agent CLI run.
	AgentTimeout = agent.Timeout
	// Output markers the agent must emit (rare enough not to appear in prose).
	AgentReportBegin = agent.ReportBegin
	AgentReportEnd   = agent.ReportEnd
	AgentMetaBegin   = agent.MetaBegin
	AgentMetaEnd     = agent.MetaEnd
	// AgentMaxSampleMessages bounds how many message files are listed in one
	// prompt (the newest ones are chosen).
	AgentMaxSampleMessages = agent.MaxSampleMessages
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
)

// --- Job kinds and statuses (jobs table) ---

const (
	JobKindSync       = "sync"       // fetch, then group, then queue analysis (one mailbox; NULL expands to every enabled mailbox)
	JobKindFetch      = "fetch"      // download new mail and index it (no classification)
	JobKindGroup      = "group"      // classify and group the messages not grouped yet, then queue analysis
	JobKindAnalyze    = "analyze"    // run the agent on groups that need it
	JobKindReindex    = "reindex"    // rebuild the index from the raw files (fetch-equivalent + full grouping)
	JobKindReclassify = "reclassify" // re-run classification and grouping over every message
	JobKindNotify     = "notify"     // send the alert summary mail to the notification recipients
	JobKindCleanup    = "cleanup"    // remove the mails older than mail_keep_days and the agent run directories older than agent_keep_days (one mailbox; NULL expands to every mailbox)

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
	CategoryIPBlocked       = "ip_blocked"        // unit: sending IP, authority: blacklist or recipient domain
	CategoryRateLimited     = "rate_limited"      // unit: sending IP/server, authority: recipient domain
	CategorySenderBlocked   = "sender_blocked"    // unit: sender address, authority: recipient domain
	CategoryAuthFailure     = "auth_failure"      // unit: sending domain (SPF/DKIM/DMARC/PTR), authority: recipient domain
	CategoryContentRejected = "content_rejected"  // unit: sender address, authority: recipient domain
	CategoryMessageTooLarge = "message_too_large" // unit: sender address, authority: recipient domain
	CategoryServerConfig    = "server_config"     // unit: our reporting MTA, authority: recipient domain
	CategoryUnknownFailure  = "unknown_failure"   // unit: recipient domain, authority: status code + diagnostic template
	// Not actionable by the mail administrator (recipient side; excluded from Alerts by default).
	CategoryUserUnknown     = "user_unknown"     // unit: recipient address
	CategoryMailboxFull     = "mailbox_full"     // unit: recipient address
	CategoryMailboxDisabled = "mailbox_disabled" // unit: recipient address
	CategoryDomainNotFound  = "domain_not_found" // unit: recipient domain
	CategoryDeliveryDelay   = "delivery_delay"   // unit: recipient domain
)

// KnownCategories lists every group category (actionable ones first).
func KnownCategories() []string {
	return []string{
		CategoryIPBlocked, CategoryRateLimited, CategorySenderBlocked, CategoryAuthFailure, CategoryContentRejected,
		CategoryMessageTooLarge, CategoryServerConfig, CategoryUnknownFailure,
		CategoryUserUnknown, CategoryMailboxFull, CategoryMailboxDisabled, CategoryDomainNotFound, CategoryDeliveryDelay,
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
	ResponsibleSender    = "sender"    // our mail server / sending domain admin
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
)

// --- Settings keys ---
//
// Keys of the settings table. A key is stored only when its value differs from
// the code default, so a later change to a default takes effect on its own.

const (
	// SettingSecretKey holds the master key (32 random bytes, hex encoded)
	// that encrypts the stored passwords and seals the Web session cookies.
	// It is generated on first use and is the one setting that is always
	// stored; the CLI never shows it and never lets it be set (secret.go).
	SettingSecretKey      = "secret_key"
	SettingWebListen      = "web_listen"
	SettingWebPort        = "web_port"
	SettingCheckTimes     = "check_times"
	SettingAgentProvider  = "agent_provider"
	SettingAgentEnabled   = "agent_enabled"   // "1" (default) or "0"
	SettingAgentKeepDays  = "agent_keep_days" // days an agent run directory is kept (DefaultAgentKeepDays)
	SettingMailKeepDays   = "mail_keep_days"  // days a fetched mail is kept (DefaultMailKeepDays)
	SettingCookieTTLHours = "cookie_ttl_hours"
	SettingWorkers        = "workers"
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
	SettingNotifyTime      = "notify_time"     // HH:MM local
	SettingNotifyInterval  = "notify_interval_days"
	SettingNotifyUserIDs   = "notify_user_ids" // comma-separated users.id
	SettingNotifyLastSent  = "notify_last_sent_at"
	// SettingCleanupLastRunDate is the local date (YYYY-MM-DD) on which the
	// scheduler last queued the daily cleanup job (internal; see scheduler.go).
	SettingCleanupLastRunDate = "cleanup_last_run_date"
)

// --- Notification defaults ---

const (
	DefaultSMTPPort           = 587
	DefaultSMTPSecurity       = IMAPSecurityStartTLS // the security values are shared with IMAP
	DefaultNotifyTime         = "09:00"
	DefaultNotifyIntervalDays = 1
	MinNotifyIntervalDays     = 1
	MaxNotifyIntervalDays     = 7
	// SMTPTimeout bounds one SMTP session.
	SMTPTimeout = 60 * time.Second
	// NotifyMailSubjectPrefix starts the subject of every notification mail.
	NotifyMailSubjectPrefix = "[MailCare] "
)

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
	// CLIJobPollInterval is how often the CLI polls a job it submitted to a
	// running server.
	CLIJobPollInterval = time.Second
)
