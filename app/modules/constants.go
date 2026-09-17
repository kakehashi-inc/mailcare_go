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
	// raw mail files and the per-mailbox index databases:
	//   data/mails/<address>/<message_key>.{eml,txt,html,json}
	//   data/mails/<address>.sqlite
	MailsDirName = "mails"
	// AgentDirName is the directory (inside the data directory) that holds the
	// agent workspaces: data/agent/<address>/<group_key>/{PROMPT.md,RESULT.log,REPORT.md}
	AgentDirName = "agent"
	// SecretKeyFileName holds the master key used to encrypt IMAP passwords at
	// rest and to seal Web session cookies. Created on first use (0600).
	SecretKeyFileName = "mailcare.key"
)

// --- Token / session ---

const (
	// TokenPrefix is prepended to the random part of every login token.
	TokenPrefix = "mlc_"
	// TokenRandomBytes is the number of random bytes (hex-encoded) in a token.
	TokenRandomBytes = 20 // -> 40 hex chars, full token "mlc_" + 40 = 44 chars
	// CookieName is the Web session cookie.
	CookieName = "mlc_session"
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
	// MaxMessageSize is the largest message body fetched from IMAP; larger
	// messages are skipped (bounce notices are small).
	MaxMessageSize = 20 * 1024 * 1024
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
	// PromptFileName / ResultFileName / ReportFileName are written inside the
	// agent workspace of a group.
	PromptFileName = agent.PromptFileName
	ResultFileName = agent.ResultFileName
	ReportFileName = agent.ReportFileName
	// Output markers the agent must emit (rare enough not to appear in prose).
	AgentReportBegin = agent.ReportBegin
	AgentReportEnd   = agent.ReportEnd
	AgentMetaBegin   = agent.MetaBegin
	AgentMetaEnd     = agent.MetaEnd
	// AgentMaxSampleMessages bounds how many message files are listed in one
	// prompt (the newest ones are chosen).
	AgentMaxSampleMessages = agent.MaxSampleMessages
)

// --- Job kinds and statuses (jobs table) ---

const (
	JobKindCheck      = "check"      // fetch new mail and index it
	JobKindReindex    = "reindex"    // rebuild the index from the raw files
	JobKindReclassify = "reclassify" // re-run bounce detection and grouping only
	JobKindAnalyze    = "analyze"    // run the agent on groups

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
	SettingWebListen      = "web_listen"
	SettingWebPort        = "web_port"
	SettingCheckTimes     = "check_times"
	SettingAgentProvider  = "agent_provider"
	SettingAgentEnabled   = "agent_enabled" // "1" (default) or "0"
	SettingCookieTTLHours = "cookie_ttl_hours"
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
