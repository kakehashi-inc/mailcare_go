package models

import (
	"database/sql"
	"strings"
	"time"
)

// BounceGroup is a row of the per-mailbox groups table: bounce messages
// bundled by the unit an administrator acts on. Category names the kind of
// problem (ip_blocked, sender_blocked, user_unknown, ...), UnitValue the thing
// to act on (a sending IP, a sender address, a sending domain, a recipient
// address or domain) and Authority the party that decides the outcome (a
// blacklist, the recipient domain). Actionable is false for recipient-side
// problems the mail administrator cannot fix; those groups are kept for
// reference but excluded from Alerts by default. Membership is recorded in
// bounces.group_key and dmarc_records.group_key. There is no stored title:
// see Label.
type BounceGroup struct {
	GroupKey           string       `json:"group_key"`
	Category           string       `json:"category"`
	Actionable         bool         `json:"actionable"`
	UnitValue          string       `json:"unit_value"`
	Authority          string       `json:"authority"`
	RecipientDomain    string       `json:"recipient_domain"`
	StatusCode         string       `json:"status_code"`
	DiagnosticTemplate string       `json:"diagnostic_template"`
	Responsible        string       `json:"responsible"`
	State              string       `json:"state"`
	StateUpdatedAt     sql.NullTime `json:"-"`
	NeedsAnalysis      bool         `json:"needs_analysis"`
	MessageCount       int          `json:"message_count"`
	RecipientCount     int          `json:"recipient_count"`
	RemoteIPCount      int          `json:"remote_ip_count"`
	FirstSeen          sql.NullTime `json:"-"`
	LastSeen           sql.NullTime `json:"-"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
}

// Label is the technical one-line name of a group ("<category>: <unit> @
// <authority>"), used by the CLI, logs and prompts. The Web UI builds a
// localized headline from the same fields instead.
func (g *BounceGroup) Label() string {
	var b strings.Builder
	b.WriteString(g.Category)
	if g.UnitValue != "" {
		b.WriteString(": ")
		b.WriteString(g.UnitValue)
	}
	if g.Authority != "" {
		b.WriteString(" @ ")
		b.WriteString(g.Authority)
	}
	return b.String()
}

const groupColumns = `group_key, category, actionable, unit_value, authority, recipient_domain, status_code,
	diagnostic_template, responsible, state, state_updated_at, needs_analysis, message_count, recipient_count,
	remote_ip_count, first_seen, last_seen, created_at, updated_at`

// UpsertGroup inserts a group or, when it exists, refreshes its descriptive
// columns (category, actionable, unit, authority, domain, status code,
// template, responsible). Counters, dates, state and needs_analysis are
// maintained by RefreshGroupCounters and the state setters. A new group
// starts flagged for analysis only when it is actionable: recipient-side
// groups are never analyzed, so their flag is always 0.
func UpsertGroup(db Execer, g *BounceGroup) error {
	now := time.Now().UTC()
	if g.State == "" {
		g.State = "open"
	}
	_, err := db.Exec(
		`INSERT INTO groups (group_key, category, actionable, unit_value, authority, recipient_domain, status_code,
		   diagnostic_template, responsible, state, needs_analysis, message_count, recipient_count, remote_ip_count,
		   created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?)
		 ON CONFLICT(group_key) DO UPDATE SET category = excluded.category, actionable = excluded.actionable,
		   unit_value = excluded.unit_value, authority = excluded.authority,
		   recipient_domain = excluded.recipient_domain, status_code = excluded.status_code,
		   diagnostic_template = excluded.diagnostic_template, responsible = excluded.responsible,
		   updated_at = excluded.updated_at`,
		g.GroupKey, g.Category, boolToInt(g.Actionable), g.UnitValue, g.Authority, g.RecipientDomain, g.StatusCode,
		g.DiagnosticTemplate, g.Responsible, g.State, boolToInt(g.Actionable), now, now,
	)
	return err
}

// RefreshGroupCounters recomputes the message/recipient/IP counts and the
// first/last seen dates of a group from its members: its bounces and its
// DMARC records (each table filtered by the group key on its own; a report
// mail with several records of the group counts once). It never sets the
// analysis flag itself (the grouping phase flags an actionable group that
// received a bounce or DMARC record of a pattern its latest completed
// report did not cover, see mailengine); a recipient-side group
// (actionable = 0) always ends with the flag cleared.
func RefreshGroupCounters(db Execer, groupKey string) error {
	// MIN()/MAX() over a DATETIME column carry no declared type, so the driver
	// returns them as strings (see ParseSQLiteTime).
	var count, recipients, ips int
	var firstRaw, lastRaw sql.NullString
	if err := db.QueryRow(`SELECT COUNT(DISTINCT m.id), MIN(m.date), MAX(m.date),
		COUNT(DISTINCT NULLIF(g.recipient, '')), COUNT(DISTINCT NULLIF(g.remote_ip, ''))
		FROM (SELECT id AS message_id, recipient, remote_ip FROM bounces WHERE group_key = ?
		      UNION ALL
		      SELECT message_id, '', source_ip FROM dmarc_records WHERE group_key = ?) g
		JOIN messages m ON m.id = g.message_id`, groupKey, groupKey).Scan(&count, &firstRaw, &lastRaw, &recipients, &ips); err != nil {
		return err
	}
	_, err := db.Exec(
		`UPDATE groups SET message_count = ?, recipient_count = ?, remote_ip_count = ?, first_seen = ?, last_seen = ?,
		   needs_analysis = CASE WHEN actionable = 0 THEN 0 ELSE needs_analysis END, updated_at = ?
		 WHERE group_key = ?`,
		count, recipients, ips, ParseSQLiteTime(firstRaw), ParseSQLiteTime(lastRaw), time.Now().UTC(), groupKey,
	)
	return err
}

// DeleteEmptyGroups removes groups that no longer have members (bounces or
// DMARC records).
func DeleteEmptyGroups(db Execer) error {
	_, err := db.Exec(`DELETE FROM groups
		WHERE NOT EXISTS (SELECT 1 FROM bounces b WHERE b.group_key = groups.group_key)
		  AND NOT EXISTS (SELECT 1 FROM dmarc_records d WHERE d.group_key = groups.group_key)`)
	return err
}

// SetGroupState changes the state (open / resolved / ignored) of a group.
func SetGroupState(db *sql.DB, groupKey, state string) error {
	now := time.Now().UTC()
	_, err := db.Exec(`UPDATE groups SET state = ?, state_updated_at = ?, updated_at = ? WHERE group_key = ?`,
		state, now, now, groupKey)
	return err
}

// SetGroupNeedsAnalysis sets or clears the analysis flag of a group.
func SetGroupNeedsAnalysis(db Execer, groupKey string, needs bool) error {
	_, err := db.Exec(`UPDATE groups SET needs_analysis = ?, updated_at = ? WHERE group_key = ?`,
		boolToInt(needs), time.Now().UTC(), groupKey)
	return err
}

// UpdateGroupResponsible overrides the responsible party of a group (from an
// agent report).
func UpdateGroupResponsible(db *sql.DB, groupKey, responsible string) error {
	_, err := db.Exec(`UPDATE groups SET responsible = ?, updated_at = ? WHERE group_key = ?`,
		responsible, time.Now().UTC(), groupKey)
	return err
}

// RestoreGroupState puts back the state, its change time and the analysis flag
// of a group (used when an index is rebuilt and the group key came back). The
// flag is never set on a recipient-side group.
func RestoreGroupState(db *sql.DB, groupKey, state string, stateUpdatedAt sql.NullTime, needsAnalysis bool) error {
	_, err := db.Exec(`UPDATE groups SET state = ?, state_updated_at = ?,
		   needs_analysis = CASE WHEN actionable = 0 THEN 0 ELSE ? END, updated_at = ?
		 WHERE group_key = ?`,
		state, utcNullTime(stateUpdatedAt), boolToInt(needsAnalysis), time.Now().UTC(), groupKey)
	return err
}

// GetGroup returns one group (sql.ErrNoRows when absent).
func GetGroup(db Execer, groupKey string) (*BounceGroup, error) {
	return scanGroup(db.QueryRow(`SELECT `+groupColumns+` FROM groups WHERE group_key = ?`, groupKey))
}

// Group list scopes of Alerts. A recipient-side group (actionable = 0) is
// "excluded" only while it is open: once someone marks it resolved or
// ignored (it was handled after all) it is listed with the actionable groups,
// and reopening it sends it back to the excluded list. Excluded groups have
// no state of their own for the user and are not counted.
const (
	GroupScopeAll        = ""
	GroupScopeActionable = "actionable"
	GroupScopeExcluded   = "excluded"
)

// groupScopeCondition returns the WHERE condition of a scope ("" for all).
func groupScopeCondition(scope string) string {
	switch scope {
	case GroupScopeActionable:
		return `(actionable = 1 OR state <> 'open')`
	case GroupScopeExcluded:
		return `(actionable = 0 AND state = 'open')`
	}
	return ""
}

// GroupFilter narrows ListGroups.
type GroupFilter struct {
	State       string // "" = all
	Responsible string // "" = all
	Category    string // "" = all
	Actionable  *bool  // nil = all; true / false = the actionable column only (analysis, notification)
	Scope       string // GroupScope*: the Alerts list the group belongs to (see groupScopeCondition)
	Query       string // matched against unit, authority, recipient domain and template (LIKE)
	Sort        string // GroupSort*: the order within a state ("" = last seen, newest first)
}

// Orders of ListGroups (GroupFilter.Sort). Groups are ordered by state
// first (open, resolved, ignored), then by the sort, then by group key, so
// that the order is total and pages never overlap.
const (
	GroupSortLastSeenDesc = ""              // last seen, newest first
	GroupSortLastSeenAsc  = "last_seen_asc" // last seen, oldest first
	GroupSortCountDesc    = "count_desc"    // most messages first, then newest
	GroupSortSeverity     = "severity"      // severity of the latest completed report (high, medium, low, none), then newest
)

// groupSortOrder returns the ORDER BY terms of a sort ("" for an unknown one).
func groupSortOrder(sort string) string {
	switch sort {
	case GroupSortLastSeenDesc:
		return `last_seen DESC`
	case GroupSortLastSeenAsc:
		return `last_seen ASC`
	case GroupSortCountDesc:
		return `message_count DESC, last_seen DESC`
	case GroupSortSeverity:
		return `CASE (SELECT r.severity FROM agent_reports r WHERE r.group_key = groups.group_key
			AND r.status = 'completed' ORDER BY r.id DESC LIMIT 1)
			WHEN 'high' THEN 0 WHEN 'medium' THEN 1 WHEN 'low' THEN 2 ELSE 3 END, last_seen DESC`
	}
	return ""
}

// ValidGroupSort reports whether s names an order of ListGroups.
func ValidGroupSort(s string) bool {
	return groupSortOrder(s) != ""
}

// groupWhere is the WHERE clause of a filter and its arguments.
func groupWhere(f GroupFilter) (string, []any) {
	var conds []string
	var args []any
	if f.State != "" {
		conds = append(conds, `state = ?`)
		args = append(args, f.State)
	}
	if f.Responsible != "" {
		conds = append(conds, `responsible = ?`)
		args = append(args, f.Responsible)
	}
	if f.Category != "" {
		conds = append(conds, `category = ?`)
		args = append(args, f.Category)
	}
	if f.Actionable != nil {
		conds = append(conds, `actionable = ?`)
		args = append(args, boolToInt(*f.Actionable))
	}
	if c := groupScopeCondition(f.Scope); c != "" {
		conds = append(conds, c)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := likeContains(q)
		conds = append(conds, `(unit_value LIKE ?`+likeEscapeClause+` OR authority LIKE ?`+likeEscapeClause+
			` OR recipient_domain LIKE ?`+likeEscapeClause+` OR diagnostic_template LIKE ?`+likeEscapeClause+`)`)
		args = append(args, like, like, like, like)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// groupOrderBy is the ORDER BY clause of a filter (see the GroupSort*
// constants; an unknown sort orders like the default).
func groupOrderBy(f GroupFilter) string {
	order := groupSortOrder(f.Sort)
	if order == "" {
		order = groupSortOrder(GroupSortLastSeenDesc)
	}
	return ` ORDER BY CASE state WHEN 'open' THEN 0 WHEN 'resolved' THEN 1 ELSE 2 END, ` + order + `, group_key`
}

// ListGroups returns every group matching the filter, ordered by state
// (open first), then by the sort of the filter, then by group key.
func ListGroups(db *sql.DB, f GroupFilter) ([]*BounceGroup, error) {
	where, args := groupWhere(f)
	return queryGroups(db, `SELECT `+groupColumns+` FROM groups`+where+groupOrderBy(f), args...)
}

// ListGroupsPage returns one page (offset, limit) of the groups matching
// the filter, in the order of ListGroups, with the number of matching
// groups.
func ListGroupsPage(db *sql.DB, f GroupFilter, offset, limit int) ([]*BounceGroup, int, error) {
	where, args := groupWhere(f)
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM groups`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	groups, err := queryGroups(db, `SELECT `+groupColumns+` FROM groups`+where+groupOrderBy(f)+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	return groups, total, err
}

// ListGroupsNeedingAnalysis returns open, actionable groups flagged for
// analysis (new groups and groups that gained messages), oldest last-seen
// first. Recipient-side groups are never analyzed.
func ListGroupsNeedingAnalysis(db *sql.DB) ([]*BounceGroup, error) {
	return queryGroups(db, `SELECT `+groupColumns+` FROM groups WHERE needs_analysis = 1 AND state = 'open' AND actionable = 1
		ORDER BY last_seen ASC`)
}

// GroupCounts holds per-state group counts for the dashboard.
type GroupCounts struct {
	Open     int `json:"open"`
	Resolved int `json:"resolved"`
	Ignored  int `json:"ignored"`
}

// CountGroups returns the number of groups per state within a scope
// (GroupScopeAll counts every group).
func CountGroups(db *sql.DB, scope string) (GroupCounts, error) {
	var c GroupCounts
	where := ""
	if cond := groupScopeCondition(scope); cond != "" {
		where = ` WHERE ` + cond
	}
	rows, err := db.Query(`SELECT state, COUNT(*) FROM groups` + where + ` GROUP BY state`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return c, err
		}
		switch state {
		case "open":
			c.Open = n
		case "resolved":
			c.Resolved = n
		case "ignored":
			c.Ignored = n
		}
	}
	return c, rows.Err()
}

func queryGroups(db *sql.DB, query string, args ...any) ([]*BounceGroup, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BounceGroup
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func scanGroup(s rowScanner) (*BounceGroup, error) {
	g := &BounceGroup{}
	var actionable, needs int
	if err := s.Scan(&g.GroupKey, &g.Category, &actionable, &g.UnitValue, &g.Authority, &g.RecipientDomain,
		&g.StatusCode, &g.DiagnosticTemplate, &g.Responsible, &g.State, &g.StateUpdatedAt, &needs, &g.MessageCount,
		&g.RecipientCount, &g.RemoteIPCount, &g.FirstSeen, &g.LastSeen, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.Actionable, g.NeedsAnalysis = actionable != 0, needs != 0
	return g, nil
}
