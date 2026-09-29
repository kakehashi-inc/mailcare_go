package models

import (
	"database/sql"
)

// agent_report_patterns: the patterns of the members (bounces.pattern_key,
// dmarc_records.pattern_key) of the group an analysis settled: a completed
// report, or a run that failed for good (the group is unanalyzable as it
// is; a usage limit or a cancellation settles nothing). A group is flagged
// for analysis again only when one of its members has a pattern that the
// latest settling report does not list (PatternCovered /
// GroupHasUncoveredPattern); more members of a pattern already settled only
// update the counters. Rows are deleted with their
// report (cascade).

// InsertAgentReportPatterns records the pattern keys a report covered.
func InsertAgentReportPatterns(db Execer, reportID int64, patternKeys []string) error {
	for _, k := range patternKeys {
		if _, err := db.Exec(`INSERT OR IGNORE INTO agent_report_patterns (report_id, pattern_key) VALUES (?, ?)`,
			reportID, k); err != nil {
			return err
		}
	}
	return nil
}

// ListAgentReportPatterns returns the pattern keys a report covered, sorted.
func ListAgentReportPatterns(db *sql.DB, reportID int64) ([]string, error) {
	rows, err := db.Query(`SELECT pattern_key FROM agent_report_patterns WHERE report_id = ? ORDER BY pattern_key`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ListAllAgentReportPatterns returns the pattern keys of every report by
// report id (used to carry the rows over when the index is rebuilt).
func ListAllAgentReportPatterns(db *sql.DB) (map[int64][]string, error) {
	rows, err := db.Query(`SELECT report_id, pattern_key FROM agent_report_patterns ORDER BY report_id, pattern_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			return nil, err
		}
		out[id] = append(out[id], k)
	}
	return out, rows.Err()
}

// latestSettlingReportID is the subquery that names the latest report of the
// group given as its parameter that settled the group (it recorded
// patterns: a completed report or a run that failed for good).
const latestSettlingReportID = `(SELECT MAX(r.id) FROM agent_reports r WHERE r.group_key = ?
	AND EXISTS (SELECT 1 FROM agent_report_patterns s WHERE s.report_id = r.id))`

// PatternCovered reports whether the latest settling report of a group
// covered the pattern (false when no analysis settled the group yet).
func PatternCovered(db Execer, groupKey, patternKey string) (bool, error) {
	var covered bool
	err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM agent_report_patterns
		WHERE pattern_key = ? AND report_id = `+latestSettlingReportID+`)`, patternKey, groupKey).Scan(&covered)
	return covered, err
}

// GroupHasUncoveredPattern reports whether a member (bounce or DMARC record)
// of the group has a pattern that the latest settling report of the group
// did not cover (true for a group with members that no analysis settled
// yet).
func GroupHasUncoveredPattern(db Execer, groupKey string) (bool, error) {
	var uncovered bool
	err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM bounces b WHERE b.group_key = ? AND NOT EXISTS (
		SELECT 1 FROM agent_report_patterns p WHERE p.pattern_key = b.pattern_key AND p.report_id = `+latestSettlingReportID+`))
		OR EXISTS (SELECT 1 FROM dmarc_records d WHERE d.group_key = ? AND NOT EXISTS (
		SELECT 1 FROM agent_report_patterns p WHERE p.pattern_key = d.pattern_key AND p.report_id = `+latestSettlingReportID+`))`,
		groupKey, groupKey, groupKey, groupKey).Scan(&uncovered)
	return uncovered, err
}
