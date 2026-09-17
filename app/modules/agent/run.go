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
	"strings"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// AnalyzeGroup builds the prompt for the group (listing the raw message files
// so the CLI reads the originals itself), runs the provider CLI in the group's
// workspace, parses the REPORT/META blocks and stores an agent report row.
// It returns the stored report (status completed or error) and an error only
// when nothing could be recorded.
//
// Success is decided by whether a usable REPORT block was extracted from the
// transcript after the echoed prompt is removed (see StripPromptEcho and
// ValidateOutput: no template placeholders, not too short, no placeholder
// summary). The CLI exit code is not a reliable signal (codex exits non-zero on
// fine runs), so it is ignored. A launch failure (CLI missing), a timeout, a
// cancellation, a usage/rate limit ("usage limit reached (retry after ...)"),
// a missing REPORT block or an unusable report fail the run and the reason is
// stored on the report. A failure never touches REPORT.md or the previous
// completed report row and sets needs_analysis so the next sync retries the
// group; RESULT.log keeps the full transcript.
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

	// Every failure re-flags the group so the next sync retries it, even
	// when this run was triggered explicitly for a group whose flag was 0.
	fail := func(reason error) (*models.AgentReport, error) {
		progress("analysis failed: " + reason.Error())
		report.Status = "error"
		report.ErrorMessage = reason.Error()
		if err := models.FailAgentReport(in.Index, report.ID, reason.Error()); err != nil {
			return report, fmt.Errorf("agent: record failure: %w", err)
		}
		if err := models.SetGroupNeedsAnalysis(in.Index, group.GroupKey, true); err != nil {
			progress("warning: could not set needs_analysis: " + err.Error())
		}
		return report, nil
	}

	provider, ok := lookupProvider(providerName)
	if !ok {
		return fail(fmt.Errorf("unknown agent provider %q", providerName))
	}

	// 1. Workspace.
	dir := filepath.Join(in.AgentRoot, mailengine.SanitizeAddress(in.Address), group.GroupKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(fmt.Errorf("create workspace: %w", err))
	}
	if err := copyTemplates(in.TemplatesFS, provider.Name(), dir); err != nil {
		return fail(fmt.Errorf("copy templates: %w", err))
	}

	// 2. Prompt.
	progress("building prompt for " + group.GroupKey)
	stats, err := models.GroupStats(in.Index, group.GroupKey)
	if err != nil {
		return fail(fmt.Errorf("load group stats: %w", err))
	}
	msgs, _, err := models.ListMessages(in.Index, models.MessageFilter{GroupKey: group.GroupKey, Limit: MaxSampleMessages})
	if err != nil {
		return fail(fmt.Errorf("list messages: %w", err))
	}
	promptText := BuildPrompt(PromptInput{
		MailsRoot: in.MailsRoot,
		Address:   in.Address,
		Language:  in.Language,
		Group:     group,
		Stats:     stats,
		Messages:  msgs,
	})
	promptFile := filepath.Join(dir, PromptFileName)
	if err := os.WriteFile(promptFile, []byte(promptText), 0o600); err != nil {
		return fail(fmt.Errorf("write prompt: %w", err))
	}

	// 3. Run the CLI.
	progress(fmt.Sprintf("running %s on %d messages", provider.Label(), len(msgs)))
	runCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	out, runErr := runProvider(runCtx, provider, dir, promptText, promptFile)
	// The echoed prompt is dropped before parsing so that its markers and
	// placeholders (and its wording, for the rate-limit markers) are ignored.
	answer := StripPromptEcho(out, promptText)
	parsed := ParseOutput(answer)

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
			failure = fmt.Errorf("analysis canceled: %v", ctx.Err())
		case limit.Limited:
			failure = errors.New(limit.ErrorMessage())
		case !parsed.ReportParsed:
			failure = fmt.Errorf("%s produced no report", provider.Label())
		default:
			// A REPORT block that still carries placeholders, is too short or
			// comes with a placeholder summary must not replace a good report.
			failure = fmt.Errorf("%s produced an unusable report: %v", provider.Label(), invalid)
		}
	}

	// 4. Record the outcome.
	if err := os.WriteFile(filepath.Join(dir, ResultFileName), []byte(formatResultLog(failure, parsed, out)), 0o600); err != nil {
		progress("warning: could not write " + ResultFileName + ": " + err.Error())
	}
	if failure != nil {
		return fail(failure)
	}
	if err := os.WriteFile(filepath.Join(dir, ReportFileName), []byte(parsed.Report+"\n"), 0o600); err != nil {
		progress("warning: could not write " + ReportFileName + ": " + err.Error())
	}
	if err := models.CompleteAgentReport(in.Index, report.ID, parsed.Meta.Summary, parsed.Meta.Responsible,
		parsed.Meta.Severity, parsed.Report); err != nil {
		return report, fmt.Errorf("agent: record report: %w", err)
	}
	report.Status = "completed"
	report.Summary = parsed.Meta.Summary
	report.Responsible = parsed.Meta.Responsible
	report.Severity = parsed.Meta.Severity
	report.ReportMarkdown = parsed.Report
	if err := models.SetGroupNeedsAnalysis(in.Index, group.GroupKey, false); err != nil {
		progress("warning: could not clear needs_analysis: " + err.Error())
	}
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

// copyTemplates copies agent-templates/<provider>/** from templates into dir.
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

// formatResultLog renders RESULT.log: the verdict, the extracted values, then
// the full CLI transcript.
func formatResultLog(failure error, parsed Output, raw string) string {
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

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}
