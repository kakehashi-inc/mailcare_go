package models

import (
	"database/sql"
	"strings"
	"time"
)

// Message is a row of the per-mailbox messages table: one fetched mail. The
// raw content lives next to the index as <message_key>.eml (original),
// <message_key>.txt (text body), <message_key>.html (HTML body, when any) and
// <message_key>.json (parsed headers). The bounce details of a bounce message
// are stored in the detail_info JSON column (see bounce.go), which also keeps the
// name of the detection rule that matched (ClassifyReason).
type Message struct {
	ID          int64        `json:"id"`
	MessageKey  string       `json:"message_key"`
	Folder      string       `json:"folder"`
	UIDValidity uint32       `json:"uidvalidity"`
	UID         uint32       `json:"uid"`
	MessageID   string       `json:"message_id"`
	Subject     string       `json:"subject"`
	FromAddress string       `json:"from_address"`
	FromName    string       `json:"from_name"`
	ToAddress   string       `json:"to_address"`
	ToName      string       `json:"to_name"`
	Date        time.Time    `json:"date"` // header Date, else INTERNALDATE, else fetch time (never zero)
	ReceivedAt  sql.NullTime `json:"-"`    // IMAP INTERNALDATE
	Size        int64        `json:"size"`
	HasText     bool         `json:"has_text"`    // the text/plain part carries a non-blank body
	HasHTML     bool         `json:"has_html"`    // the text/html part carries a non-blank body
	BodySource  string       `json:"body_source"` // the body used for detection: "text" | "html" | "" (none)
	IsBounce    bool         `json:"is_bounce"`
	BounceKind  string       `json:"bounce_kind"` // failed | delayed | auto_reply | other | ""
	Classified  bool         `json:"classified"`  // false until the grouping phase processed the message
	GroupKey    string       `json:"group_key"`
	FetchedAt   time.Time    `json:"fetched_at"`

	// ClassifyReason is the name of the detection rule that matched. It is
	// stored inside the detail_info JSON column ("rule") and loaded with the row.
	ClassifyReason string `json:"classify_reason"`
}

const messageColumns = `id, message_key, folder, uidvalidity, uid, message_id, subject, from_address, from_name,
	to_address, to_name, date, received_at, size, has_text, has_html, body_source, is_bounce, bounce_kind, classified,
	group_key, json_extract(detail_info, '$.rule'), fetched_at`

