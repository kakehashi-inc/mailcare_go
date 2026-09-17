package models

import (
	"database/sql"
	"time"
)

// Token represents a row in the tokens table.
type Token struct {
	ID         int64        `json:"-"`
	Identifier string       `json:"identifier"`
	Name       string       `json:"name"`
	Token      string       `json:"token"`
	IsDefault  bool         `json:"is_default"`
	ExpiresAt  sql.NullTime `json:"-"`
	CreatedAt  time.Time    `json:"-"`
}

// IsExpired reports whether the token has an expiry that has already passed.
func (t *Token) IsExpired() bool {
	return t.ExpiresAt.Valid && t.ExpiresAt.Time.Before(time.Now())
}

// InsertToken creates a new token row.
func InsertToken(db *sql.DB, t *Token) error {
	res, err := db.Exec(
		`INSERT INTO tokens (identifier, name, token, is_default, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.Identifier, t.Name, t.Token, boolToInt(t.IsDefault), t.ExpiresAt, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err == nil {
		t.ID = id
	}
	return nil
}

// ListTokens returns all tokens ordered by creation time.
func ListTokens(db *sql.DB) ([]*Token, error) {
	rows, err := db.Query(
		`SELECT id, identifier, name, token, is_default, expires_at, created_at
		 FROM tokens ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []*Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// GetTokenByIdentifier returns the token with the given identifier.
func GetTokenByIdentifier(db *sql.DB, identifier string) (*Token, error) {
	row := db.QueryRow(
		`SELECT id, identifier, name, token, is_default, expires_at, created_at
		 FROM tokens WHERE identifier = ?`, identifier,
	)
	return scanToken(row)
}

// GetTokenByValue returns the token matching the given bearer token value.
func GetTokenByValue(db *sql.DB, value string) (*Token, error) {
	row := db.QueryRow(
		`SELECT id, identifier, name, token, is_default, expires_at, created_at
		 FROM tokens WHERE token = ?`, value,
	)
	return scanToken(row)
}

// GetDefaultToken returns the default token, or sql.ErrNoRows if none exists.
func GetDefaultToken(db *sql.DB) (*Token, error) {
	row := db.QueryRow(
		`SELECT id, identifier, name, token, is_default, expires_at, created_at
		 FROM tokens WHERE is_default = 1 ORDER BY id ASC LIMIT 1`,
	)
	return scanToken(row)
}

// CountTokens returns the number of tokens.
func CountTokens(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM tokens`).Scan(&n)
	return n, err
}

// SetDefaultToken marks the given identifier as the only default token.
func SetDefaultToken(db *sql.DB, identifier string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tokens SET is_default = 0`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE tokens SET is_default = 1 WHERE identifier = ?`, identifier); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteToken removes a token row by identifier. Deleting the default token
// does not promote another one; the server creates a new default on its next
// start.
func DeleteToken(db *sql.DB, identifier string) error {
	_, err := db.Exec(`DELETE FROM tokens WHERE identifier = ?`, identifier)
	return err
}

// rowScanner abstracts *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanToken(s rowScanner) (*Token, error) {
	t := &Token{}
	var isDefault int
	if err := s.Scan(&t.ID, &t.Identifier, &t.Name, &t.Token, &isDefault, &t.ExpiresAt, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.IsDefault = isDefault != 0
	return t, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
