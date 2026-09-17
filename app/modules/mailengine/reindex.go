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

// Reindex rebuilds the index from the raw .eml files: the fetch-equivalent
// import (parse, regenerate the body section files, insert the row) followed
// by a full grouping (classify, extract, categorize, group). From the
// previous index it carries over, for every message key that still has a
// .eml, the IMAP identity and the fetch facts of the row (folder,
// uidvalidity, uid, size, received_at, fetched_at; a raw file the previous
// index did not know gets a synthetic identity, see sourceForKey, and a
// synthetic identity that collides with an indexed row is retried with the
// next UID, see storeWithFreeUID), and, for
// every group key that exists in the rebuilt index (keys are deterministic),
// the group state (open / resolved / ignored with its timestamp), the
// needs_analysis flag and the agent reports; a group whose message count
// grew is flagged for analysis again, and the responsible party named by
// the latest completed report is re-applied. Groups that no longer exist
// lose their reports. When the previous index cannot be read or was written
// with another schema version nothing is carried over. Section files are
// written over the files of the same name; nothing is deleted.
//
// The new index is built as <address>.sqlite.rebuild and swapped in only at
// the end (the swap retries for a few seconds when a file is still open,
// see swapIndexFiles); on failure or cancellation the build file (with its
// SQLite side files) is removed and the previous index stays as it was. A
// raw file that cannot be indexed is reported and counted in Skipped.
func Reindex(ctx context.Context, mailsRoot, address string, progress Progress) (*ReindexResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := MailboxDir(mailsRoot, address)
	indexPath := MailboxIndexPath(mailsRoot, address)
	carried, err := readCarryover(indexPath)
	if err != nil {
		report(progress, fmt.Sprintf("previous index not readable; sources, states and reports are not carried over: %v", err))
		carried = nil
	}
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
		if swapped {
			return
		}
		_ = db.Close()
		_ = removeIndexFiles(buildPath)
	}()

	listing, err := listRawFiles(dir)
	if err != nil {
		return nil, err
	}
	report(progress, fmt.Sprintf("reindexing %d raw messages", len(listing.keys)))
	result := &ReindexResult{}
	tracker := newGroupTracker()
	exclude := []string{address}
	for i, key := range listing.keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := readRawMessage(dir, key)
		if err != nil {
			report(progress, fmt.Sprintf("error: skipping %s: %v", key, err))
			result.Skipped++
			continue
		}
		src := sourceForKey(dir, key, carried.messageSource(key))
		msg, pm, err := storeWithFreeUID(db, dir, raw, src)
		if err != nil {
			if isUniqueViolation(err) {
				// Only a synthetic identity can collide (a carried one was
				// unique in the previous index); after the retries the raw
				// file is left out of the index and counted as skipped.
				report(progress, fmt.Sprintf("error: skipping %s: %v", key, err))
				result.Skipped++
				continue
			}
			return nil, err
		}
		if err := groupMessage(db, msg, pm, tracker, exclude...); err != nil {
			return nil, fmt.Errorf("group %s: %w", key, err)
		}
		result.Messages++
		if msg.IsBounce {
			result.Bounces++
		}
		if (i+1)%progressInterval == 0 {
			report(progress, fmt.Sprintf("reindexed %d of %d messages", i+1, len(listing.keys)))
		}
	}
	groups, err := finishGroups(db)
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
	line := fmt.Sprintf("reindexed %d messages, %d bounces, %d groups", result.Messages, result.Bounces, result.Groups)
	if result.Skipped > 0 {
		line += fmt.Sprintf(", %d skipped", result.Skipped)
	}
	report(progress, line)
	return result, nil
}

// maxSyntheticUIDRetries bounds how many consecutive UIDs storeWithFreeUID
// tries for a raw file whose synthetic identity collides with an indexed
// row (a variable so that the tests can lower it).
var maxSyntheticUIDRetries = 1000

