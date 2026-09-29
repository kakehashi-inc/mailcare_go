package models

import (
	"database/sql"
	"errors"
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
	BounceKind  string       `json:"bounce_kind"` // failed | delayed | auto_reply | other | report | junk | ""
	Rule        string       `json:"rule"`        // name of the detection rule that matched
	FetchedAt   time.Time    `json:"fetched_at"`
	// ServerDeletedAt is when MailCare deleted the message from the IMAP
	// server (the server retention of the mailbox); NULL while it is still
	// there. The raw file and the row stay until the local retention.
	ServerDeletedAt sql.NullTime `json:"-"`

	// GroupKey is the group of the bounce (bounces.group_key), "" when the
	// message is not a grouped bounce. It is read with the row, never written
	// through it.
	GroupKey string `json:"group_key"`
}

// messageColumns lists the columns of a messages row joined with its bounces
// row (alias m / b), in scanMessage order.
const messageColumns = `m.id, m.message_key, m.folder, m.uidvalidity, m.uid, m.message_id, m.subject, m.from_address,
	m.from_name, m.to_address, m.to_name, m.date, m.received_at, m.size, m.text_count, m.html_count, m.body_source,
	m.classified, m.is_bounce, m.bounce_kind, m.rule, m.fetched_at, m.server_deleted_at, COALESCE(b.group_key, '')`

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
	m.ServerDeletedAt = utcNullTime(m.ServerDeletedAt)
	if m.Folder == "" {
		m.Folder = "INBOX"
	}
	res, err := db.Exec(
		`INSERT INTO messages (message_key, folder, uidvalidity, uid, message_id, subject, from_address, from_name,
		   to_address, to_name, date, received_at, size, text_count, html_count, body_source, classified, is_bounce,
		   bounce_kind, rule, fetched_at, server_deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MessageKey, m.Folder, m.UIDValidity, m.UID, m.MessageID, m.Subject, m.FromAddress, m.FromName,
		m.ToAddress, m.ToName, m.Date, m.ReceivedAt, m.Size, m.TextCount, m.HTMLCount, m.BodySource,
		boolToInt(m.Classified), boolToInt(m.IsBounce), m.BounceKind, m.Rule, m.FetchedAt, m.ServerDeletedAt,
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
// or removed (DeleteBounce), and the DMARC records replaced
// (ReplaceDMARCRecords), by the caller in the same transaction.
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

// ReidentifyMessage moves an indexed message whose IMAP identity is stale to
// the identity the server gives it now (folder, uidValidity, uid), and
// reports whether one was moved. A stale row has the same Message-ID, is
// still on the server as far as MailCare knows (server_deleted_at NULL) and
// was indexed either in the folder under another UIDVALIDITY (the folder was
// re-created and numbered its mail anew) or with a synthetic identity
// (uidvalidity 0: indexed from a raw file the fetch never saw). A row whose
// size equals size is preferred (the same copy when the server holds several
// with that Message-ID), then the oldest. Rows already indexed under the
// current identity of the folder are never moved: a message with the same
// Message-ID under another UID of that folder is another message on the
// server and is indexed as its own row. The message key, the fetch facts and
// the files stay as they are.
func ReidentifyMessage(db *sql.DB, messageID, folder string, uidValidity, uid uint32, size int64) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var id int64
	err := db.QueryRow(`SELECT id FROM messages
		WHERE message_id = ? AND server_deleted_at IS NULL
		  AND ((folder = ? AND uidvalidity <> ?) OR uidvalidity = 0)
		ORDER BY (size = ?) DESC, date ASC, id ASC LIMIT 1`,
		messageID, folder, uidValidity, size).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := db.Exec(`UPDATE messages SET folder = ?, uidvalidity = ?, uid = ? WHERE id = ?`,
		folder, uidValidity, uid, id); err != nil {
		return false, err
	}
	return true, nil
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
// groups it is a member of (the group of its bounce, or the groups of the
// records of a DMARC report; none for any other message).
type ExpiredMessage struct {
	ID         int64
	MessageKey string
	TextCount  int
	HTMLCount  int
	GroupKeys  []string
}

// ListMessagesOlderThan returns the messages whose date is before cutoff,
// oldest first.
func ListMessagesOlderThan(db *sql.DB, cutoff time.Time) ([]ExpiredMessage, error) {
	rows, err := db.Query(`SELECT m.id, m.message_key, m.text_count, m.html_count, COALESCE(b.group_key, ''),
		COALESCE((SELECT GROUP_CONCAT(DISTINCT d.group_key) FROM dmarc_records d WHERE d.message_id = m.id), '')
		`+messageFrom+` WHERE m.date < ? ORDER BY m.date ASC, m.id ASC`, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExpiredMessage
	for rows.Next() {
		var m ExpiredMessage
		var bounceKey, recordKeys string
		if err := rows.Scan(&m.ID, &m.MessageKey, &m.TextCount, &m.HTMLCount, &bounceKey, &recordKeys); err != nil {
			return nil, err
		}
		if bounceKey != "" {
			m.GroupKeys = append(m.GroupKeys, bounceKey)
		}
		if recordKeys != "" {
			m.GroupKeys = append(m.GroupKeys, strings.Split(recordKeys, ",")...)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMessage removes a message row; its bounces row and its
// dmarc_records rows go with it (ON DELETE CASCADE). The files of the
// message are the caller's business.
func DeleteMessage(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM messages WHERE id = ?`, id)
	return err
}

