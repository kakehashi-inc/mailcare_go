package models

import (
	"database/sql"
	"time"
)

// Token is a row of the tokens table: a login token that belongs to a user and
// can be used instead of the password to open a Web session.
type Token struct {
	ID         int64        `json:"id"`
	UserID     int64        `json:"user_id"`
	Identifier string       `json:"identifier"`
	Name       string       `json:"name"`
	Token      string       `json:"-"`
	ExpiresAt  sql.NullTime `json:"-"`
	CreatedAt  time.Time    `json:"created_at"`
	LastUsedAt sql.NullTime `json:"-"`
}

// IsExpired reports whether the token has an expiry that has already passed.
func (t *Token) IsExpired() bool {
	return t.ExpiresAt.Valid && t.ExpiresAt.Time.Before(time.Now())
}

const tokenColumns = `id, user_id, identifier, name, token, expires_at, created_at, last_used_at`

// InsertToken creates a new token row and fills in its ID.
func InsertToken(db *sql.DB, t *Token) error {
	now := time.Now().UTC()
	res, err := db.Exec(
		`INSERT INTO tokens (user_id, identifier, name, token, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		t.UserID, t.Identifier, t.Name, t.Token, t.ExpiresAt, now,
	)
	if err != nil {
		return err
	}
	t.ID, _ = res.LastInsertId()
	t.CreatedAt = now
	return nil
}

// ListTokens returns all tokens ordered by creation time.
func ListTokens(db *sql.DB) ([]*Token, error) {
	return queryTokens(db, `SELECT `+tokenColumns+` FROM tokens ORDER BY created_at ASC, id ASC`)
}

// ListTokensByUser returns the tokens of one user ordered by creation time.
func ListTokensByUser(db *sql.DB, userID int64) ([]*Token, error) {
	return queryTokens(db, `SELECT `+tokenColumns+` FROM tokens WHERE user_id = ? ORDER BY created_at ASC, id ASC`, userID)
}

func queryTokens(db *sql.DB, query string, args ...any) ([]*Token, error) {
	rows, err := db.Query(query, args...)
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
	return scanToken(db.QueryRow(`SELECT `+tokenColumns+` FROM tokens WHERE identifier = ?`, identifier))
}

// GetTokenByValue returns the token matching the given token value.
func GetTokenByValue(db *sql.DB, value string) (*Token, error) {
	return scanToken(db.QueryRow(`SELECT `+tokenColumns+` FROM tokens WHERE token = ?`, value))
}

// CountTokens returns the number of tokens.
func CountTokens(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM tokens`).Scan(&n)
	return n, err
}

// TouchTokenUse records that a token was just used.
func TouchTokenUse(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE tokens SET last_used_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// DeleteToken removes a token row by identifier.
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
	if err := s.Scan(&t.ID, &t.UserID, &t.Identifier, &t.Name, &t.Token, &t.ExpiresAt, &t.CreatedAt, &t.LastUsedAt); err != nil {
		return nil, err
	}
	return t, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
