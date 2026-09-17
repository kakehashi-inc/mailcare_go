package models

import "database/sql"

// Setting is one row of the settings table: a server setting that overrides
// the code default (only non-default values are stored).
type Setting struct {
	Key   string
	Value string
}

// GetSetting retrieves a setting value by key. Returns empty string if absent.
// Read errors are swallowed too, so callers that must distinguish "absent"
// from "could not read" (e.g. generated secrets) use GetSettingStrict instead.
func GetSetting(db *sql.DB, key string) string {
	value, _, _ := GetSettingStrict(db, key)
	return value
}

// GetSettingStrict retrieves a setting value, distinguishing a missing row
// (found=false, no error) from a read failure (err != nil).
func GetSettingStrict(db *sql.DB, key string) (value string, found bool, err error) {
	err = db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// ListSettings returns every stored setting ordered by key.
func ListSettings(db *sql.DB) ([]Setting, error) {
	rows, err := db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Setting
	for rows.Next() {
		var s Setting
		if err := rows.Scan(&s.Key, &s.Value); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// InsertSettingIfAbsent stores the pair only when the key does not exist yet.
// Unlike SetSetting it can never overwrite an existing value, which makes it
// safe for generated secrets.
func InsertSettingIfAbsent(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		key, value,
	)
	return err
}

// SetSetting upserts a setting key/value pair.
func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// DeleteSetting removes a setting row if present (no error when absent). Used to
// keep the table free of values equal to the code default, so a later change to a
// default is not overridden by a baked-in row.
func DeleteSetting(db *sql.DB, key string) error {
	_, err := db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}
