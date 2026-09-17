package modules

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// --- bounce group commands (groups / group) ---

// openIndexForCLI opens the per-mailbox index of a mailbox.
func openIndexForCLI(ctx context.Context, mb *models.Mailbox) (*sql.DB, error) {
	mailsRoot, _, err := dataRoots()
	if err != nil {
		return nil, err
	}
	idx, err := mailengine.OpenIndex(ctx, mailsRoot, mb.Address, echoProgress)
	if err != nil {
		return nil, NewExitErrorf(ExitFileIO, "failed to open the index of %s: %s", mb.Address, err)
	}
	return idx, nil
}

// groupRow renders a group for JSON output.
func groupRow(g *models.BounceGroup, report *models.AgentReport) map[string]interface{} {
	row := map[string]interface{}{
		"group_key": g.GroupKey, "title": g.Title, "bounce_kind": g.BounceKind, "recipient_domain": g.RecipientDomain,
		"status_code": g.StatusCode, "smtp_code": g.SMTPCode, "diagnostic_template": g.DiagnosticTemplate,
		"responsible": g.Responsible, "message_count": g.MessageCount, "recipient_count": g.RecipientCount,
		"remote_ip_count": g.RemoteIPCount, "first_seen": rfc3339OrNull(g.FirstSeen), "last_seen": rfc3339OrNull(g.LastSeen),
		"state": g.State, "state_updated_at": rfc3339OrNull(g.StateUpdatedAt), "needs_analysis": g.NeedsAnalysis,
		"report_summary": "", "report_severity": "",
	}
	if report != nil {
		row["report_summary"] = report.Summary
		row["report_severity"] = report.Severity
	}
	return row
}

// GroupsCmd lists the bounce groups of a mailbox.
type GroupsCmd struct {
	Address string `arg:"" help:"Mail address"`
	State   string `help:"Filter by state" enum:",open,resolved,ignored" default:""`
	JSON    bool   `help:"Output as JSON"`
}

func (c *GroupsCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	mb, err := findMailbox(db, c.Address)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	idx, err := openIndexForCLI(ctx, mb)
	if err != nil {
		return err
	}
	defer idx.Close()
	groups, err := models.ListGroups(idx, models.GroupFilter{State: c.State})
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	reports, err := models.LatestCompletedAgentReports(idx)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		out := make([]map[string]interface{}, 0, len(groups))
		for _, g := range groups {
			out = append(out, groupRow(g, reports[g.GroupKey]))
		}
		printJSON(out)
		return nil
	}
	if len(groups) == 0 {
		fmt.Println("No groups.")
		return nil
	}
	fmt.Printf("%-16s %-8s %-9s %-5s %-20s %-8s %s\n", "KEY", "STATE", "RESP", "MSGS", "LAST SEEN", "SEVERITY", "TITLE")
	for _, g := range groups {
		severity := ""
		if r := reports[g.GroupKey]; r != nil {
			severity = r.Severity
		}
		fmt.Printf("%-16s %-8s %-9s %-5d %-20s %-8s %s\n", g.GroupKey, g.State, g.Responsible, g.MessageCount,
			formatNullTime(g.LastSeen), severity, clip(g.Title, 60))
	}
	return nil
}

// GroupCmd shows one bounce group with its latest report.
type GroupCmd struct {
	Address string `arg:"" help:"Mail address"`
	Key     string `arg:"" help:"Group key"`
	JSON    bool   `help:"Output as JSON"`
}

func (c *GroupCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	mb, err := findMailbox(db, c.Address)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	idx, err := openIndexForCLI(ctx, mb)
	if err != nil {
		return err
	}
	defer idx.Close()
	g, err := models.GetGroup(idx, strings.TrimSpace(c.Key))
	if err == sql.ErrNoRows {
		return NewExitErrorf(ExitArgument, "group %q not found", c.Key)
	}
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	stats, err := models.GroupStats(idx, g.GroupKey)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	report, err := models.LatestCompletedAgentReport(idx, g.GroupKey)
	if err != nil && err != sql.ErrNoRows {
		return NewExitError(ExitGeneral, err.Error())
	}
	if c.JSON {
		row := groupRow(g, report)
		row["stats"] = stats
		if report != nil {
			row["report"] = map[string]interface{}{
				"id": report.ID, "provider": report.Provider, "status": report.Status, "summary": report.Summary,
				"responsible": report.Responsible, "severity": report.Severity, "report_markdown": report.ReportMarkdown,
				"finished_at": rfc3339OrNull(report.FinishedAt),
			}
		} else {
			row["report"] = nil
		}
		printJSON(row)
		return nil
	}
	fmt.Printf("key:           %s\n", g.GroupKey)
	fmt.Printf("title:         %s\n", g.Title)
	fmt.Printf("state:         %s\n", g.State)
	fmt.Printf("kind:          %s\n", g.BounceKind)
	fmt.Printf("domain:        %s\n", g.RecipientDomain)
	fmt.Printf("status code:   %s (smtp %s)\n", g.StatusCode, g.SMTPCode)
	fmt.Printf("responsible:   %s\n", g.Responsible)
	fmt.Printf("messages:      %d (recipients %d, remote IPs %d)\n", g.MessageCount, g.RecipientCount, g.RemoteIPCount)
	fmt.Printf("first seen:    %s\n", formatNullTime(g.FirstSeen))
	fmt.Printf("last seen:     %s\n", formatNullTime(g.LastSeen))
	fmt.Printf("needs analysis: %v\n", g.NeedsAnalysis)
	fmt.Printf("template:      %s\n", clip(g.DiagnosticTemplate, 200))
	if len(stats.Recipients) > 0 {
		fmt.Printf("recipients:    %s\n", clip(strings.Join(stats.Recipients, ", "), 300))
	}
	if len(stats.RemoteIPs) > 0 {
		fmt.Printf("remote IPs:    %s\n", clip(strings.Join(stats.RemoteIPs, ", "), 300))
	}
	if len(stats.RemoteMTAs) > 0 {
		fmt.Printf("remote MTAs:   %s\n", clip(strings.Join(stats.RemoteMTAs, ", "), 300))
	}
	if report == nil {
		fmt.Println("\nNo completed analysis report.")
		return nil
	}
	fmt.Printf("\n--- report (%s, %s, severity %s, responsible %s) ---\n", report.Provider,
		formatNullTime(report.FinishedAt), report.Severity, report.Responsible)
	if report.Summary != "" {
		fmt.Printf("summary: %s\n\n", report.Summary)
	}
	fmt.Println(strings.TrimSpace(report.ReportMarkdown))
	return nil
}
