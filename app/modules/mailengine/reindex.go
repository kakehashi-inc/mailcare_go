package mailengine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mailcare/app/models"
)

// OpenIndex opens the per-mailbox index. When the file carries an outdated
// schema it is rebuilt from the raw files first (which can take a while).
func OpenIndex(ctx context.Context, mailsRoot, address string, progress Progress) (*sql.DB, error) {
	path := MailboxIndexPath(mailsRoot, address)
	db, err := models.OpenMailIndex(path)
	if err == nil {
		return db, nil
	}
	if !errors.Is(err, models.ErrMailIndexOutdated) {
		return nil, fmt.Errorf("open index %s: %w", filepath.Base(path), err)
	}
	report(progress, "index schema is outdated; rebuilding from the raw files")
	if _, err := Reindex(ctx, mailsRoot, address, progress); err != nil {
		return nil, err
	}
	db, err = models.OpenMailIndex(path)
	if err != nil {
		return nil, fmt.Errorf("open rebuilt index %s: %w", filepath.Base(path), err)
	}
	return db, nil
}

// Reindex rebuilds the index from the raw .eml files: parse, classify and
// group everything again. The group state (open / resolved / ignored with its
// timestamp), the needs_analysis flag and the agent reports of the previous
// index are carried over for every group key that exists in the rebuilt
// index (keys are deterministic); a group whose message count grew is flagged
// for analysis again, and the responsible party named by the latest completed
// report is re-applied. Groups that no longer exist lose their reports. When
// the previous index cannot be read nothing is carried over.
func Reindex(ctx context.Context, mailsRoot, address string, progress Progress) (*ReindexResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := MailboxDir(mailsRoot, address)
	indexPath := MailboxIndexPath(mailsRoot, address)
	carried, err := readCarryover(indexPath)
	if err != nil {
		report(progress, fmt.Sprintf("previous index not readable; states and reports are not carried over: %v", err))
		carried = nil
	}
	// Build into a scratch file next to the index and swap it in at the end,
	// so an interrupted rebuild leaves the previous index untouched.
	buildPath := indexPath + ".rebuild"
	if err := removeIndexFiles(buildPath); err != nil {
		return nil, fmt.Errorf("remove stale build index: %w", err)
	}
	db, err := models.OpenMailIndex(buildPath)
	if err != nil {
		return nil, fmt.Errorf("create index: %w", err)
	}
	swapped := false
	defer func() {
		if !swapped {
			db.Close()
			_ = removeIndexFiles(buildPath)
		}
	}()

	keys, err := listRawKeys(dir)
	if err != nil {
		return nil, err
	}
	report(progress, fmt.Sprintf("reindexing %d raw messages", len(keys)))
	result := &ReindexResult{}
	tracker := newGroupTracker()
	exclude := []string{address}
	for i, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(filepath.Join(dir, key+".eml"))
		if err != nil {
			report(progress, fmt.Sprintf("skipping %s: %v", key, err))
			continue
		}
		src := sourceForKey(dir, key)
		msg, _, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: false}, tracker, exclude...)
		if err != nil {
			if isUniqueViolation(err) {
				report(progress, fmt.Sprintf("skipping duplicate %s", key))
				continue
			}
			return nil, err
		}
		result.Messages++
		if msg.IsBounce {
			result.Bounces++
		}
		if (i+1)%progressInterval == 0 {
			report(progress, fmt.Sprintf("reindexed %d of %d messages", i+1, len(keys)))
		}
	}
	groups, err := finishGroups(db, tracker)
	if err != nil {
		return nil, err
	}
	result.Groups = groups
	if carried != nil {
		g, r, err := carried.apply(db)
		if err != nil {
			return nil, err
		}
		if err := reapplyReportResponsible(db); err != nil {
			return nil, fmt.Errorf("reapply responsible: %w", err)
		}
		report(progress, fmt.Sprintf("carried over %d group states and %d agent reports", g, r))
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("close index: %w", err)
	}
	if err := swapIndexFiles(buildPath, indexPath); err != nil {
		return nil, err
	}
	swapped = true
	report(progress, fmt.Sprintf("reindexed %d messages, %d bounces, %d groups", result.Messages, result.Bounces, result.Groups))
	return result, nil
}