// storeWithFreeUID stores a raw message for the rebuilt index. A synthetic
// identity (UIDVALIDITY 0, the UID hashed from the key by sourceForKey) can
// collide with the identity of another raw file; the UNIQUE violation is
// then retried with the next UID, up to maxSyntheticUIDRetries times, before
// the error is returned. A carried identity is stored as it is.
func storeWithFreeUID(db *sql.DB, dir string, raw []byte, src Source) (*models.Message, *ParsedMessage, error) {
	msg, pm, err := storeMessage(db, dir, raw, src, storeOptions{writeEML: false})
	for attempt := 0; err != nil && isUniqueViolation(err) && src.UIDValidity == 0 && attempt < maxSyntheticUIDRetries; attempt++ {
		src.UID++
		if src.UID == 0 {
			src.UID = 1
		}
		msg, pm, err = storeMessage(db, dir, raw, src, storeOptions{writeEML: false})
	}
	return msg, pm, err
}

// File operations on the index are retried for a while: on Windows a
// reader that still holds the old index open makes its removal or the
// rename of the new one fail transiently (variables so that the tests can
// shorten the wait).
var (
	fileRetryInterval = 100 * time.Millisecond
	fileRetryTimeout  = 5 * time.Second
)

// retryFileOp calls op until it succeeds, retrying every fileRetryInterval
// for up to fileRetryTimeout; the last error is returned when it never
// succeeds.
func retryFileOp(op func() error) error {
	deadline := time.Now().Add(fileRetryTimeout)
	for {
		err := op()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(fileRetryInterval)
	}
}

// swapIndexFiles replaces the index at dst with the freshly built one at src
// (including a WAL side file that may still exist after Close). Every step
// is retried for a few seconds (retryFileOp) because a reader that has the
// old index open can make the removal or the rename fail on Windows.
func swapIndexFiles(src, dst string) error {
	if err := retryFileOp(func() error { return removeIndexFiles(dst) }); err != nil {
		return fmt.Errorf("remove old index: %w", err)
	}
	if err := retryFileOp(func() error { return os.Rename(src, dst) }); err != nil {
		return fmt.Errorf("install rebuilt index: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(src + suffix); err == nil {
			if err := retryFileOp(func() error { return os.Rename(src+suffix, dst+suffix) }); err != nil {
				return fmt.Errorf("install rebuilt index%s: %w", suffix, err)
			}
		}
	}
	return nil
}

// finishGroups drops the groups left without messages and returns the
// number of groups left (the counters were maintained message by message).
func finishGroups(db *sql.DB) (int, error) {
	if err := models.DeleteEmptyGroups(db); err != nil {
		return 0, fmt.Errorf("delete empty groups: %w", err)
	}
	return countAllGroups(db)
}

// rawListing is what the mailbox directory holds: the message keys of the
// .eml files, sorted.
type rawListing struct {
	keys []string
}

// listRawFiles scans the mailbox directory for .eml files named after a
// message key; other files are ignored. A missing directory yields an empty
// listing.
func listRawFiles(dir string) (*rawListing, error) {
	listing := &rawListing{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return listing, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".eml") {
			continue
		}
		if key := keyFromFileName(e.Name()); key != "" {
			listing.keys = append(listing.keys, key)
		}
	}
	sort.Strings(listing.keys)
	return listing, nil
}

// sourceForKey builds the Source of a raw message for the rebuilt row. When
// the previous index knew the key, its IMAP identity and fetch facts are
// used as they were (carried is nil otherwise), so the rebuilt row is the
// same as before. A raw file the index did not know (copied in by hand, or
// the index was lost) gets a synthetic identity: UIDVALIDITY 0, the default
// folder, a UID hashed from the key (FNV-32) and the file's modification
// time as received date. Such rows never collide with real IMAP identities
// and are re-fetched by the next check only if the server still has the
// message within the window (the key would then be identical and the
// duplicate is skipped).
func sourceForKey(dir, key string, carried *models.MessageSource) Source {
	if carried != nil {
		src := Source{
			MessageKey:  key,
			Folder:      carried.Folder,
			UIDValidity: carried.UIDValidity,
			UID:         carried.UID,
			Size:        carried.Size,
			FetchedAt:   carried.FetchedAt,
		}
		if carried.ReceivedAt.Valid {
			src.ReceivedAt = carried.ReceivedAt.Time
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
