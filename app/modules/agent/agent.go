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
// the provider guide in the Documents directory for the step-by-step recipe.
//
// Workspace layout inside agentRoot (data/agent): every analysis run gets
// its own directory named after its agent_reports row, so the prompt and the
// transcript of each run stay together and a failed run never touches the
// files of an earlier successful one. The CLI runs with that directory as
// its working directory and reads nothing but the evidence files in it; the
// prompt goes in on stdin and is written to PROMPT.md only after the CLI has
// ended, so the agent never finds a copy of it to read again.
//
//	<address>/<group_key>/<report_id>/AGENTS.md              copied from templates/agent/<provider>/ (if present)
//	<address>/<group_key>/<report_id>/evidence/<key>.txt     full evidence of one sample notice (see evidence.go)
//	<address>/<group_key>/<report_id>/PROMPT.md              the prompt fed to the CLI (written after the run)
//	<address>/<group_key>/<report_id>/RESULT.log             verdict + full CLI transcript of the run
//	<address>/<group_key>/<report_id>/REPORT.md              the extracted report (Markdown; successful runs only)
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
	// EvidenceDirName is the directory inside the workspace that holds one
	// evidence file per sample notice.
	EvidenceDirName = "evidence"
	// MaxPromptPatterns bounds how many bounce patterns the PATTERNS section
	// lists (the most frequent first; the rest is counted).
	MaxPromptPatterns = 20
	// MaxEvidenceSamples bounds how many sample notices (one per pattern)
	// the EVIDENCE section carries.
	MaxEvidenceSamples = 5
	// MaxExcerptLines bounds the body excerpt of one sample in the prompt (half
	// of it when the diagnostic comes from the delivery-status part); a
	// longer body is cut around the diagnostic and marked [TRUNCATED].
	MaxExcerptLines = 40
	// MaxEvidenceReads is how many evidence files the agent may read in one
	// run, and only under the conditions the prompt names.
	MaxEvidenceReads = 3
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

// Confidence values accepted in META: how firmly the notices establish the
// cause.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
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

// ProviderStatus describes a provider, whether its CLI is on PATH and, for a
// provider that accepts a model (ModelSelector), the option that names it
// and the models it is known to accept (for reference only), and, for a
// provider that accepts a reasoning level (ReasoningSelector), the option
// that carries it and the levels each known model accepts (what saving a
// level is checked against, see CheckReasoningEffort).
type ProviderStatus struct {
	Name            string              `json:"name"`
	Label           string              `json:"label"`
	Available       bool                `json:"available"`
	ModelOption     string              `json:"model_option"`
	Models          []string            `json:"models"`
	ReasoningOption string              `json:"reasoning_option"`
	ReasoningLevels map[string][]string `json:"reasoning_levels"`
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
	Model       string // model passed to a provider that supports one (ModelSelector); "" = the CLI default
	// ReasoningEffort is the reasoning level passed to a provider that
	// supports one (ReasoningSelector); "" = the CLI's own setting.
	ReasoningEffort string
	Language        string // language for the report ("ja" default)
}
