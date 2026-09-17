package models

import (
	"database/sql"
	"strings"
	"time"
)

// Message is a row of the per-mailbox messages table: one fetched mail. The
// raw content lives next to the index as <message_key>.eml (the original,
// untouched) and the decoded UTF-8 body sections as <message_key>-1.txt,
// <message_key>-2.txt, ... and <message_key>-1.html, <message_key>-2.html,
// ... (one file per non-blank section, numbered from 1 in MIME order;
// TextCount / HTMLCount say how many exist). The details extracted from a
// bounce message are the bounces row with the same id (bounce.go).
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
	TextCount   int          `json:"text_count"`  // text/plain sections with content (= .txt files)
	HTMLCount   int          `json:"html_count"`  // text/html sections with content (= .html files)
	BodySource  string       `json:"body_source"` // the body used for detection: "text" | "html" | "" (none)
	Classified  bool         `json:"classified"`  // false until the grouping phase processed the message
	IsBounce    bool         `json:"is_bounce"`
	BounceKind  string       `json:"bounce_kind"` // failed | delayed | auto_reply | other | ""
	Rule        string       `json:"rule"`        // name of the detection rule that matched
	FetchedAt   time.Time    `json:"fetched_at"`

	// GroupKey is the group of the bounce (bounces.group_key), "" when the
	// message is not a grouped bounce. It is read with the row, never written
	// through it.
	GroupKey string `json:"group_key"`
}

// messageColumns lists the columns of a messages row joined with its bounces
// row (alias m / b), in scanMessage order.
const messageColumns = `m.id, m.message_key, m.folder, m.uidvalidity, m.uid, m.message_id, m.subject, m.from_address,
	m.from_name, m.to_address, m.to_name, m.date, m.received_at, m.size, m.text_count, m.html_count, m.body_source,
	m.classified, m.is_bounce, m.bounce_kind, m.rule, m.fetched_at, COALESCE(b.group_key, '')`

const messageFrom = ` FROM messages m LEFT JOIN bounces b ON b.id = m.id`

// InsertMessage adds a message row and fills in its ID. Times are
// normalized to UTC.
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
	if m.Folder == "" {
		m.Folder = "INBOX"
	}
	res, err := db.Exec(
		`INSERT INTO messages (message_key, folder, uidvalidity, uid, message_id, subject, from_address, from_name,
		   to_address, to_name, date, received_at, size, text_count, html_count, body_source, classified, is_bounce,
		   bounce_kind, rule, fetched_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MessageKey, m.Folder, m.UIDValidity, m.UID, m.MessageID, m.Subject, m.FromAddress, m.FromName,
		m.ToAddress, m.ToName, m.Date, m.ReceivedAt, m.Size, m.TextCount, m.HTMLCount, m.BodySource,
		boolToInt(m.Classified), boolToInt(m.IsBounce), m.BounceKind, m.Rule, m.FetchedAt,
	)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	return nil
}

// UpdateMessageClassification stores the detection outcome of a message
// (is_bounce, kind, the rule that matched, the body actually used) and marks
// it as classified. The bounce details are stored separately (UpsertBounce)
// or removed (DeleteBounce) by the caller in the same transaction.
func UpdateMessageClassification(db Execer, id int64, isBounce bool, bounceKind, rule, bodySource string) error {
	_, err := db.Exec(`UPDATE messages SET is_bounce = ?, bounce_kind = ?, rule = ?, body_source = ?, classified = 1 WHERE id = ?`,
		boolToInt(isBounce), bounceKind, rule, bodySource, id)
	return err
}

// ListUnclassifiedMessages returns the messages the grouping phase has not
// processed yet, oldest first.
func ListUnclassifiedMessages(db *sql.DB) ([]*Message, error) {
	return queryMessages(db, `SELECT `+messageColumns+messageFrom+` WHERE m.classified = 0 ORDER BY m.date ASC, m.id ASC`)
}

// CountUnclassifiedMessages returns how many messages still await grouping.
func CountUnclassifiedMessages(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE classified = 0`).Scan(&n)
	return n, err
}

