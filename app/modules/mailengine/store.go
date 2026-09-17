package mailengine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mailcare/app/models"
)

// storeOptions controls how storeMessage behaves for check vs. reindex.
type storeOptions struct {
	writeEML bool // write the .eml (check); reindex reads it instead
	// dedupeByMessageID skips a message whose Message-ID is already indexed.
	// A UIDVALIDITY change gives every message a new key, so this is what
	// keeps the re-scanned window from being indexed twice (check only).
	dedupeByMessageID bool
}

// errDuplicateMessage is returned by storeMessage when the message is already
// indexed under another key (see storeOptions.dedupeByMessageID).
var errDuplicateMessage = errors.New("mailengine: message already indexed")

// storeMessage writes the derived files of one message, inserts its index row,
// classifies it and records its bounce details and group. The group counters
// are not refreshed here; the caller does that once per run through tracker.
//
// raw is the original message, src its IMAP identity. exclude lists the
// addresses never taken as failed recipient (the monitored address).
func storeMessage(db *sql.DB, dir string, raw []byte, src Source, opts storeOptions, tracker *groupTracker,
	exclude ...string) (*models.Message, *ParsedMessage, error) {
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
	pm.Source = src
	if pm.Source.FetchedAt.IsZero() {
		pm.Source.FetchedAt = time.Now().UTC()
	}
	if pm.Source.Size == 0 {
		pm.Source.Size = int64(len(raw))
	}
	key := src.MessageKey
	if key == "" {
		key = MessageKey(keyDate(pm.Date, src.ReceivedAt, pm.Source.FetchedAt), src.Folder, src.UIDValidity, src.UID)
		pm.Source.MessageKey = key
	}

	if opts.writeEML {
		if err := writeFileAtomic(filepath.Join(dir, key+".eml"), raw, 0o600); err != nil {
			return nil, pm, fmt.Errorf("write eml: %w", err)
		}
	}
	if err := writeDerivedFiles(dir, key, pm); err != nil {
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
		// A missing or unparsable Date header falls back to INTERNALDATE and
		// then to the fetch time (the same order as the message key), so the
		// row always sorts and groups.last_seen is never NULL. The .json keeps
		// the parsed value empty and the raw header in Headers.
		Date:       models.NullTime(keyDate(pm.Date, src.ReceivedAt, pm.Source.FetchedAt)),
		ReceivedAt: models.NullTime(src.ReceivedAt),
		Size:       pm.Source.Size,
		HasText:    pm.HasText,
		HasHTML:    pm.HasHTML,
		FetchedAt:  pm.Source.FetchedAt,
	}
	cls := Classify(pm)
	var bounce *models.Bounce
	var group *models.BounceGroup
	if cls.IsBounce {
		msg.IsBounce = true
		msg.BounceKind = cls.Kind
		msg.ClassifyReason = cls.Reason
		if cls.Kind != bounceKindAutoReply {
			bounce = ExtractBounce(pm, cls.Kind, append([]string{pm.FromAddress}, exclude...)...)
			group = groupForBounce(cls.Kind, bounce)
			if group != nil {
				msg.GroupKey = group.GroupKey
			}
		}
	}
	if err := models.InsertMessage(db, msg); err != nil {
		return nil, pm, fmt.Errorf("insert message %s: %w", key, err)
	}
	if bounce != nil {
		bounce.MessageID = msg.ID
		if err := models.UpsertBounce(db, bounce); err != nil {
			return nil, pm, fmt.Errorf("insert bounce %s: %w", key, err)
		}
	}
	if err := tracker.upsert(db, group); err != nil {
		return nil, pm, fmt.Errorf("upsert group for %s: %w", key, err)
	}
	return msg, pm, nil
}

// reclassifyMessage re-runs classification, extraction and grouping for an
// existing index row (Reclassify). The bounces and groups tables were cleared
// beforehand by the caller.
func reclassifyMessage(db *sql.DB, msg *models.Message, pm *ParsedMessage, tracker *groupTracker,
	exclude ...string) error {
	cls := Classify(pm)
	var bounce *models.Bounce
	var group *models.BounceGroup
	groupKey := ""
	if cls.IsBounce && cls.Kind != bounceKindAutoReply {
		bounce = ExtractBounce(pm, cls.Kind, append([]string{pm.FromAddress}, exclude...)...)
		group = groupForBounce(cls.Kind, bounce)
		if group != nil {
			groupKey = group.GroupKey
		}
	}
	if err := models.UpdateMessageClassification(db, msg.ID, cls.IsBounce, cls.Kind, cls.Reason, groupKey); err != nil {
		return err
	}
	msg.IsBounce, msg.BounceKind, msg.ClassifyReason, msg.GroupKey = cls.IsBounce, cls.Kind, cls.Reason, groupKey
	if bounce != nil {
		bounce.MessageID = msg.ID
		if err := models.UpsertBounce(db, bounce); err != nil {
			return err
		}
	}
	return tracker.upsert(db, group)
}

// writeDerivedFiles writes <key>.txt, <key>.html (only when present, removed
// otherwise) and <key>.json.
func writeDerivedFiles(dir, key string, pm *ParsedMessage) error {
	if err := writeFileAtomic(filepath.Join(dir, key+".txt"), []byte(pm.TextBody), 0o600); err != nil {
		return fmt.Errorf("write txt: %w", err)
	}
	htmlPath := filepath.Join(dir, key+".html")
	if pm.HTMLBody != "" {
		if err := writeFileAtomic(htmlPath, []byte(pm.HTMLBody), 0o600); err != nil {
			return fmt.Errorf("write html: %w", err)
		}
	} else if err := os.Remove(htmlPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale html: %w", err)
	}
	data, err := json.MarshalIndent(pm, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, key+".json"), data, 0o600); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// loadParsedMessage reads <key>.json and <key>.txt / <key>.html of a message.
// It returns os.ErrNotExist (wrapped) when the JSON file is missing.
func loadParsedMessage(dir, key string) (*ParsedMessage, error) {
	data, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		return nil, err
	}
	pm := &ParsedMessage{}
	if err := json.Unmarshal(data, pm); err != nil {
		return nil, fmt.Errorf("decode %s.json: %w", key, err)
	}
	if pm.Headers == nil {
		pm.Headers = map[string]string{}
	}
	if b, err := os.ReadFile(filepath.Join(dir, key+".txt")); err == nil {
		pm.TextBody = string(b)
	}
	if b, err := os.ReadFile(filepath.Join(dir, key+".html")); err == nil {
		pm.HTMLBody = string(b)
	}
	pm.HasText, pm.HasHTML = pm.TextBody != "", pm.HTMLBody != ""
	return pm, nil
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