// swapIndexFiles replaces the index at dst with the freshly built one at src
// (including a WAL side file that may still exist after Close).
func swapIndexFiles(src, dst string) error {
	if err := removeIndexFiles(dst); err != nil {
		return fmt.Errorf("remove old index: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("install rebuilt index: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(src + suffix); err == nil {
			if err := os.Rename(src+suffix, dst+suffix); err != nil {
				return fmt.Errorf("install rebuilt index%s: %w", suffix, err)
			}
		}
	}
	return nil
}

// Reclassify keeps the messages but re-runs bounce detection and grouping from
// the raw files (used when the detection rules change). Groups and agent
// reports are kept: a group whose key comes back keeps its state, its reports
// and its needs_analysis flag (set again only when its message count grew, as
// RefreshGroupCounters does), the responsible party named by its latest
// completed report is re-applied, and groups that end up without messages are
// deleted together with their reports.
func Reclassify(ctx context.Context, mailsRoot, address string, progress Progress) (*ReindexResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := MailboxDir(mailsRoot, address)
	db, err := OpenIndex(ctx, mailsRoot, address, progress)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if err := models.ClearMailClassification(db); err != nil {
		return nil, fmt.Errorf("clear classification: %w", err)
	}
	msgs, err := models.ListAllMessages(db)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	report(progress, fmt.Sprintf("reclassifying %d messages", len(msgs)))
	result := &ReindexResult{}
	tracker := newGroupTracker()
	exclude := []string{address}
	for i, msg := range msgs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pm, err := loadParsedMessage(dir, msg.MessageKey)
		if err != nil {
			// No usable .json: parse the original again and refresh the
			// derived files while at it.
			raw, rerr := os.ReadFile(filepath.Join(dir, msg.MessageKey+".eml"))
			if rerr != nil {
				report(progress, fmt.Sprintf("skipping %s: %v", msg.MessageKey, rerr))
				continue
			}
			pm = ParseMessage(raw)
			pm.Source = Source{
				MessageKey: msg.MessageKey, Folder: msg.Folder, UIDValidity: msg.UIDValidity, UID: msg.UID,
				Size: msg.Size, ReceivedAt: msg.ReceivedAt.Time, FetchedAt: msg.FetchedAt,
			}
			if err := writeDerivedFiles(dir, msg.MessageKey, pm); err != nil {
				return nil, err
			}
		}
		if err := reclassifyMessage(db, msg, pm, tracker, exclude...); err != nil {
			return nil, fmt.Errorf("reclassify %s: %w", msg.MessageKey, err)
		}
		result.Messages++
		if msg.IsBounce {
			result.Bounces++
		}
		if (i+1)%progressInterval == 0 {
			report(progress, fmt.Sprintf("reclassified %d of %d messages", i+1, len(msgs)))
		}
	}
	groups, err := finishGroups(db, tracker)
	if err != nil {
		return nil, err
	}
	result.Groups = groups
	if err := reapplyReportResponsible(db); err != nil {
		return nil, fmt.Errorf("reapply responsible: %w", err)
	}
	report(progress, fmt.Sprintf("reclassified %d messages, %d bounces, %d groups", result.Messages, result.Bounces, result.Groups))
	return result, nil
}

// finishGroups refreshes the counters of the touched groups, drops empty
// groups and returns the number of groups left.
func finishGroups(db *sql.DB, tracker *groupTracker) (int, error) {
	if _, err := tracker.refresh(db); err != nil {
		return 0, fmt.Errorf("refresh groups: %w", err)
	}
	if err := models.DeleteEmptyGroups(db); err != nil {
		return 0, fmt.Errorf("delete empty groups: %w", err)
	}
	counts, err := models.CountGroups(db)
	if err != nil {
		return 0, fmt.Errorf("count groups: %w", err)
	}
	return counts.Open + counts.Resolved + counts.Ignored, nil
}

// listRawKeys returns the message keys of every .eml in dir, sorted (the key
// starts with the date, so this is chronological order). A missing directory
// yields no keys.
func listRawKeys(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".eml") {
			continue
		}
		if key := keyFromFileName(e.Name()); key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// sourceForKey recovers the IMAP identity of a raw message from its .json.
// When the JSON is missing (files copied in by hand) a synthetic identity is
// derived from the key so that the row can still be inserted: UIDVALIDITY 0,
// the default folder and a UID hashed from the key. Such rows never collide with real
// IMAP identities and are re-fetched by the next check only if the server
// still has the message within the window (the key would then be identical
// and the duplicate is skipped).
func sourceForKey(dir, key string) Source {
	if pm, err := loadParsedMessage(dir, key); err == nil && pm.Source.MessageKey == key && (pm.Source.UID != 0 || pm.Source.UIDValidity != 0) {
		src := pm.Source
		if src.Folder == "" {
			// Older .json files recorded no folder.
			src.Folder = defaultFolder
		}
		return src
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	uid := h.Sum32()
	if uid == 0 {
		uid = 1
	}
	var received time.Time
	if info, err := os.Stat(filepath.Join(dir, key+".eml")); err == nil {
		received = info.ModTime().UTC()
	}
	return Source{MessageKey: key, Folder: defaultFolder, UIDValidity: 0, UID: uid, ReceivedAt: received}
}