// GetMessageByKey returns one message (sql.ErrNoRows when absent).
func GetMessageByKey(db *sql.DB, key string) (*Message, error) {
	return scanMessage(db.QueryRow(`SELECT `+messageColumns+messageFrom+` WHERE m.message_key = ?`, key))
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

// ExpiredMessage is what the mail retention (mailengine.PruneMailbox) needs
// of a message older than the retention: its id, the key its files are
// named after, how many body section files of each kind exist and the
// group of its bounce ("" when the message is not a grouped bounce).
type ExpiredMessage struct {
	ID         int64
	MessageKey string
	TextCount  int
	HTMLCount  int
	GroupKey   string
}

// ListMessagesOlderThan returns the messages whose date is before cutoff,
// oldest first.
func ListMessagesOlderThan(db *sql.DB, cutoff time.Time) ([]ExpiredMessage, error) {
	rows, err := db.Query(`SELECT m.id, m.message_key, m.text_count, m.html_count, COALESCE(b.group_key, '')`+
		messageFrom+` WHERE m.date < ? ORDER BY m.date ASC, m.id ASC`, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExpiredMessage
	for rows.Next() {
		var m ExpiredMessage
		if err := rows.Scan(&m.ID, &m.MessageKey, &m.TextCount, &m.HTMLCount, &m.GroupKey); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMessage removes a message row; its bounces row goes with it (ON
// DELETE CASCADE). The files of the message are the caller's business.
func DeleteMessage(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM messages WHERE id = ?`, id)
	return err
}

// MessageSource is the IMAP identity and the fetch facts of an indexed
// message, keyed by message_key (used by reindex to carry them over from the
// previous index, since the raw file does not record them).
type MessageSource struct {
	Folder      string
	UIDValidity uint32
	UID         uint32
	Size        int64
	ReceivedAt  sql.NullTime
	FetchedAt   time.Time
}

// ListMessageSources returns the source facts of every message keyed by
// message_key.
func ListMessageSources(db *sql.DB) (map[string]MessageSource, error) {
	rows, err := db.Query(`SELECT message_key, folder, uidvalidity, uid, size, received_at, fetched_at FROM messages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MessageSource{}
	for rows.Next() {
		var key string
		var s MessageSource
		if err := rows.Scan(&key, &s.Folder, &s.UIDValidity, &s.UID, &s.Size, &s.ReceivedAt, &s.FetchedAt); err != nil {
			return nil, err
		}
		out[key] = s
	}
	return out, rows.Err()
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
	if err := db.QueryRow(`SELECT COUNT(*)`+messageFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	out, err := queryMessages(db, `SELECT `+messageColumns+messageFrom+where+
		` ORDER BY m.date DESC, m.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	return out, total, err
}

// ListAllMessages returns every message oldest first.
func ListAllMessages(db *sql.DB) ([]*Message, error) {
	return queryMessages(db, `SELECT `+messageColumns+messageFrom+` ORDER BY m.date ASC, m.id ASC`)
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
		conds = append(conds, `m.is_bounce = 1`)
	}
	if f.GroupKey != "" {
		conds = append(conds, `b.group_key = ?`)
		args = append(args, f.GroupKey)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := likeContains(q)
		conds = append(conds, `(m.subject LIKE ?`+likeEscapeClause+` OR m.from_address LIKE ?`+likeEscapeClause+
			` OR m.from_name LIKE ?`+likeEscapeClause+` OR m.to_address LIKE ?`+likeEscapeClause+`)`)
		args = append(args, like, like, like, like)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func scanMessage(s rowScanner) (*Message, error) {
	m := &Message{}
	var classified, isBounce int
	if err := s.Scan(&m.ID, &m.MessageKey, &m.Folder, &m.UIDValidity, &m.UID, &m.MessageID, &m.Subject,
		&m.FromAddress, &m.FromName, &m.ToAddress, &m.ToName, &m.Date, &m.ReceivedAt, &m.Size, &m.TextCount,
		&m.HTMLCount, &m.BodySource, &classified, &isBounce, &m.BounceKind, &m.Rule, &m.FetchedAt, &m.GroupKey); err != nil {
		return nil, err
	}
	m.Classified, m.IsBounce = classified != 0, isBounce != 0
	return m, nil
}
