package models

import (
	"database/sql"
	"time"
)

// Mailbox is a row of the mailboxes table: one monitored mail address with its
// IMAP connection settings. The IMAP password is stored encrypted
// (ImapPasswordEnc); decryption happens in app/modules (secret.go).
type Mailbox struct {
	ID              int64        `json:"id"`
	Address         string       `json:"address"`
	DisplayName     string       `json:"display_name"`
	ImapHost        string       `json:"imap_host"`
	ImapPort        int          `json:"imap_port"`
	ImapSecurity    string       `json:"imap_security"`
	ImapUsername    string       `json:"imap_username"`
	ImapPasswordEnc string       `json:"-"`
	Folder          string       `json:"folder"`
	Enabled         bool         `json:"enabled"`
	InitialDays     int          `json:"initial_days"`
	RecentDays      int          `json:"recent_days"`
	LastCheckedAt   sql.NullTime `json:"-"`
	LastCheckStatus string       `json:"last_check_status"`
	LastCheckError  string       `json:"last_check_error"`
	LastUIDValidity int64        `json:"-"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

const mailboxColumns = `id, address, display_name, imap_host, imap_port, imap_security, imap_username,
	imap_password_enc, folder, enabled, initial_days, recent_days, last_checked_at, last_check_status,
	last_check_error, last_uidvalidity, created_at, updated_at`

// InsertMailbox creates a mailbox row and fills in its ID.
func InsertMailbox(db *sql.DB, m *Mailbox) error {
	now := time.Now().UTC()
	res, err := db.Exec(
		`INSERT INTO mailboxes (address, display_name, imap_host, imap_port, imap_security, imap_username,
		   imap_password_enc, folder, enabled, initial_days, recent_days, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Address, m.DisplayName, m.ImapHost, m.ImapPort, m.ImapSecurity, m.ImapUsername,
		m.ImapPasswordEnc, m.Folder, boolToInt(m.Enabled), m.InitialDays, m.RecentDays, now, now,
	)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	m.CreatedAt, m.UpdatedAt = now, now
	return nil
}

// UpdateMailbox updates every editable column of a mailbox. When
// ImapPasswordEnc is empty the stored password is kept.
func UpdateMailbox(db *sql.DB, m *Mailbox) error {
	now := time.Now().UTC()
	var err error
	if m.ImapPasswordEnc == "" {
		_, err = db.Exec(
			`UPDATE mailboxes SET address = ?, display_name = ?, imap_host = ?, imap_port = ?, imap_security = ?,
			   imap_username = ?, folder = ?, enabled = ?, initial_days = ?, recent_days = ?, updated_at = ?
			 WHERE id = ?`,
			m.Address, m.DisplayName, m.ImapHost, m.ImapPort, m.ImapSecurity, m.ImapUsername, m.Folder,
			boolToInt(m.Enabled), m.InitialDays, m.RecentDays, now, m.ID,
		)
	} else {
		_, err = db.Exec(
			`UPDATE mailboxes SET address = ?, display_name = ?, imap_host = ?, imap_port = ?, imap_security = ?,
			   imap_username = ?, imap_password_enc = ?, folder = ?, enabled = ?, initial_days = ?, recent_days = ?,
			   updated_at = ?
			 WHERE id = ?`,
			m.Address, m.DisplayName, m.ImapHost, m.ImapPort, m.ImapSecurity, m.ImapUsername, m.ImapPasswordEnc,
			m.Folder, boolToInt(m.Enabled), m.InitialDays, m.RecentDays, now, m.ID,
		)
	}
	if err == nil {
		m.UpdatedAt = now
	}
	return err
}

// UpdateMailboxCheckResult records the outcome of a check run.
func UpdateMailboxCheckResult(db *sql.DB, id int64, status, errMsg string, uidValidity int64) error {
	_, err := db.Exec(
		`UPDATE mailboxes SET last_checked_at = ?, last_check_status = ?, last_check_error = ?, last_uidvalidity = ?
		 WHERE id = ?`,
		time.Now().UTC(), status, errMsg, uidValidity, id,
	)
	return err
}

// ResetMailboxCheckState clears the last-check bookkeeping (used by reindex so
// the next check re-scans the initial window when the index was rebuilt).
func ResetMailboxCheckState(db *sql.DB, id int64) error {
	_, err := db.Exec(
		`UPDATE mailboxes SET last_checked_at = NULL, last_check_status = '', last_check_error = '', last_uidvalidity = 0
		 WHERE id = ?`, id,
	)
	return err
}

// ListMailboxes returns every mailbox ordered by address.
func ListMailboxes(db *sql.DB) ([]*Mailbox, error) {
	rows, err := db.Query(`SELECT ` + mailboxColumns + ` FROM mailboxes ORDER BY address ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Mailbox
	for rows.Next() {
		m, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMailboxByID returns the mailbox with the given id (sql.ErrNoRows when absent).
func GetMailboxByID(db *sql.DB, id int64) (*Mailbox, error) {
	return scanMailbox(db.QueryRow(`SELECT `+mailboxColumns+` FROM mailboxes WHERE id = ?`, id))
}

// GetMailboxByAddress returns the mailbox with the given address (sql.ErrNoRows when absent).
func GetMailboxByAddress(db *sql.DB, address string) (*Mailbox, error) {
	return scanMailbox(db.QueryRow(`SELECT `+mailboxColumns+` FROM mailboxes WHERE address = ?`, address))
}

// DeleteMailbox removes a mailbox row. The raw mail files and the index are
// removed by the caller (app/modules).
func DeleteMailbox(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM mailboxes WHERE id = ?`, id)
	return err
}

func scanMailbox(s rowScanner) (*Mailbox, error) {
	m := &Mailbox{}
	var enabled int
	if err := s.Scan(&m.ID, &m.Address, &m.DisplayName, &m.ImapHost, &m.ImapPort, &m.ImapSecurity, &m.ImapUsername,
		&m.ImapPasswordEnc, &m.Folder, &enabled, &m.InitialDays, &m.RecentDays, &m.LastCheckedAt, &m.LastCheckStatus,
		&m.LastCheckError, &m.LastUIDValidity, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Enabled = enabled != 0
	return m, nil
}
