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
// messages.group_key.
type BounceGroup struct {
	GroupKey           string       `json:"group_key"`
	Title              string       `json:"title"`
	Category           string       `json:"category"`
	UnitValue          string       `json:"unit_value"`
	Authority          string       `json:"authority"`
	Actionable         bool         `json:"actionable"`
	BounceKind         string       `json:"bounce_kind"`
	RecipientDomain    string       `json:"recipient_domain"`
	StatusCode         string       `json:"status_code"`
	SMTPCode           string       `json:"smtp_code"`
	DiagnosticTemplate string       `json:"diagnostic_template"`
	Responsible        string       `json:"responsible"`
	MessageCount       int          `json:"message_count"`
	RecipientCount     int          `json:"recipient_count"`
	RemoteIPCount      int          `json:"remote_ip_count"`
	FirstSeen          sql.NullTime `json:"-"`
	LastSeen           sql.NullTime `json:"-"`
	State              string       `json:"state"`
	StateUpdatedAt     sql.NullTime `json:"-"`
	NeedsAnalysis      bool         `json:"needs_analysis"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
}

const groupColumns = `group_key, title, category, unit_value, authority, actionable, bounce_kind, recipient_domain,
	status_code, smtp_code, diagnostic_template, responsible, message_count, recipient_count, remote_ip_count,
	first_seen, last_seen, state, state_updated_at, needs_analysis, created_at, updated_at`

// UpsertGroup inserts a group or, when it exists, refreshes its descriptive
// columns (title, kind, domain, codes, template, responsible). Counters, dates,
// state and needs_analysis are maintained by RefreshGroupCounters and the state
// setters.
func UpsertGroup(db *sql.DB, g *BounceGroup) error {
	now := time.Now().UTC()
	if g.State == "" {
		g.State = "open"
	}
	_, err := db.Exec(
		`INSERT INTO groups (group_key, title, category, unit_value, authority, actionable, bounce_kind,
		   recipient_domain, status_code, smtp_code, diagnostic_template, responsible, state, needs_analysis,
		   created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		 ON CONFLICT(group_key) DO UPDATE SET title = excluded.title, category = excluded.category,
		   unit_value = excluded.unit_value, authority = excluded.authority, actionable = excluded.actionable,
		   bounce_kind = excluded.bounce_kind, recipient_domain = excluded.recipient_domain,
		   status_code = excluded.status_code, smtp_code = excluded.smtp_code,
		   diagnostic_template = excluded.diagnostic_template, responsible = excluded.responsible,
		   updated_at = excluded.updated_at`,
		g.GroupKey, g.Title, g.Category, g.UnitValue, g.Authority, boolToInt(g.Actionable), g.BounceKind,
		g.RecipientDomain, g.StatusCode, g.SMTPCode, g.DiagnosticTemplate, g.Responsible, g.State, now, now,
	)
	return err
}

// RefreshGroupCounters recomputes message/recipient/IP counts and first/last
// seen of a group from its messages. It marks the group as needing analysis
// when the message count grew.
func RefreshGroupCounters(db *sql.DB, groupKey string) error {
	var prev int
	_ = db.QueryRow(`SELECT message_count FROM groups WHERE group_key = ?`, groupKey).Scan(&prev)
	var count int
	// MIN()/MAX() over a DATETIME column carry no declared type, so the
	// driver returns them as strings; parse them by hand (see ParseSQLiteTime).
	var firstRaw, lastRaw sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*), MIN(date), MAX(date) FROM messages WHERE group_key = ?`, groupKey).
		Scan(&count, &firstRaw, &lastRaw); err != nil {
		return err
	}
	first, last := ParseSQLiteTime(firstRaw), ParseSQLiteTime(lastRaw)
	var recipients, ips int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT b.original_recipient), COUNT(DISTINCT NULLIF(b.remote_ip, ''))
		FROM bounces b JOIN messages m ON m.id = b.message_id WHERE m.group_key = ?`, groupKey).
		Scan(&recipients, &ips); err != nil {
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
		count, recipients, ips, first, last, needs, time.Now().UTC(), groupKey,
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

// UpdateGroupResponsible overrides the machine-derived responsible party of a
// group (used when an agent report names one).
func UpdateGroupResponsible(db *sql.DB, groupKey, responsible string) error {
	_, err := db.Exec(`UPDATE groups SET responsible = ?, updated_at = ? WHERE group_key = ?`,
		responsible, time.Now().UTC(), groupKey)
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
	Query       string // matched against title, unit, authority, domain, template (LIKE)
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
		conds = append(conds, `(title LIKE ? OR unit_value LIKE ? OR authority LIKE ? OR recipient_domain LIKE ? OR diagnostic_template LIKE ? OR status_code LIKE ?)`)
		args = append(args, like, like, like, like, like, like)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	rows, err := db.Query(`SELECT `+groupColumns+` FROM groups`+where+
		` ORDER BY CASE state WHEN 'open' THEN 0 WHEN 'resolved' THEN 1 ELSE 2 END, last_seen DESC`, args...)
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

// ListGroupsNeedingAnalysis returns open, actionable groups flagged for
// analysis (new groups and groups that gained messages), oldest last-seen
// first. Recipient-side groups are never analyzed.
func ListGroupsNeedingAnalysis(db *sql.DB) ([]*BounceGroup, error) {
	rows, err := db.Query(`SELECT ` + groupColumns + ` FROM groups WHERE needs_analysis = 1 AND state = 'open' AND actionable = 1 ORDER BY last_seen ASC`)
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

func scanGroup(s rowScanner) (*BounceGroup, error) {
	g := &BounceGroup{}
	var needs, actionable int
	if err := s.Scan(&g.GroupKey, &g.Title, &g.Category, &g.UnitValue, &g.Authority, &actionable, &g.BounceKind,
		&g.RecipientDomain, &g.StatusCode, &g.SMTPCode, &g.DiagnosticTemplate, &g.Responsible, &g.MessageCount,
		&g.RecipientCount, &g.RemoteIPCount, &g.FirstSeen, &g.LastSeen, &g.State, &g.StateUpdatedAt, &needs,
		&g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.NeedsAnalysis, g.Actionable = needs != 0, actionable != 0
	return g, nil
}

// RestoreGroupState writes back the state, its timestamp and the analysis
// flag of a group that survived a reindex (mailengine carries them over from
// the previous index).
func RestoreGroupState(db *sql.DB, groupKey, state string, stateUpdatedAt sql.NullTime, needsAnalysis bool) error {
	_, err := db.Exec(`UPDATE groups SET state = ?, state_updated_at = ?, needs_analysis = ?, updated_at = ? WHERE group_key = ?`,
		state, stateUpdatedAt, boolToInt(needsAnalysis), time.Now().UTC(), groupKey)
	return err
}
