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
	PasswordHash string       `json:"-"`
	Role         string       `json:"role"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	LastLoginAt  sql.NullTime `json:"-"`
}

const userColumns = `id, username, display_name, password_hash, role, created_at, updated_at, last_login_at`

// InsertUser creates a user row and fills in its ID.
func InsertUser(db *sql.DB, u *User) error {
	now := time.Now().UTC()
	res, err := db.Exec(
		`INSERT INTO users (username, display_name, password_hash, role, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, u.PasswordHash, u.Role, now, now,
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

// UpdateUser updates the display name and role of a user.
func UpdateUser(db *sql.DB, id int64, displayName, role string) error {
	_, err := db.Exec(
		`UPDATE users SET display_name = ?, role = ?, updated_at = ? WHERE id = ?`,
		displayName, role, time.Now().UTC(), id,
	)
	return err
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
	if err := s.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt); err != nil {
		return nil, err
	}
	return u, nil
}
