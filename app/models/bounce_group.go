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
// messages.group_key. There is no stored title: see Label.
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
// maintained by RefreshGroupCounters and the state setters.
func UpsertGroup(db *sql.DB, g *BounceGroup) error {
	now := time.Now().UTC()
	if g.State == "" {
		g.State = "open"
	}
	g.UnitValue = truncateRunes(g.UnitValue, 320)
	g.Authority = truncateRunes(g.Authority, 320)
	g.RecipientDomain = truncateRunes(g.RecipientDomain, 253)
	g.StatusCode = truncateRunes(g.StatusCode, 11)
	g.DiagnosticTemplate = truncateRunes(g.DiagnosticTemplate, 300)
	_, err := db.Exec(
		`INSERT INTO groups (group_key, category, actionable, unit_value, authority, recipient_domain, status_code,
		   diagnostic_template, responsible, state, needs_analysis, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		 ON CONFLICT(group_key) DO UPDATE SET category = excluded.category, actionable = excluded.actionable,
		   unit_value = excluded.unit_value, authority = excluded.authority,
		   recipient_domain = excluded.recipient_domain, status_code = excluded.status_code,
		   diagnostic_template = excluded.diagnostic_template, responsible = excluded.responsible,
		   updated_at = excluded.updated_at`,
		g.GroupKey, g.Category, boolToInt(g.Actionable), g.UnitValue, g.Authority, g.RecipientDomain, g.StatusCode,
		g.DiagnosticTemplate, g.Responsible, g.State, now, now,
	)
	return err
}

// RefreshGroupCounters recomputes the message/recipient/IP counts and the
// first/last seen dates of a group from its messages. It marks the group as
// needing analysis when the message count grew.
func RefreshGroupCounters(db *sql.DB, groupKey string) error {
	var prev int
	_ = db.QueryRow(`SELECT message_count FROM groups WHERE group_key = ?`, groupKey).Scan(&prev)
	// MIN()/MAX() over a DATETIME column carry no declared type, so the driver
	// returns them as strings (see ParseSQLiteTime).
	var count, recipients, ips int
	var firstRaw, lastRaw sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*), MIN(date), MAX(date),
		COUNT(DISTINCT NULLIF(json_extract(detail_info, '$.recipient'), '')),
		COUNT(DISTINCT NULLIF(json_extract(detail_info, '$.remote_ip'), ''))
		FROM messages WHERE group_key = ?`, groupKey).Scan(&count, &firstRaw, &lastRaw, &recipients, &ips); err != nil {
		return err
	}
	needs := 0
	if count > prev {
		needs = 1
	}
	_, err := db.Exec(
		`UPDATE groups SET message_count = ?, recipient_count = ?, remote_ip_count = ?, first_seen = ?, last_seen = ?,
		   needs_analysis = CASE WHEN ? = 1 THEN 1 ELSE needs_analysis END, updated_at = ?
		 WHERE group_key = ?`,
		count, recipients, ips, ParseSQLiteTime(firstRaw), ParseSQLiteTime(lastRaw), needs, time.Now().UTC(), groupKey,
	)
	return err
}

// DeleteEmptyGroups removes groups that no longer have messages.
func DeleteEmptyGroups(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM groups WHERE group_key NOT IN (SELECT DISTINCT group_key FROM messages WHERE group_key <> '')`)
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
func SetGroupNeedsAnalysis(db *sql.DB, groupKey string, needs bool) error {
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
// of a group (used when an index is rebuilt and the group key came back).
func RestoreGroupState(db *sql.DB, groupKey, state string, stateUpdatedAt sql.NullTime, needsAnalysis bool) error {
	_, err := db.Exec(`UPDATE groups SET state = ?, state_updated_at = ?, needs_analysis = ?, updated_at = ? WHERE group_key = ?`,
		state, utcNullTime(stateUpdatedAt), boolToInt(needsAnalysis), time.Now().UTC(), groupKey)
	return err
}

// GetGroup returns one group (sql.ErrNoRows when absent).
func GetGroup(db *sql.DB, groupKey string) (*BounceGroup, error) {
	return scanGroup(db.QueryRow(`SELECT `+groupColumns+` FROM groups WHERE group_key = ?`, groupKey))
}

// GroupFilter narrows ListGroups.
type GroupFilter struct {
	State       string // "" = all
	Responsible string // "" = all
	Category    string // "" = all
	Actionable  *bool  // nil = all; true = actionable only; false = excluded (recipient-side) only
	Query       string // matched against unit, authority, recipient domain and template (LIKE)
}

// ListGroups returns groups ordered by state (open first) then last seen desc.
func ListGroups(db *sql.DB, f GroupFilter) ([]*BounceGroup, error) {
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
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + q + "%"
		conds = append(conds, `(unit_value LIKE ? OR authority LIKE ? OR recipient_domain LIKE ? OR diagnostic_template LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	return queryGroups(db, `SELECT `+groupColumns+` FROM groups`+where+
		` ORDER BY CASE state WHEN 'open' THEN 0 WHEN 'resolved' THEN 1 ELSE 2 END, last_seen DESC`, args...)
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

// CountGroups returns the number of groups per state. actionable nil counts
// every group; true/false counts only actionable or only excluded groups.
func CountGroups(db *sql.DB, actionable *bool) (GroupCounts, error) {
	var c GroupCounts
	where := ""
	var args []any
	if actionable != nil {
		where = ` WHERE actionable = ?`
		args = append(args, boolToInt(*actionable))
	}
	rows, err := db.Query(`SELECT state, COUNT(*) FROM groups`+where+` GROUP BY state`, args...)
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