// ServerDeletionCandidate is a message the server retention may delete from
// the IMAP server.
type ServerDeletionCandidate struct {
	ID          int64
	MessageKey  string
	MessageID   string
	UIDValidity uint32
	UID         uint32
}

// ListServerDeletionCandidates returns the messages of a folder that are
// still on the IMAP server (server_deleted_at NULL), dated before cutoff and
// classified (classified = 1) by one of rules, oldest first: the rules name
// what may be deleted (the notices with certain evidence and the junk mail,
// whatever the state of their group). The messages not classified yet and
// the ones matched by another rule (or by none: ordinary mail) are left
// out; no rule means no candidate. Messages with a synthetic identity
// (uidvalidity 0: indexed from a raw file the fetch never saw) are never
// candidates.
func ListServerDeletionCandidates(db *sql.DB, folder string, cutoff time.Time, rules []string) ([]ServerDeletionCandidate, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	args := []any{folder, cutoff.UTC()}
	for _, r := range rules {
		args = append(args, r)
	}
	rows, err := db.Query(`SELECT m.id, m.message_key, m.message_id, m.uidvalidity, m.uid
		FROM messages m
		WHERE m.classified = 1
		  AND m.server_deleted_at IS NULL AND m.folder = ?
		  AND m.uidvalidity <> 0 AND m.date < ?
		  AND m.rule IN (?`+strings.Repeat(", ?", len(rules)-1)+`)
		ORDER BY m.date ASC, m.id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServerDeletionCandidate
	for rows.Next() {
		var c ServerDeletionCandidate
		if err := rows.Scan(&c.ID, &c.MessageKey, &c.MessageID, &c.UIDValidity, &c.UID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountClassifiedByRules returns how many messages are classified
// (classified = 1) by one of rules, whether or not they are still on the
// IMAP server. No rule counts nothing.
func CountClassifiedByRules(db *sql.DB, rules []string) (int, error) {
	if len(rules) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(rules))
	for _, r := range rules {
		args = append(args, r)
	}
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages
		WHERE classified = 1
		  AND rule IN (?`+strings.Repeat(", ?", len(rules)-1)+`)`, args...).Scan(&n)
	return n, err
}

// MarkServerDeleted records that the messages were deleted from (or are no
// longer on) the IMAP server.
func MarkServerDeleted(db *sql.DB, ids []int64, at time.Time) error {
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE messages SET server_deleted_at = ? WHERE id = ?`, at.UTC(), id); err != nil {
			return err
		}
	}
	return nil
}

// MessageSource is the IMAP identity and the fetch facts of an indexed
// message, keyed by message_key (used by reindex to carry them over from the
// previous index, since the raw file does not record them).
type MessageSource struct {
	Folder          string
	UIDValidity     uint32
	UID             uint32
	Size            int64
	ReceivedAt      sql.NullTime
	FetchedAt       time.Time
	ServerDeletedAt sql.NullTime
}

// ListMessageSources returns the source facts of every message keyed by
// message_key.
func ListMessageSources(db *sql.DB) (map[string]MessageSource, error) {
	rows, err := db.Query(`SELECT message_key, folder, uidvalidity, uid, size, received_at, fetched_at, server_deleted_at FROM messages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MessageSource{}
	for rows.Next() {
		var key string
		var s MessageSource
		if err := rows.Scan(&key, &s.Folder, &s.UIDValidity, &s.UID, &s.Size, &s.ReceivedAt, &s.FetchedAt, &s.ServerDeletedAt); err != nil {
			return nil, err
		}
		out[key] = s
	}
	return out, rows.Err()
}

