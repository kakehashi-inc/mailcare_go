package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// AnalyzeGroup prepares the evidence of the group (its bounce patterns and
// one sample notice per pattern, see evidence.go) in a fresh workspace
// directory made for this run, builds the prompt around it, runs the
// provider CLI there, parses the REPORT/META blocks and stores an agent
// report row with the usage the CLI reported. It returns the stored report
// (status completed or error) and an error only when nothing could be
// recorded.
//
// Success is decided by whether a usable REPORT block was extracted from the
// transcript after the echoed prompt is removed (see StripPromptEcho and
// ValidateOutput: no template placeholders, not too short, no placeholder
// summary). The CLI exit code is not a reliable signal (codex exits non-zero on
// fine runs), so it is ignored. A launch failure (CLI missing), a timeout, a
// cancellation, a usage/rate limit ("usage limit reached (retry after ...)"),
// a missing REPORT block or an unusable report (template placeholders, too
// short, or secret-like content: see ValidateOutput) fail the run and the
// reason is stored on the report. A failed run writes no REPORT.md and
// leaves the previous completed report row (and the directory of that run)
// untouched; its RESULT.log keeps the full transcript. A failure by a usage
// limit or a cancellation leaves needs_analysis alone (the group keeps
// waiting); any other failure settles the group like a success does
// (the patterns are recorded, the flag cleared): it is analyzed again only
// when a bounce of a new pattern arrives. A completed report
// records the patterns it covered; the group stays flagged only when a
// bounce of another pattern arrived meanwhile.
func AnalyzeGroup(ctx context.Context, in AnalyzeInput, progress func(string)) (*models.AgentReport, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if in.Index == nil {
		return nil, errors.New("agent: no index database")
	}
	if in.GroupKey == "" {
		return nil, errors.New("agent: empty group key")
	}
	if in.AgentRoot == "" {
		return nil, errors.New("agent: no workspace root")
	}
	providerName := in.Provider
	if providerName == "" {
		providerName = DefaultProvider
	}

	group, err := models.GetGroup(in.Index, in.GroupKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("agent: group %s not found", in.GroupKey)
		}
		return nil, fmt.Errorf("agent: load group: %w", err)
	}

	report := &models.AgentReport{GroupKey: group.GroupKey, Provider: providerName, MessageCount: group.MessageCount}
	if err := models.InsertAgentReport(in.Index, report); err != nil {
		return nil, fmt.Errorf("agent: record report: %w", err)
	}

	// settle ends the waiting of the group: it records the patterns the
	// run dealt with and sets needs_analysis to whether a bounce of another
	// pattern arrived meanwhile (always false for a recipient-side group).
	settle := func(patterns []string) {
		if err := models.InsertAgentReportPatterns(in.Index, report.ID, patterns); err != nil {
			progress("warning: could not record the patterns: " + err.Error())
		}
		needs := false
		if group.Actionable {
			var err error
			if needs, err = models.GroupHasUncoveredPattern(in.Index, group.GroupKey); err != nil {
				progress("warning: could not check the covered patterns: " + err.Error())
				needs = false
			}
		}
		if err := models.SetGroupNeedsAnalysis(in.Index, group.GroupKey, needs); err != nil {
			progress("warning: could not update needs_analysis: " + err.Error())
		}
	}

	// A usage limit or a cancellation says nothing about the group: it keeps
	// waiting and a later run tries again. Any other failure means the group
	// cannot be analyzed as it is: the run settles it (like a success, but
	// without a report), so it is not analyzed again until a bounce of a
	// new pattern arrives.
	fail := func(reason error) (*models.AgentReport, error) {
		if ctx.Err() != nil && !strings.HasPrefix(reason.Error(), canceledPrefix) {
			// Stopped by the caller, whatever went wrong on the way.
			reason = fmt.Errorf("%s: %v", canceledPrefix, reason)
		}
		progress("analysis failed: " + reason.Error())
		report.Status = "error"
		report.ErrorMessage = reason.Error()
		if err := models.FailAgentReport(in.Index, report.ID, reason.Error()); err != nil {
			return report, fmt.Errorf("agent: record failure: %w", err)
		}
		if !FailureSettles(reason.Error()) {
			return report, nil
		}
		patterns, err := models.ListGroupPatternKeys(in.Index, group.GroupKey)
		if err != nil {
			progress("warning: could not list the patterns: " + err.Error())
			return report, nil
		}
		progress("the group is not analyzed again until a bounce of a new pattern arrives")
		settle(patterns)
		return report, nil
	}

	provider, ok := lookupProvider(providerName)
	if !ok {
		return fail(fmt.Errorf("unknown agent provider %q", providerName))
	}

	// 1. Workspace: one directory per run, named after the report row.
	dir := RunDir(in.AgentRoot, in.Address, group.GroupKey, report.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(fmt.Errorf("create workspace: %w", err))
	}
	if err := copyTemplates(in.TemplatesFS, provider.Name(), dir); err != nil {
		return fail(fmt.Errorf("copy templates: %w", err))
	}

	// 2. Evidence: the patterns of the group and one sample notice per
	// pattern. When the latest completed report covered some of the
	// patterns, only the new ones are sampled (an update).
	progress("building evidence for " + group.GroupKey)
	stats, err := models.GroupStats(in.Index, group.GroupKey)
	if err != nil {
		return fail(fmt.Errorf("load group stats: %w", err))
	}
	bounces, err := models.ListGroupBounces(in.Index, group.GroupKey)
	if err != nil {
		return fail(fmt.Errorf("list bounces: %w", err))
	}
	previous, covered, err := previousCoverage(in.Index, group.GroupKey)
	if err != nil {
		return fail(fmt.Errorf("load previous report: %w", err))
	}
	ev := BuildEvidence(EvidenceInput{MailsRoot: in.MailsRoot, Address: in.Address, Group: group, Bounces: bounces, Covered: covered})
	if err := writeEvidenceFiles(dir, ev); err != nil {
		return fail(fmt.Errorf("write evidence: %w", err))
	}
	if !ev.Update {
		previous = nil
	}

	// 3. Prompt. It goes in on stdin and is written to PROMPT.md after the
	// run (only a provider that reads it from {prompt_file} gets the file
	// before), so the agent finds no copy of it to read again.
	promptText := BuildPrompt(PromptInput{
		Address:     in.Address,
		Language:    in.Language,
		TemplatesFS: in.TemplatesFS,
		Group:       group,
		Stats:       stats,
		Evidence:    ev,
		Previous:    previous,
	})
	// The progress names the run directory relative to the address (its
	// absolute path would expose the server's layout to every Web user).
	progress("workspace " + group.GroupKey + "/" + strconv.FormatInt(report.ID, 10))
	promptFile := filepath.Join(dir, PromptFileName)
	writePrompt := func() error { return os.WriteFile(promptFile, []byte(promptText), 0o600) }
	promptWritten := false
	if usesPromptFile(providerCommand(provider, in.Model, in.ReasoningEffort)) {
		if err := writePrompt(); err != nil {
			return fail(fmt.Errorf("write prompt: %w", err))
		}
		promptWritten = true
	}

	// 4. Run the CLI.
	progress(runLine(provider, in.Model, in.ReasoningEffort, ev))
	runCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	out, runErr := runProvider(runCtx, provider, in.Model, in.ReasoningEffort, dir, promptText, promptFile)
	if !promptWritten {
		if err := writePrompt(); err != nil {
			progress("warning: could not write " + PromptFileName + ": " + err.Error())
		}
	}
	// The echoed prompt is dropped before parsing so that its markers and
	// placeholders (and its wording, for the rate-limit markers) are ignored.
	answer := StripPromptEcho(out, promptText)
	parsed := ParseOutput(answer)
	usage := parseUsage(provider, out, answer)
	if err := models.UpdateAgentReportUsage(in.Index, report.ID, usage); err != nil {
		progress("warning: could not record the usage: " + err.Error())
	}
	report.Model, report.ReasoningEffort = usage.Model, usage.ReasoningEffort
	report.TokensUsed, report.CommandCount = usage.TokensUsed, usage.CommandCount
	if line := usageLine(usage); line != "" {
		progress(line)
	}

	var exitErr *exec.ExitError
	launchErr := runErr != nil && !errors.As(runErr, &exitErr)
	var invalid error
	if parsed.ReportParsed {
		invalid = ValidateOutput(parsed)
	}
	success := parsed.ReportParsed && invalid == nil && !launchErr
	var failure error
	if !success {
		limit := detectRateLimit(provider, answer)
		switch {
		case launchErr:
			failure = fmt.Errorf("%s could not run: %v", provider.Label(), runErr)
		case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
			failure = fmt.Errorf("%s timed out after %s", provider.Label(), Timeout)
		case ctx.Err() != nil:
			failure = fmt.Errorf("%s: %v", canceledPrefix, ctx.Err())
		case limit.Limited:
			failure = errors.New(limit.ErrorMessage())
		case !parsed.ReportParsed:
			failure = fmt.Errorf("%s produced no report", provider.Label())
		default:
			// A REPORT block that still carries placeholders, is too short,
			// comes with a placeholder summary or contains secret-like content
			// must not replace a good report.
			failure = fmt.Errorf("%s produced an unusable report: %v", provider.Label(), invalid)
		}
	}

	// 5. Record the outcome.
	if err := os.WriteFile(filepath.Join(dir, ResultFileName), []byte(formatResultLog(failure, parsed, usage, out)), 0o600); err != nil {
		progress("warning: could not write " + ResultFileName + ": " + err.Error())
	}
	if failure != nil {
		return fail(failure)
	}
	if err := os.WriteFile(filepath.Join(dir, ReportFileName), []byte(parsed.Report+"\n"), 0o600); err != nil {
		progress("warning: could not write " + ReportFileName + ": " + err.Error())
	}
	if err := models.CompleteAgentReport(in.Index, report.ID, parsed.Meta.Summary, parsed.Meta.Responsible,
		parsed.Meta.Severity, parsed.Meta.Confidence, parsed.Report); err != nil {
		return report, fmt.Errorf("agent: record report: %w", err)
	}
	settle(ev.PatternKeys())
	report.Status = "completed"
	report.Summary = parsed.Meta.Summary
	report.Responsible = parsed.Meta.Responsible
	report.Severity = parsed.Meta.Severity
	report.Confidence = parsed.Meta.Confidence
	report.ReportMarkdown = parsed.Report
	// The machine-derived responsible party is replaced only by a definite
	// answer. "unknown" (or a missing META) keeps the rule-based value on the
	// group; the report row still records what the agent said.
	if isDefiniteResponsible(parsed.Meta.Responsible) && parsed.Meta.Responsible != group.Responsible {
		if err := models.UpdateGroupResponsible(in.Index, group.GroupKey, parsed.Meta.Responsible); err != nil {
			progress("warning: could not update responsible: " + err.Error())
		}
	}
	if !parsed.MetaParsed {
		progress("report stored (META block missing or invalid)")
	} else {
		progress("report stored")
	}
	return report, nil
}

