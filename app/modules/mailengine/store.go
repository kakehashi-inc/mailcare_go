package mailengine

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mailcare/app/models"
)

// storeOptions controls how storeMessage behaves for fetch vs. reindex.
type storeOptions struct {
	writeEML bool // write the .eml (fetch); reindex reads it instead
	// dedupeByMessageID skips a message whose Message-ID is already indexed.
	// A UIDVALIDITY change gives every message a new key, so this is what
	// keeps the re-scanned window from being indexed twice (fetch only).
	dedupeByMessageID bool
}

// errDuplicateMessage is returned by storeMessage when the message is already
// indexed under another key (see storeOptions.dedupeByMessageID).
var errDuplicateMessage = errors.New("mailengine: message already indexed")

// storeMessage writes the body section files of one message and inserts its
// index row with classified = 0 (design 5.1). Classification, extraction
// and grouping happen later in the grouping phase (groupMessage).
//
// raw is the original message, src its IMAP identity and fetch facts (a
// zero FetchedAt means now, a zero Size the length of raw, an empty
// MessageKey lets the key be derived from the date and the identity).
func storeMessage(db *sql.DB, dir string, raw []byte, src Source, opts storeOptions) (*models.Message, *ParsedMessage, error) {
	pm := ParseMessage(raw)
	if opts.dedupeByMessageID && pm.MessageID != "" {
		exists, err := models.MessageIDExists(db, pm.MessageID)
		if err != nil {
			return nil, pm, fmt.Errorf("lookup message id: %w", err)
		}
		if exists {
			return nil, pm, errDuplicateMessage
		}
	}
	if src.FetchedAt.IsZero() {
		src.FetchedAt = time.Now().UTC()
	}
	if src.Size == 0 {
		src.Size = int64(len(raw))
	}
	if src.Folder == "" {
		src.Folder = defaultFolder
	}
	// A missing or unparsable Date header falls back to INTERNALDATE and
	// then to the fetch time (the same order as the message key), so the
	// row always sorts and groups.last_seen is never NULL. The parsed value
	// stays empty and the raw header is kept in Headers.
	date := keyDate(pm.Date, src.ReceivedAt, src.FetchedAt)
	key := src.MessageKey
	if key == "" {
		key = MessageKey(date, src.Folder, src.UIDValidity, src.UID)
	}

	if opts.writeEML {
		if !ValidMessageKey(key) {
			return nil, pm, ErrInvalidMessageKey
		}
		if err := writeFileAtomic(rawFilePath(dir, key), raw, 0o600); err != nil {
			return nil, pm, fmt.Errorf("write eml: %w", err)
		}
	}
	if err := writeBodySections(dir, key, pm); err != nil {
		return nil, pm, err
	}

	msg := &models.Message{
		MessageKey:  key,
		UID:         src.UID,
		UIDValidity: src.UIDValidity,
		Folder:      src.Folder,
		MessageID:   pm.MessageID,
		Subject:     pm.Subject,
		FromAddress: pm.FromAddress,
		FromName:    pm.FromName,
		ToAddress:   pm.To,
		ToName:      pm.ToName,
		Date:        date,
		ReceivedAt:  models.NullTime(src.ReceivedAt),
		Size:        src.Size,
		TextCount:   pm.TextCount(),
		HTMLCount:   pm.HTMLCount(),
		BodySource:  pm.BodySource,
		FetchedAt:   src.FetchedAt,
	}
	if err := models.InsertMessage(db, msg); err != nil {
		return nil, pm, fmt.Errorf("insert message %s: %w", key, err)
	}
	return msg, pm, nil
}

// writeBodySections replaces the body section files of a message (design
// 3 / 5.1): every text section is written as <key>-1.txt, <key>-2.txt, ...
// and every HTML section as <key>-1.html, <key>-2.html, ... in MIME order.
// Only sections with content exist in pm (finishBodies), so a message
// without a text or HTML body has no file of that kind.
func writeBodySections(dir, key string, pm *ParsedMessage) error {
	if !ValidMessageKey(key) {
		return ErrInvalidMessageKey
	}
	for _, kind := range []struct {
		ext      string
		sections []string
	}{{"txt", pm.TextSections}, {"html", pm.HTMLSections}} {
		for i, s := range kind.sections {
			path := SectionFilePath(dir, key, kind.ext, i+1)
			if err := writeFileAtomic(path, []byte(s), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", filepath.Base(path), err)
			}
		}
	}
	return nil
}

// readRawMessage reads the .eml of a message from the mailbox directory.
func readRawMessage(dir, key string) ([]byte, error) {
	path := rawFilePath(dir, key)
	if path == "" {
		return nil, ErrInvalidMessageKey
	}
	return os.ReadFile(path)
}

// keyDate picks the timestamp of a message key: the Date header, else the
// IMAP INTERNALDATE, else the fetch time (design 3.2).
func keyDate(date, received, fetched time.Time) time.Time {
	switch {
	case !date.IsZero():
		return date
	case !received.IsZero():
		return received
	case !fetched.IsZero():
		return fetched
	}
	return time.Now().UTC()
}

// keyFromFileName returns the message key of a raw file name ("" when the
// name is not a valid key).
func keyFromFileName(name string) string {
	key := strings.TrimSuffix(name, filepath.Ext(name))
	if !ValidMessageKey(key) {
		return ""
	}
	return key
}
