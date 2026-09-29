package models

import (
	"database/sql"
	"mailcare/app/modules/message"
	"time"
)

// ErrEmailTaken is returned by the user writes when the mail address is
// already set on another user. Addresses are unique among users (they can be
// used to log in), but without a UNIQUE constraint: the writes check it in
// the same statement (SQLite runs each statement atomically, so concurrent
// writers, including the CLI in another process, cannot both pass), and rows
// that shared an address before the rule existed can keep it.
var ErrEmailTaken = message.New("validation.user.emailTaken", "email address is already used by another user")

// User is a row of the users table: a person who can log in to the Web UI.
type User struct {
	ID           int64        `json:"id"`
	Username     string       `json:"username"`
	DisplayName  string       `json:"display_name"`
	Email        string       `json:"email"`    // optional notification address ("" = none)
	Language     string       `json:"language"` // UI language: ja | en (default ja; no browser detection)
	Timezone     string       `json:"timezone"` // IANA zone used to display times to this user (default Asia/Tokyo)
	Theme        string       `json:"theme"`    // UI theme: auto | light | dark (default auto)
	PasswordHash string       `json:"-"`
	Role         string       `json:"role"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	LastLoginAt  sql.NullTime `json:"-"`
}

const userColumns = `id, username, display_name, email, language, timezone, theme, password_hash, role, created_at,
	updated_at, last_login_at`

// Defaults of the user preferences.
const (
	DefaultLanguage = "ja"
	DefaultTimezone = "Asia/Tokyo"
	DefaultTheme    = "auto"
)

// applyPreferenceDefaults fills empty preference fields with the defaults.
func (u *User) applyPreferenceDefaults() {
	if u.Language == "" {
		u.Language = DefaultLanguage
	}
	if u.Timezone == "" {
		u.Timezone = DefaultTimezone
	}
	if u.Theme == "" {
		u.Theme = DefaultTheme
	}
}

// InsertUser creates a user row and fills in its ID. It returns
// ErrEmailTaken when another user already has the address.
func InsertUser(db *sql.DB, u *User) error {
	now := time.Now().UTC()
	u.applyPreferenceDefaults()
	res, err := db.Exec(
		`INSERT INTO users (username, display_name, email, language, timezone, theme, password_hash, role, created_at,
		   updated_at)
		 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		 WHERE ? = '' OR NOT EXISTS (SELECT 1 FROM users WHERE email = ?)`,
		u.Username, u.DisplayName, u.Email, u.Language, u.Timezone, u.Theme, u.PasswordHash, u.Role, now, now,
		u.Email, u.Email,
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrEmailTaken
	}
	u.ID, _ = res.LastInsertId()
	u.CreatedAt, u.UpdatedAt = now, now
	return nil
}

// ListUsers returns every user ordered by username.
func ListUsers(db *sql.DB) ([]*User, error) {
	rows, err := db.Query(`SELECT ` + userColumns + ` FROM users ORDER BY username ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUserByID returns the user with the given id (sql.ErrNoRows when absent).
func GetUserByID(db *sql.DB, id int64) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// GetUserByEmail returns the user whose notification address is email
// (compared as stored: addresses are saved lower-cased), the oldest one when
// several share it (possible only for data saved before addresses became
// unique); sql.ErrNoRows when none.
func GetUserByEmail(db *sql.DB, email string) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userColumns+` FROM users WHERE email = ? AND email <> '' ORDER BY id LIMIT 1`, email))
}

// GetUserByUsername returns the user with the given username (sql.ErrNoRows when absent).
func GetUserByUsername(db *sql.DB, username string) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

// CountUsers returns the number of users.
func CountUsers(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountAdmins returns the number of users with the admin role.
func CountAdmins(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

// UpdateUser updates the display name, preferences and role of a user
// (administrator edit). Empty preference values fall back to the defaults.
// It returns ErrEmailTaken when the address is changed to one another user
// already has (an unchanged address is always kept).
func UpdateUser(db *sql.DB, id int64, displayName, email, language, timezone, theme, role string) error {
	p := &User{Language: language, Timezone: timezone, Theme: theme}
	p.applyPreferenceDefaults()
	res, err := db.Exec(
		`UPDATE users SET display_name = ?, email = ?, language = ?, timezone = ?, theme = ?, role = ?, updated_at = ?
		 WHERE id = ? AND (email = ? OR ? = '' OR NOT EXISTS (SELECT 1 FROM users o WHERE o.email = ? AND o.id <> ?))`,
		displayName, email, p.Language, p.Timezone, p.Theme, role,
		time.Now().UTC(), id, email, email, email, id,
	)
	return emailUpdateResult(db, res, err, id)
}

// UpdateUserProfile updates the fields a user may change about themselves
// (profile page): display name, notification address, language, timezone and
// theme. Empty preference values fall back to the defaults. It returns
// ErrEmailTaken like UpdateUser.
func UpdateUserProfile(db *sql.DB, id int64, displayName, email, language, timezone, theme string) error {
	p := &User{Language: language, Timezone: timezone, Theme: theme}
	p.applyPreferenceDefaults()
	res, err := db.Exec(
		`UPDATE users SET display_name = ?, email = ?, language = ?, timezone = ?, theme = ?, updated_at = ?
		 WHERE id = ? AND (email = ? OR ? = '' OR NOT EXISTS (SELECT 1 FROM users o WHERE o.email = ? AND o.id <> ?))`,
		displayName, email, p.Language, p.Timezone, p.Theme, time.Now().UTC(), id, email, email, email, id,
	)
	return emailUpdateResult(db, res, err, id)
}

// emailUpdateResult interprets an UPDATE guarded by the address condition:
// no row changed means either the user is gone (not an error, as before) or
// the address is taken.
func emailUpdateResult(db *sql.DB, res sql.Result, err error, id int64) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return err
	}
	if _, err := GetUserByID(db, id); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	return ErrEmailTaken
}

// ListUsersByIDs returns the users with the given ids (missing ids are skipped), ordered by username.
func ListUsersByIDs(db *sql.DB, ids []int64) ([]*User, error) {
	var out []*User
	for _, id := range ids {
		u, err := GetUserByID(db, id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// UpdateUserPassword replaces the password hash of a user.
func UpdateUserPassword(db *sql.DB, id int64, passwordHash string) error {
	_, err := db.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, time.Now().UTC(), id,
	)
	return err
}

// TouchUserLogin records a successful login.
func TouchUserLogin(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// DeleteUser removes a user.
func DeleteUser(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func scanUser(s rowScanner) (*User, error) {
	u := &User{}
	if err := s.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Language, &u.Timezone, &u.Theme, &u.PasswordHash, &u.Role,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt); err != nil {
		return nil, err
	}
	return u, nil
}