// previousCoverage returns the latest completed report of a group and the
// pattern keys it covered (nil, nil when there is none, or when it covered
// no recorded pattern: a report written before patterns were recorded).
func previousCoverage(db *sql.DB, groupKey string) (*models.AgentReport, map[string]bool, error) {
	prev, err := models.LatestCompletedAgentReport(db, groupKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	keys, err := models.ListAgentReportPatterns(db, prev.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(keys) == 0 {
		return nil, nil, nil
	}
	covered := make(map[string]bool, len(keys))
	for _, k := range keys {
		covered[k] = true
	}
	return prev, covered, nil
}

// writeEvidenceFiles writes the evidence file of every sample into the
// workspace.
func writeEvidenceFiles(dir string, ev *Evidence) error {
	if len(ev.Samples) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(dir, EvidenceDirName), 0o700); err != nil {
		return err
	}
	for _, s := range ev.Samples {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(s.FileName)), []byte(s.File), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// usesPromptFile reports whether a launch argv reads the prompt from
// {prompt_file}.
func usesPromptFile(argv []string) bool {
	for _, a := range argv {
		if strings.Contains(a, "{prompt_file}") {
			return true
		}
	}
	return false
}

// runLine is the progress line announcing the run.
func runLine(p Provider, model, reasoning string, ev *Evidence) string {
	var opts []string
	if _, ok := p.(ModelSelector); ok && model != "" {
		opts = append(opts, "model "+model)
	}
	if _, ok := p.(ReasoningSelector); ok && reasoning != "" {
		opts = append(opts, "reasoning "+reasoning)
	}
	line := "running " + p.Label()
	if len(opts) > 0 {
		line += " (" + strings.Join(opts, ", ") + ")"
	}
	return fmt.Sprintf("%s on %d messages in %d patterns, %d samples", line, ev.Messages, len(ev.Patterns), len(ev.Samples))
}

// usageLine renders what the CLI reported about the run ("" when nothing).
func usageLine(u models.AgentRunUsage) string {
	var parts []string
	if u.Model != "" {
		parts = append(parts, "model "+u.Model)
	}
	if u.ReasoningEffort != "" {
		parts = append(parts, "reasoning "+u.ReasoningEffort)
	}
	if u.TokensUsed.Valid {
		parts = append(parts, fmt.Sprintf("tokens %d", u.TokensUsed.Int64))
	}
	if u.CommandCount.Valid {
		parts = append(parts, fmt.Sprintf("commands %d", u.CommandCount.Int64))
	}
	if len(parts) == 0 {
		return ""
	}
	return "usage: " + strings.Join(parts, ", ")
}

// RunDir returns the workspace directory of one analysis run:
// <agentRoot>/<sanitized address>/<group_key>/<report_id>.
func RunDir(agentRoot, address, groupKey string, reportID int64) string {
	return filepath.Join(agentRoot, mailengine.SanitizeAddress(address), groupKey, strconv.FormatInt(reportID, 10))
}

// copyTemplates copies templates/agent/<provider>/** from templates into dir.
// A nil FS or a missing provider directory is not an error.
func copyTemplates(templates fs.FS, provider, dir string) error {
	if templates == nil {
		return nil
	}
	root := TemplatesDirName + "/" + provider
	sub, err := fs.Sub(templates, root)
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "."); err != nil {
		return nil // no templates for this provider
	}
	return fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

// formatResultLog renders RESULT.log: the verdict, the extracted values, the
// usage the CLI reported, then the full CLI transcript.
func formatResultLog(failure error, parsed Output, usage models.AgentRunUsage, raw string) string {
	var b strings.Builder
	if failure == nil {
		b.WriteString("Result: Success\n")
	} else {
		b.WriteString("Result: Failure\n")
		b.WriteString("Reason: " + failure.Error() + "\n")
	}
	b.WriteString("\nSummary (extracted):\n")
	b.WriteString(orNone(parsed.Meta.Summary) + "\n")
	b.WriteString("\nResponsible (extracted): " + orNone(parsed.Meta.Responsible) + "\n")
	b.WriteString("Severity (extracted): " + orNone(parsed.Meta.Severity) + "\n")
	b.WriteString("Confidence (extracted): " + orNone(parsed.Meta.Confidence) + "\n")
	b.WriteString("\nUsage (reported by the CLI):\n")
	b.WriteString("Model: " + orNone(usage.Model) + "\n")
	b.WriteString("Reasoning effort: " + orNone(usage.ReasoningEffort) + "\n")
	b.WriteString("Tokens used: " + orNone(nullIntText(usage.TokensUsed)) + "\n")
	b.WriteString("Commands run: " + orNone(nullIntText(usage.CommandCount)) + "\n")
	b.WriteString("\nReport (extracted):\n")
	b.WriteString(orNone(parsed.Report) + "\n")
	b.WriteString("\nResponse (full agent transcript):\n")
	if strings.TrimSpace(raw) == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(raw)
		if !strings.HasSuffix(raw, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// isDefiniteResponsible reports whether v names an actual party (sender,
// recipient or domain) rather than "" or unknown.
func isDefiniteResponsible(v string) bool {
	switch v {
	case ResponsibleSender, ResponsibleRecipient, ResponsibleDomain:
		return true
	}
	return false
}

// nullIntText renders a nullable integer ("" when NULL).
func nullIntText(v sql.NullInt64) string {
	if !v.Valid {
		return ""
	}
	return strconv.FormatInt(v.Int64, 10)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
