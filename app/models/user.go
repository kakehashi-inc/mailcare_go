package models

import (
	"database/sql"
	"time"
)

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

// InsertUser creates a user row and fills in its ID.
func InsertUser(db *sql.DB, u *User) error {
	now := time.Now().UTC()
	u.DisplayName = truncateRunes(u.DisplayName, 128)
	u.Email = truncateRunes(u.Email, 254)
	u.applyPreferenceDefaults()
	res, err := db.Exec(
		`INSERT INTO users (username, display_name, email, language, timezone, theme, password_hash, role, created_at,
		   updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, u.Email, u.Language, u.Timezone, u.Theme, u.PasswordHash, u.Role, now, now,
	)
	if err != nil {
		return err
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
func UpdateUser(db *sql.DB, id int64, displayName, email, language, timezone, theme, role string) error {
	p := &User{Language: language, Timezone: timezone, Theme: theme}
	p.applyPreferenceDefaults()
	_, err := db.Exec(
		`UPDATE users SET display_name = ?, email = ?, language = ?, timezone = ?, theme = ?, role = ?, updated_at = ?
		 WHERE id = ?`,
		truncateRunes(displayName, 128), truncateRunes(email, 254), p.Language, p.Timezone, p.Theme, role,
		time.Now().UTC(), id,
	)
	return err
}

// UpdateUserProfile updates the fields a user may change about themselves
// (profile page): display name, notification address, language, timezone and
// theme. Empty preference values fall back to the defaults.
func UpdateUserProfile(db *sql.DB, id int64, displayName, email, language, timezone, theme string) error {
	p := &User{Language: language, Timezone: timezone, Theme: theme}
	p.applyPreferenceDefaults()
	_, err := db.Exec(
		`UPDATE users SET display_name = ?, email = ?, language = ?, timezone = ?, theme = ?, updated_at = ? WHERE id = ?`,
		truncateRunes(displayName, 128), truncateRunes(email, 254), p.Language, p.Timezone, p.Theme, time.Now().UTC(), id,
	)
	return err
}

// UpdateUserEmail changes only the notification address of a user.
func UpdateUserEmail(db *sql.DB, id int64, email string) error {
	_, err := db.Exec(`UPDATE users SET email = ?, updated_at = ? WHERE id = ?`, email, time.Now().UTC(), id)
	return err
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

// DeleteUser removes a user (its tokens are removed by the foreign key cascade).
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