// InsertMessage adds a message row and fills in its ID. Text values are
// truncated to the column bounds and times normalized to UTC.
func InsertMessage(db *sql.DB, m *Message) error {
	if m.FetchedAt.IsZero() {
		m.FetchedAt = time.Now()
	}
	m.FetchedAt = m.FetchedAt.UTC().Round(0)
	if m.Date.IsZero() {
		m.Date = m.FetchedAt
	}
	m.Date = m.Date.UTC().Round(0)
	m.ReceivedAt = utcNullTime(m.ReceivedAt)
	m.Folder = truncateRunes(m.Folder, 255)
	if m.Folder == "" {
		m.Folder = "INBOX"
	}
	m.MessageID = truncateRunes(m.MessageID, 998)
	m.Subject = truncateRunes(m.Subject, 2000)
	m.FromAddress = truncateRunes(m.FromAddress, 320)
	m.FromName = truncateRunes(m.FromName, 500)
	m.ToAddress = truncateRunes(m.ToAddress, 320)
	m.ToName = truncateRunes(m.ToName, 500)
	details := "{}"
	if m.ClassifyReason != "" {
		details = marshalJSON(map[string]string{"rule": truncateRunes(m.ClassifyReason, 64)})
	}
	res, err := db.Exec(
		`INSERT INTO messages (message_key, folder, uidvalidity, uid, message_id, subject, from_address, from_name,
		   to_address, to_name, date, received_at, size, has_text, has_html, body_source, is_bounce, bounce_kind,
		   classified, group_key, detail_info, fetched_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MessageKey, m.Folder, m.UIDValidity, m.UID, m.MessageID, m.Subject, m.FromAddress, m.FromName,
		m.ToAddress, m.ToName, m.Date, m.ReceivedAt, m.Size, boolToInt(m.HasText), boolToInt(m.HasHTML), m.BodySource,
		boolToInt(m.IsBounce), m.BounceKind, boolToInt(m.Classified), m.GroupKey, details, m.FetchedAt,
	)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	return nil
}

// UpdateMessageClassification stores the bounce-detection outcome of a message
// and marks it as classified. The rule name goes into the bounce JSON; a
// non-bounce keeps only that rule name there (details are cleared).
func UpdateMessageClassification(db *sql.DB, id int64, isBounce bool, bounceKind, reason, groupKey string) error {
	reason = truncateRunes(reason, 64)
	q := `UPDATE messages SET is_bounce = ?, bounce_kind = ?, classified = 1, group_key = ?, detail_info = json_set(detail_info, '$.rule', ?)`
	args := []any{boolToInt(isBounce), bounceKind, groupKey, reason}
	if !isBounce {
		q = `UPDATE messages SET is_bounce = ?, bounce_kind = ?, classified = 1, group_key = ?, detail_info = json_object('rule', ?)`
	}
	_, err := db.Exec(q+` WHERE id = ?`, append(args, id)...)
	return err
}

// ListUnclassifiedMessages returns the messages the grouping phase has not
// processed yet, oldest first.
func ListUnclassifiedMessages(db *sql.DB) ([]*Message, error) {
	return queryMessages(db, `SELECT `+messageColumns+` FROM messages WHERE classified = 0 ORDER BY date ASC, id ASC`)
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
	err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE folder = ? AND uidvalidity = ? AND uid = ?`,
		folder, uidValidity, uid).Scan(&n)
	return n > 0, err
}

// MessageIDExists reports whether a message with the given Message-ID header is
// indexed (used to skip duplicates after a UIDVALIDITY change).
func MessageIDExists(db *sql.DB, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE message_id = ?`, messageID).Scan(&n)
	return n > 0, err
}

// MaxUIDForValidity returns the highest indexed UID for the given UIDVALIDITY
// and folder (0 when none).
func MaxUIDForValidity(db *sql.DB, uidValidity uint32, folder string) (uint32, error) {
	var uid sql.NullInt64
	err := db.QueryRow(`SELECT MAX(uid) FROM messages WHERE folder = ? AND uidvalidity = ?`, folder, uidValidity).Scan(&uid)
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
	out, err := queryMessages(db, `SELECT `+messageColumns+` FROM messages`+where+
		` ORDER BY date DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	return out, total, err
}

// ListAllMessages returns every message oldest first (used by reclassify).
func ListAllMessages(db *sql.DB) ([]*Message, error) {
	return queryMessages(db, `SELECT `+messageColumns+` FROM messages ORDER BY date ASC, id ASC`)
}

// CountMessages returns the total and bounce message counts.
func CountMessages(db *sql.DB) (total, bounces int, err error) {
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(is_bounce), 0) FROM messages`).Scan(&total, &bounces)
	return
}

func queryMessages(db *sql.DB, query string, args ...any) ([]*Message, error) {
	rows, err := db.Query(query, args...)
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
	var rule sql.NullString
	if err := s.Scan(&m.ID, &m.MessageKey, &m.Folder, &m.UIDValidity, &m.UID, &m.MessageID, &m.Subject,
		&m.FromAddress, &m.FromName, &m.ToAddress, &m.ToName, &m.Date, &m.ReceivedAt, &m.Size, &hasText, &hasHTML,
		&m.BodySource, &isBounce, &m.BounceKind, &classified, &m.GroupKey, &rule, &m.FetchedAt); err != nil {
		return nil, err
	}
	m.HasText, m.HasHTML, m.IsBounce, m.Classified = hasText != 0, hasHTML != 0, isBounce != 0, classified != 0
	m.ClassifyReason = rule.String
	return m, nil
}