// Message kinds of MessageFilter.Kind.
const (
	MessageKindAll    = ""       // every message
	MessageKindTarget = "target" // classified by one of MessageFilter.TargetRules (the notices MailCare handles)
	MessageKindJunk   = "junk"   // detected as junk (bounce_kind = junk: phishing, spam)
	MessageKindOther  = "other"  // everything else (neither a target nor junk; unclassified mail included)
)

// BounceKindJunk is messages.bounce_kind of junk mail (same literal as
// mailengine's; models must not import mailengine).
const BounceKindJunk = "junk"

// MessageFilter narrows ListMessages.
type MessageFilter struct {
	Query string // matched against subject, from and to (LIKE)
	Kind  string // MessageKindAll | MessageKindTarget | MessageKindJunk | MessageKindOther
	// TargetRules are the rules of the target messages (the certain notices,
	// mailengine.TargetRules); required by the target and other kinds and
	// by CountMessagesByKind.
	TargetRules []string
	GroupKey    string
	Offset      int
	Limit       int
}

// MessageKindCounts is how many messages of each kind match a filter (its
// Kind ignored).
type MessageKindCounts struct {
	All    int `json:"all"`
	Target int `json:"target"`
	Junk   int `json:"junk"`
	Other  int `json:"other"`
}

// CountMessagesByKind counts the messages matching the filter per kind,
// whatever f.Kind says (the counts of the kind switch).
func CountMessagesByKind(db *sql.DB, f MessageFilter) (MessageKindCounts, error) {
	f.Kind = MessageKindAll
	where, args := messageWhere(f)
	target, targetArgs := targetCondition(f.TargetRules)
	var c MessageKindCounts
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(`+target+`), 0), COALESCE(SUM(m.bounce_kind = ?), 0)`+
		messageFrom+where, append(append(targetArgs, BounceKindJunk), args...)...).Scan(&c.All, &c.Target, &c.Junk)
	c.Other = c.All - c.Target - c.Junk
	return c, err
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

// ListGroupMessages returns every member message of a group (the mails of
// its bounces and the report mails of its DMARC records), newest first.
func ListGroupMessages(db *sql.DB, groupKey string) ([]*Message, error) {
	where, args := messageWhere(MessageFilter{GroupKey: groupKey})
	return queryMessages(db, `SELECT `+messageColumns+messageFrom+where+` ORDER BY m.date DESC, m.id DESC`, args...)
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
	switch f.Kind {
	case MessageKindTarget:
		target, targetArgs := targetCondition(f.TargetRules)
		conds = append(conds, target)
		args = append(args, targetArgs...)
	case MessageKindJunk:
		conds = append(conds, `m.bounce_kind = ?`)
		args = append(args, BounceKindJunk)
	case MessageKindOther:
		target, targetArgs := targetCondition(f.TargetRules)
		conds = append(conds, `NOT `+target+` AND m.bounce_kind <> ?`)
		args = append(append(args, targetArgs...), BounceKindJunk)
	}
	if f.GroupKey != "" {
		// The members of a group: the mails of its bounces and the report
		// mails of its DMARC records, each table searched by its group index.
		conds = append(conds, `(m.id IN (SELECT id FROM bounces WHERE group_key = ?)
		  OR m.id IN (SELECT message_id FROM dmarc_records WHERE group_key = ?))`)
		args = append(args, f.GroupKey, f.GroupKey)
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

// targetCondition is the condition of a target message: classified by one
// of the rules (never true without rules).
func targetCondition(rules []string) (string, []any) {
	if len(rules) == 0 {
		return `(0)`, nil
	}
	args := make([]any, 0, len(rules))
	for _, r := range rules {
		args = append(args, r)
	}
	return `(m.classified = 1 AND m.rule IN (?` + strings.Repeat(", ?", len(rules)-1) + `))`, args
}

func scanMessage(s rowScanner) (*Message, error) {
	m := &Message{}
	var classified, isBounce int
	if err := s.Scan(&m.ID, &m.MessageKey, &m.Folder, &m.UIDValidity, &m.UID, &m.MessageID, &m.Subject,
		&m.FromAddress, &m.FromName, &m.ToAddress, &m.ToName, &m.Date, &m.ReceivedAt, &m.Size, &m.TextCount,
		&m.HTMLCount, &m.BodySource, &classified, &isBounce, &m.BounceKind, &m.Rule, &m.FetchedAt, &m.ServerDeletedAt,
		&m.GroupKey); err != nil {
		return nil, err
	}
	m.Classified, m.IsBounce = classified != 0, isBounce != 0
	return m, nil
}
