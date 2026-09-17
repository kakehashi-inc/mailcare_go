package models

import (
	"database/sql"
	"time"
)

// Mailbox is a row of the mailboxes table: one monitored mail address. The
// mail server connection settings are stored together in the detail_info JSON
// column (mailboxDetails, keyed by "protocol": only "imap" today, so a POP3
// account later needs no schema change); the password inside it is encrypted
// (ImapPasswordEnc) and decrypted in app/modules (secret.go).
type Mailbox struct {
	ID             int64        `json:"id"`
	Address        string       `json:"address"`
	DisplayName    string       `json:"display_name"`
	Enabled        bool         `json:"enabled"`
	LastFetchedAt  sql.NullTime `json:"-"`
	LastFetchError string       `json:"last_fetch_error"` // "" = the last fetch succeeded
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`

	// Stored in the detail_info JSON column (protocol "imap").
	ImapHost        string `json:"imap_host"`
	ImapPort        int    `json:"imap_port"`
	ImapSecurity    string `json:"imap_security"` // ssl | starttls | none
	ImapUsername    string `json:"imap_username"`
	ImapPasswordEnc string `json:"-"`
	Folder          string `json:"folder"`
	InitialDays     int    `json:"initial_days"`
	RecentDays      int    `json:"recent_days"`
}

// ProtocolIMAP is the value of the "protocol" key in mailboxes.detail_info.
const ProtocolIMAP = "imap"

// mailboxDetails is the JSON shape of mailboxes.detail_info.
type mailboxDetails struct {
	Protocol    string `json:"protocol"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	PasswordEnc string `json:"password_enc"`
	Folder      string `json:"folder"`
	InitialDays int    `json:"initial_days"`
	RecentDays  int    `json:"recent_days"`
}

func (m *Mailbox) detailsJSON() string {
	return marshalJSON(mailboxDetails{
		Protocol: ProtocolIMAP, Host: truncateRunes(m.ImapHost, 253), Port: m.ImapPort, Security: m.ImapSecurity,
		Username: truncateRunes(m.ImapUsername, 254), PasswordEnc: m.ImapPasswordEnc,
		Folder: truncateRunes(m.Folder, 255), InitialDays: m.InitialDays, RecentDays: m.RecentDays,
	})
}

func (m *Mailbox) applyDetails(raw string) {
	var j mailboxDetails
	unmarshalJSON(raw, &j)
	m.ImapHost, m.ImapPort, m.ImapSecurity, m.ImapUsername = j.Host, j.Port, j.Security, j.Username
	m.ImapPasswordEnc, m.Folder, m.InitialDays, m.RecentDays = j.PasswordEnc, j.Folder, j.InitialDays, j.RecentDays
}

const mailboxColumns = `id, address, display_name, enabled, detail_info, last_fetched_at, last_fetch_error, created_at, updated_at`

// InsertMailbox creates a mailbox row and fills in its ID.
func InsertMailbox(db *sql.DB, m *Mailbox) error {
	now := time.Now().UTC()
	m.DisplayName = truncateRunes(m.DisplayName, 128)
	res, err := db.Exec(
		`INSERT INTO mailboxes (address, display_name, enabled, detail_info, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		m.Address, m.DisplayName, boolToInt(m.Enabled), m.detailsJSON(), now, now,
	)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	m.CreatedAt, m.UpdatedAt = now, now
	return nil
}

// UpdateMailbox updates every editable field of a mailbox. When
// ImapPasswordEnc is empty the stored password is kept.
func UpdateMailbox(db *sql.DB, m *Mailbox) error {
	if m.ImapPasswordEnc == "" {
		cur, err := GetMailboxByID(db, m.ID)
		if err != nil {
			return err
		}
		m.ImapPasswordEnc = cur.ImapPasswordEnc
	}
	now := time.Now().UTC()
	m.DisplayName = truncateRunes(m.DisplayName, 128)
	_, err := db.Exec(
		`UPDATE mailboxes SET address = ?, display_name = ?, enabled = ?, detail_info = ?, updated_at = ? WHERE id = ?`,
		m.Address, m.DisplayName, boolToInt(m.Enabled), m.detailsJSON(), now, m.ID,
	)
	if err == nil {
		m.UpdatedAt = now
	}
	return err
}

// UpdateMailboxFetchResult records the outcome of a fetch run: the time and
// the error text ("" on success).
func UpdateMailboxFetchResult(db *sql.DB, id int64, errMsg string) error {
	_, err := db.Exec(`UPDATE mailboxes SET last_fetched_at = ?, last_fetch_error = ? WHERE id = ?`,
		time.Now().UTC(), truncateRunes(errMsg, 2000), id)
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
	var details string
	if err := s.Scan(&m.ID, &m.Address, &m.DisplayName, &enabled, &details, &m.LastFetchedAt, &m.LastFetchError,
		&m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Enabled = enabled != 0
	m.applyDetails(details)
	return m, nil
}
