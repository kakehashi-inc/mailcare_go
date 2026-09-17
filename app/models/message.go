package models

import (
	"database/sql"
	"strings"
	"time"
)

// Message is a row of the per-mailbox messages table: one fetched mail. The
// raw content lives next to the index as <message_key>.eml (original),
// <message_key>.txt (text body), <message_key>.html (HTML body, when any) and
// <message_key>.json (parsed headers).
type Message struct {
	ID             int64        `json:"id"`
	MessageKey     string       `json:"message_key"`
	UID            uint32       `json:"uid"`
	UIDValidity    uint32       `json:"uidvalidity"`
	Folder         string       `json:"folder"`
	MessageID      string       `json:"message_id"`
	Subject        string       `json:"subject"`
	FromAddress    string       `json:"from_address"`
	FromName       string       `json:"from_name"`
	ToAddress      string       `json:"to_address"`
	Date           sql.NullTime `json:"-"`
	ReceivedAt     sql.NullTime `json:"-"`
	Size           int64        `json:"size"`
	HasText        bool         `json:"has_text"`
	HasHTML        bool         `json:"has_html"`
	IsBounce       bool         `json:"is_bounce"`
	BounceKind     string       `json:"bounce_kind"`
	ClassifyReason string       `json:"classify_reason"`
	GroupKey       string       `json:"group_key"`
	Classified     bool         `json:"classified"` // false until the grouping phase processed the message
	FetchedAt      time.Time    `json:"fetched_at"`
}

const messageColumns = `id, message_key, uid, uidvalidity, folder, message_id, subject, from_address, from_name,
	to_address, date, received_at, size, has_text, has_html, is_bounce, bounce_kind, classify_reason, group_key,
	classified, fetched_at`

// InsertMessage adds a message row and fills in its ID.
func InsertMessage(db *sql.DB, m *Message) error {
	if m.FetchedAt.IsZero() {
		m.FetchedAt = time.Now().UTC()
	}
	// Store every timestamp in UTC so the textual DATETIME values sort and
	// compare correctly (MIN/MAX and ORDER BY work on the stored text).
	m.FetchedAt = m.FetchedAt.UTC()
	m.Date, m.ReceivedAt = utcNullTime(m.Date), utcNullTime(m.ReceivedAt)
	res, err := db.Exec(
		`INSERT INTO messages (message_key, uid, uidvalidity, folder, message_id, subject, from_address, from_name,
		   to_address, date, received_at, size, has_text, has_html, is_bounce, bounce_kind, classify_reason, group_key,
		   classified, fetched_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MessageKey, m.UID, m.UIDValidity, m.Folder, m.MessageID, m.Subject, m.FromAddress, m.FromName,
		m.ToAddress, m.Date, m.ReceivedAt, m.Size, boolToInt(m.HasText), boolToInt(m.HasHTML),
		boolToInt(m.IsBounce), m.BounceKind, m.ClassifyReason, m.GroupKey, boolToInt(m.Classified), m.FetchedAt,
	)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	return nil
}

// UpdateMessageClassification stores the bounce-detection outcome of a message
// and marks it as classified.
func UpdateMessageClassification(db *sql.DB, id int64, isBounce bool, bounceKind, reason, groupKey string) error {
	_, err := db.Exec(
		`UPDATE messages SET is_bounce = ?, bounce_kind = ?, classify_reason = ?, group_key = ?, classified = 1 WHERE id = ?`,
		boolToInt(isBounce), bounceKind, reason, groupKey, id,
	)
	return err
}

// ListUnclassifiedMessages returns the messages the grouping phase has not
// processed yet, oldest first.
func ListUnclassifiedMessages(db *sql.DB) ([]*Message, error) {
	rows, err := db.Query(`SELECT ` + messageColumns + ` FROM messages WHERE classified = 0 ORDER BY date ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountUnclassifiedMessages returns how many messages still await grouping.
func CountUnclassifiedMessages(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE classified = 0`).Scan(&n)
	return n, err
}

// GetMessageByKey returns one message (sql.ErrNoRows when absent).
func GetMessageByKey(db *sql.DB, key string) (*Message, error) {
	return scanMessage(db.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE message_key = ?`, key))
}

// MessageExists reports whether a message with the given IMAP identity is indexed.
func MessageExists(db *sql.DB, uidValidity, uid uint32, folder string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE uidvalidity = ? AND uid = ? AND folder = ?`,
		uidValidity, uid, folder).Scan(&n)
	return n > 0, err
}

// MaxUIDForValidity returns the highest indexed UID for the given UIDVALIDITY
// and folder (0 when none).
func MaxUIDForValidity(db *sql.DB, uidValidity uint32, folder string) (uint32, error) {
	var uid sql.NullInt64
	err := db.QueryRow(`SELECT MAX(uid) FROM messages WHERE uidvalidity = ? AND folder = ?`, uidValidity, folder).Scan(&uid)
	if err != nil {
		return 0, err
	}
	if !uid.Valid {
		return 0, nil
	}
	return uint32(uid.Int64), nil
}

// MessageFilter narrows ListMessages.
type MessageFilter struct {
	Query      string // matched against subject, from and to (LIKE)
	OnlyBounce bool
	GroupKey   string
	Offset     int
	Limit      int
}

// ListMessages returns messages newest first with the total count matching the filter.
func ListMessages(db *sql.DB, f MessageFilter) ([]*Message, int, error) {
	where, args := messageWhere(f)
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + messageColumns + ` FROM messages` + where + ` ORDER BY date DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := db.Query(q, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// ListAllMessages returns every message oldest first (used by reclassify).
func ListAllMessages(db *sql.DB) ([]*Message, error) {
	rows, err := db.Query(`SELECT ` + messageColumns + ` FROM messages ORDER BY date ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountMessages returns the total and bounce message counts.
func CountMessages(db *sql.DB) (total, bounces int, err error) {
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(is_bounce), 0) FROM messages`).Scan(&total, &bounces)
	return
}

func messageWhere(f MessageFilter) (string, []any) {
	var conds []string
	var args []any
	if f.OnlyBounce {
		conds = append(conds, `is_bounce = 1`)
	}
	if f.GroupKey != "" {
		conds = append(conds, `group_key = ?`)
		args = append(args, f.GroupKey)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + q + "%"
		conds = append(conds, `(subject LIKE ? OR from_address LIKE ? OR from_name LIKE ? OR to_address LIKE ?)`)
		args = append(args, like, like, like, like)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func scanMessage(s rowScanner) (*Message, error) {
	m := &Message{}
	var hasText, hasHTML, isBounce, classified int
	if err := s.Scan(&m.ID, &m.MessageKey, &m.UID, &m.UIDValidity, &m.Folder, &m.MessageID, &m.Subject,
		&m.FromAddress, &m.FromName, &m.ToAddress, &m.Date, &m.ReceivedAt, &m.Size, &hasText, &hasHTML, &isBounce,
		&m.BounceKind, &m.ClassifyReason, &m.GroupKey, &classified, &m.FetchedAt); err != nil {
		return nil, err
	}
	m.HasText, m.HasHTML, m.IsBounce, m.Classified = hasText != 0, hasHTML != 0, isBounce != 0, classified != 0
	return m, nil
}

// MessageIDExists reports whether a message with the given Message-ID header
// is already indexed (used to skip a message seen again after the folder's
// UIDVALIDITY changed, which gives it a new key).
func MessageIDExists(db *sql.DB, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE message_id = ?`, messageID).Scan(&n)
	return n > 0, err
}
