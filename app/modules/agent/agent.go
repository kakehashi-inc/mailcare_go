// Package agent runs an external agent CLI (codex today; more providers can be
// registered) over one bounce group to produce a cause analysis and a
// recommended action, and stores the result as an agent report in the
// per-mailbox index.
//
// Provider architecture (mirrors ai_agent_bridge): every CLI is one Provider
// implementation living in its own provider_<name>.go file and registered at
// init time. Nothing else branches on a provider name. Adding a provider means
// adding provider_<name>.go and, when it needs a workspace skeleton,
// templates/agent/<name>/ (embedded from package main). See
// templates/agent/README.md for the step-by-step recipe.
//
// Workspace layout inside agentRoot (data/agent): every analysis run gets
// its own directory named after its agent_reports row, so the prompt and the
// transcript of each run stay together and a failed run never touches the
// files of an earlier successful one. The CLI runs with that directory as
// its working directory.
//
//	<address>/<group_key>/<report_id>/PROMPT.md   the prompt fed to the CLI
//	<address>/<group_key>/<report_id>/RESULT.log  verdict + full CLI transcript of the run
//	<address>/<group_key>/<report_id>/REPORT.md   the extracted report (Markdown; successful runs only)
//	<address>/<group_key>/<report_id>/AGENTS.md   copied from templates/agent/<provider>/ (if present)
//
// Run directories are removed by CleanupWorkspaces (cleanup.go) once they are
// older than the retention the caller passes (setting agent_keep_days); a
// group directory left empty goes with them.
//
// This package must not import app/modules (app/modules imports this package),
// so the handful of values it shares with app/modules/constants.go are declared
// here as exported constants with identical literals.
package agent

import (
	"database/sql"
	"io/fs"
	"time"
)

// Values shared with app/modules/constants.go (kept identical there).
const (
	// DefaultProvider is the agent CLI used when AnalyzeInput.Provider is "".
	DefaultProvider = "codex"
	// Timeout is a safety-net backstop on one agent CLI run.
	Timeout = 30 * time.Minute
	// PromptFileName, ResultFileName and ReportFileName are written inside the
	// workspace directory of a run.
	PromptFileName = "PROMPT.md"
	ResultFileName = "RESULT.log"
	ReportFileName = "REPORT.md"
	// Output markers the agent must emit (rare enough not to appear in prose).
	ReportBegin = "<<<MLC:REPORT>>>"
	ReportEnd   = "<<<MLC:/REPORT>>>"
	MetaBegin   = "<<<MLC:META>>>"
	MetaEnd     = "<<<MLC:/META>>>"
	// MaxSampleMessages bounds how many message files are listed in one prompt
	// (the newest ones are chosen).
	MaxSampleMessages = 20
	// MaxPromptListItems bounds how many distinct recipients, remote IPs and
	// remote MTAs the group summary of the prompt lists (the rest is counted).
	MaxPromptListItems = 30
	// MaxSummaryRunes bounds the summary derived from the report body when the
	// META block carries none.
	MaxSummaryRunes = 200
	// TemplatesDirName is the top-level directory inside AnalyzeInput.TemplatesFS
	// that holds one sub-directory per provider.
	TemplatesDirName = "templates/agent"
)

// Responsible party values accepted in META (same literals as app/modules).
const (
	ResponsibleSender    = "sender"
	ResponsibleRecipient = "recipient"
	ResponsibleDomain    = "domain"
	ResponsibleUnknown   = "unknown"
)

// Severity values accepted in META.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// Provider is everything an agent CLI must provide.
type Provider interface {
	// Name is the stable identifier stored in settings and reports ("codex").
	Name() string
	// Label is the display name ("Codex").
	Label() string
	// Command is the non-interactive, read-only launch argv. Placeholders:
	// {prompt} (inline prompt argv element), {prompt_file} (path to PROMPT.md),
	// {cwd} (workspace directory). Without {prompt}/{prompt_file} the prompt is
	// fed on stdin.
	Command() []string
}

// ProviderStatus describes a provider and whether its CLI is on PATH.
type ProviderStatus struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
}

// AnalyzeInput carries everything AnalyzeGroup needs.
type AnalyzeInput struct {
	MailsRoot   string  // data/mails
	AgentRoot   string  // data/agent (the run directory is created below it)
	TemplatesFS fs.FS   // root contains "templates/agent/<provider>/**" (may be nil)
	Address     string  // the mailbox address
	Index       *sql.DB // the opened per-mailbox index
	GroupKey    string
	Provider    string // provider name; "" = DefaultProvider
	Language    string // language for the report ("ja" default)
}
