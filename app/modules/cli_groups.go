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
		"group_key": g.GroupKey, "label": g.Label(), "category": g.Category, "unit_value": g.UnitValue,
		"authority": g.Authority, "actionable": g.Actionable, "recipient_domain": g.RecipientDomain,
		"status_code": g.StatusCode, "diagnostic_template": g.DiagnosticTemplate,
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

// Group list scopes: actionable groups (the default), the recipient-side
// groups excluded from Alerts, or both.
const (
	GroupScopeActionable = "actionable"
	GroupScopeExcluded   = "excluded"
	GroupScopeAll        = "all"
)

// ActionableFilter maps a scope to the ListGroups / CountGroups filter
// (nil = every group). ok is false for an unknown scope.
func ActionableFilter(scope string) (actionable *bool, ok bool) {
	switch scope {
	case "", GroupScopeActionable:
		v := true
		return &v, true
	case GroupScopeExcluded:
		v := false
		return &v, true
	case GroupScopeAll:
		return nil, true
	}
	return nil, false
}

// GroupsCmd lists the bounce groups of a mailbox ("groups ADDRESS") or, with
// a group key, shows one group with its latest report ("groups ADDRESS KEY").
type GroupsCmd struct {
	Address  string `arg:"" help:"Mail address"`
	Key      string `arg:"" optional:"" help:"Group key: show that group instead of the list"`
	State    string `help:"Filter by state" enum:",open,resolved,ignored" default:""`
	Scope    string `help:"Which groups: actionable (default), excluded (recipient-side problems) or all" enum:"actionable,excluded,all" default:"actionable"`
	Category string `help:"Filter by category (ip_blocked, user_unknown, ...)" default:""`
	JSON     bool   `help:"Output as JSON"`
}

func (c *GroupsCmd) Run() error {
	if strings.TrimSpace(c.Key) != "" {
		return showGroup(c.Address, c.Key, c.JSON)
	}
	actionable, ok := ActionableFilter(c.Scope)
	if !ok {
		return NewExitErrorf(ExitArgument, "unknown scope %q", c.Scope)
	}
	if c.Category != "" && !IsKnownCategory(c.Category) {
		return NewExitErrorf(ExitArgument, "unknown category %q (known: %s)", c.Category, strings.Join(KnownCategories(), ", "))
	}
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
	groups, err := models.ListGroups(idx, models.GroupFilter{State: c.State, Category: c.Category, Actionable: actionable})
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
	fmt.Printf("%-16s %-8s %-17s %-9s %-5s %-20s %-8s %s\n", "KEY", "STATE", "CATEGORY", "RESP", "MSGS", "LAST SEEN", "SEVERITY", "TITLE")
	for _, g := range groups {
		severity := ""
		if r := reports[g.GroupKey]; r != nil {
			severity = r.Severity
		}
		fmt.Printf("%-16s %-8s %-17s %-9s %-5d %-20s %-8s %s\n", g.GroupKey, g.State, clip(g.Category, 17), g.Responsible,
			g.MessageCount, formatNullTime(g.LastSeen), severity, clip(g.Label(), 60))
	}
	return nil
}

// showGroup prints one bounce group with its latest report ("groups ADDRESS
// KEY").
func showGroup(address, key string, asJSON bool) error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	mb, err := findMailbox(db, address)
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
	g, err := models.GetGroup(idx, strings.TrimSpace(key))
	if err == sql.ErrNoRows {
		return NewExitErrorf(ExitArgument, "group %q not found", key)
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
	if asJSON {
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
	fmt.Printf("label:         %s\n", g.Label())
	fmt.Printf("state:         %s\n", g.State)
	fmt.Printf("category:      %s\n", g.Category)
	fmt.Printf("unit:          %s\n", g.UnitValue)
	fmt.Printf("authority:     %s\n", g.Authority)
	fmt.Printf("actionable:    %v\n", g.Actionable)
	fmt.Printf("domain:        %s\n", g.RecipientDomain)
	fmt.Printf("status code:   %s\n", g.StatusCode)
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
