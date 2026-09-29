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
	mailsRoot, err := mailsRootForCLI()
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
		"state": g.State, "state_updated_at": rfc3339OrNull(g.StateUpdatedAt), "state_reason": stringOrNull(g.StateReason.String),
		"state_note": stringOrNull(g.StateNote.String), "needs_recheck": g.NeedsRecheck, "needs_analysis": g.NeedsAnalysis,
		"report_summary": "", "report_severity": "",
	}
	if report != nil {
		row["report_summary"] = report.Summary
		row["report_severity"] = report.Severity
	}
	return row
}

// Group list scopes: actionable groups (the default), the open
// recipient-side groups excluded from Alerts, or both (see
// models.GroupScopeActionable for how a handled excluded group moves).
const (
	GroupScopeActionable = "actionable"
	GroupScopeExcluded   = "excluded"
	GroupScopeAll        = "all"
)

// GroupListScope maps a scope to the models.GroupFilter.Scope /
// CountGroups value. ok is false for an unknown scope.
func GroupListScope(scope string) (modelScope string, ok bool) {
	switch scope {
	case "", GroupScopeActionable:
		return models.GroupScopeActionable, true
	case GroupScopeExcluded:
		return models.GroupScopeExcluded, true
	case GroupScopeAll:
		return models.GroupScopeAll, true
	}
	return "", false
}

// GroupsCmd groups the bounce group subcommands: "groups ADDRESS [KEY]"
// (the default subcommand, list) lists the groups of a mailbox or shows one
// group with its latest report; "groups set-state ADDRESS KEY STATE" changes
// the state of a group.
type GroupsCmd struct {
	List     GroupsListCmd     `cmd:"" default:"withargs" help:"List the bounce groups of a mail address, or show one group (ADDRESS KEY)"`
	SetState GroupsSetStateCmd `cmd:"" name:"set-state" help:"Change the state of a group (open, resolved or ignored)"`
}

// GroupsListCmd lists the bounce groups of a mailbox ("groups ADDRESS") or,
// with a group key, shows one group with its latest report ("groups ADDRESS
// KEY").
type GroupsListCmd struct {
	Address  string `arg:"" help:"Mail address"`
	Key      string `arg:"" optional:"" help:"Group key: show that group instead of the list"`
	State    string `help:"Filter by state" enum:",open,resolved,ignored" default:""`
	Recheck  bool   `help:"Only the resolved / ignored groups sent back for a re-check"`
	Scope    string `help:"Which groups: actionable (default), excluded (recipient-side problems) or all" enum:"actionable,excluded,all" default:"actionable"`
	Category string `help:"Filter by category (ip_blocked, user_unknown, ...)" default:""`
	JSON     bool   `help:"Output as JSON"`
}

func (c *GroupsListCmd) Run() error {
	if strings.TrimSpace(c.Key) != "" {
		return showGroup(c.Address, c.Key, c.JSON)
	}
	scope, ok := GroupListScope(c.Scope)
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
	filter := models.GroupFilter{State: c.State, Category: c.Category, Scope: scope}
	if c.Recheck {
		recheck := true
		filter.Recheck = &recheck
	}
	groups, err := models.ListGroups(idx, filter)
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
	fmt.Printf("%-16s %-13s %-17s %-9s %-5s %-20s %-8s %s\n", "KEY", "STATE", "CATEGORY", "RESP", "MSGS", "LAST SEEN", "SEVERITY", "TITLE")
	for _, g := range groups {
		severity := ""
		if r := reports[g.GroupKey]; r != nil {
			severity = r.Severity
		}
		fmt.Printf("%-16s %-13s %-17s %-9s %-5d %-20s %-8s %s\n", g.GroupKey, cliGroupState(g), clip(g.Category, 17), g.Responsible,
			g.MessageCount, formatNullTime(g.LastSeen), severity, clip(g.Label(), 60))
	}
	return nil
}

// cliGroupState is the state of a group as the CLI shows it: a resolved or
// ignored group sent back for a re-check is marked "(re)".
func cliGroupState(g *models.BounceGroup) string {
	if g.NeedsRecheck {
		return g.State + " (re)"
	}
	return g.State
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
	history, err := models.ListGroupStateChanges(idx, g.GroupKey)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if asJSON {
		row := groupRow(g, report)
		row["stats"] = stats
		changes := make([]map[string]interface{}, 0, len(history))
		for _, h := range history {
			changes = append(changes, map[string]interface{}{
				"state": h.State, "reason": stringOrNull(h.Reason.String), "note": stringOrNull(h.Note.String),
				"changed_by": h.ChangedBy, "changed_at": h.ChangedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			})
		}
		row["history"] = changes
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
	fmt.Printf("state:         %s\n", cliGroupState(g))
	if g.StateReason.Valid {
		fmt.Printf("reason:        %s\n", g.StateReason.String)
	}
	if g.StateNote.Valid {
		fmt.Printf("note:          %s\n", clip(g.StateNote.String, 300))
	}
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
	if len(history) > 0 {
		fmt.Println("\n--- state history (newest first) ---")
		for _, h := range history {
			line := fmt.Sprintf("%s  %-8s", h.ChangedAt.Local().Format(cliTimeFmt), h.State)
			if h.Reason.Valid {
				line += "  " + h.Reason.String
			}
			if h.ChangedBy != "" {
				line += "  by " + h.ChangedBy
			}
			if h.Note.Valid {
				line += "  - " + clip(h.Note.String, 200)
			}
			fmt.Println(line)
		}
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

// GroupsSetStateCmd changes the state of a group ("groups set-state ADDRESS
// KEY STATE [--reason R] [--note N]"), the same change the Web UI makes from
// the alerts list and the group detail (validated by GroupStateInput,
// applied by ChangeGroupState).
type GroupsSetStateCmd struct {
	Address string `arg:"" help:"Mail address"`
	Key     string `arg:"" help:"Group key"`
	State   string `arg:"" help:"New state" enum:"open,resolved,ignored"`
	Reason  string `help:"Required with resolved (delisting, dns_fixed, server_fixed, sender_changed, content_changed, volume_adjusted, recipient_fixed, recipient_asked, other) and ignored (temporary, recipient_side, input_error, stopped_sending, spoofing, external_service, false_positive, low_impact, test_mail, other)"`
	Note    string `help:"Details of what was done (resolved), or the reason in words (ignored with --reason other)"`
}

func (c *GroupsSetStateCmd) Run() error {
	change, err := GroupStateInput(c.State, c.Reason, c.Note)
	if err != nil {
		return NewExitError(ExitArgument, err.Error())
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
	key := strings.TrimSpace(c.Key)
	g, err := models.GetGroup(idx, key)
	if err == sql.ErrNoRows {
		return NewExitErrorf(ExitArgument, "group %q not found", key)
	}
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if err := ChangeGroupState(idx, g.GroupKey, change, ""); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	fmt.Printf("Group %s of %s is now %s\n", g.GroupKey, mb.Address, c.State)
	return nil
}
