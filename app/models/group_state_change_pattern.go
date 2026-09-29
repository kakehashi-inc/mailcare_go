package models

// group_state_change_patterns: the re-check keys of the members a group had
// when its state was changed. A re-check key is the pattern of a member
// without its remote MTA (mailengine.RecheckKey: a bounce's status code,
// wording and kind of diagnostic source; a DMARC record's pattern), so that
// the same rejection reported by another receiving server is not new. The
// keys are computed by mailengine, which is why they are written by
// mailengine (and the rows the migration could not fill are filled when the
// index is opened, see mailengine.OpenIndex). Rows are deleted with their
// state change (cascade).

// InsertGroupStateChangePatterns records the re-check keys of a state
// change.
func InsertGroupStateChangePatterns(db Execer, changeID int64, keys []string) error {
	for _, k := range keys {
		if _, err := db.Exec(`INSERT OR IGNORE INTO group_state_change_patterns (change_id, pattern_key) VALUES (?, ?)`,
			changeID, k); err != nil {
			return err
		}
	}
	return nil
}

// ListGroupStateChangePatterns returns the re-check keys recorded with a
// state change as a set.
func ListGroupStateChangePatterns(db Execer, changeID int64) (map[string]bool, error) {
	rows, err := db.Query(`SELECT pattern_key FROM group_state_change_patterns WHERE change_id = ?`, changeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// ListAllGroupStateChangePatterns returns the re-check keys of every state
// change by change id (used to carry the rows over when the index is
// rebuilt).
func ListAllGroupStateChangePatterns(db Execer) (map[int64][]string, error) {
	rows, err := db.Query(`SELECT change_id, pattern_key FROM group_state_change_patterns ORDER BY change_id, pattern_key`)
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

// ListStateChangesWithoutPatterns returns the ids and group keys of the
// state changes that have no recorded re-check key (the rows the migration
// created for the groups that were already resolved or ignored).
func ListStateChangesWithoutPatterns(db Execer) (map[int64]string, error) {
	rows, err := db.Query(`SELECT c.id, c.group_key FROM group_state_changes c
		WHERE NOT EXISTS (SELECT 1 FROM group_state_change_patterns p WHERE p.change_id = c.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, rows.Err()
}
