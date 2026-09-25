package mailengine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"mailcare/app/models"
)

// PruneMailbox is the mail retention (design 3 / 5.1 / 5.5): it removes the
// mails of the address whose date (messages.date: the Date header, else
// INTERNALDATE, else the fetch time) is older than now - keep. For every
// such message the body section files (<key>-1.txt ... <key>-<text_count>.txt
// and <key>-1.html ... <key>-<html_count>.html) and then the raw file
// (<key>.eml) are deleted, and then the index row (its bounces row goes
// with it by cascade). The groups that lost bounces are recounted
// (RefreshGroupCounters, which never flags a group for analysis when its
// count shrinks) and the groups left without messages are deleted together
// with their agent reports (cascade). A file that does not exist any more
// is not an error. When a file cannot be removed the row is kept, so the
// next run tries again, and the other messages are still processed; the
// failures are returned joined, together with the result of what was done.
// A cancelled context stops the loop the same way. keep must be positive.
// One progress line reports the outcome when something was removed.
func PruneMailbox(ctx context.Context, mailsRoot, address string, keep time.Duration, progress Progress) (*PruneResult, error) {
	if keep <= 0 {
		return nil, errors.New("mailengine: the mail retention must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := OpenIndex(ctx, mailsRoot, address, progress)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	cutoff := time.Now().Add(-keep)
	expired, err := models.ListMessagesOlderThan(db, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list expired messages: %w", err)
	}
	result := &PruneResult{}
	if len(expired) == 0 {
		return result, nil
	}
	dir := MailboxDir(mailsRoot, address)
	var groups []string
	seen := map[string]bool{}
	var errs []error
	for _, m := range expired {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := removeMessageFiles(dir, m); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := models.DeleteMessage(db, m.ID); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", m.MessageKey, err))
			continue
		}
		result.Removed++
		if m.GroupKey != "" && !seen[m.GroupKey] {
			seen[m.GroupKey] = true
			groups = append(groups, m.GroupKey)
		}
	}
	if result.Removed > 0 {
		removedGroups, err := recountGroups(db, groups)
		if err != nil {
			errs = append(errs, err)
		}
		result.RemovedGroups = removedGroups
		line := fmt.Sprintf("removed %d message(s) older than %s", result.Removed, keepLabel(keep))
		if result.RemovedGroups > 0 {
			line += fmt.Sprintf(", %d group(s) left empty and removed", result.RemovedGroups)
		}
		report(progress, line)
	}
	return result, errors.Join(errs...)
}

// removeMessageFiles deletes the body section files and then the raw file
// of a message. Files that do not exist are skipped; the raw file goes
// last so that a message whose removal fails half-way still has its
// original (the sections come back with a reindex).
func removeMessageFiles(dir string, m models.ExpiredMessage) error {
	if !ValidMessageKey(m.MessageKey) {
		return fmt.Errorf("%w: %q", ErrInvalidMessageKey, m.MessageKey)
	}
	var paths []string
	for n := 1; n <= m.TextCount; n++ {
		paths = append(paths, SectionFilePath(dir, m.MessageKey, "txt", n))
	}
	for n := 1; n <= m.HTMLCount; n++ {
		paths = append(paths, SectionFilePath(dir, m.MessageKey, "html", n))
	}
	paths = append(paths, rawFilePath(dir, m.MessageKey))
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", filepath.Base(p), err)
		}
	}
	removeEmptyMonthDir(MessageDir(dir, m.MessageKey))
	return nil
}

// removeEmptyMonthDir removes a month directory, then its year directory,
// once they are empty; os.Remove refuses a directory that still holds
// files, which is fine.
func removeEmptyMonthDir(month string) {
	if os.Remove(month) == nil {
		_ = os.Remove(filepath.Dir(month))
	}
}

// recountGroups refreshes the counters of the groups that lost messages
// and deletes the groups left without messages (their reports go with
// them by cascade). It returns how many groups were deleted.
func recountGroups(db *sql.DB, keys []string) (int, error) {
	for _, key := range keys {
		if err := models.RefreshGroupCounters(db, key); err != nil {
			return 0, fmt.Errorf("refresh group %s: %w", key, err)
		}
	}
	before, err := countAllGroups(db)
	if err != nil {
		return 0, err
	}
	if err := models.DeleteEmptyGroups(db); err != nil {
		return 0, fmt.Errorf("delete empty groups: %w", err)
	}
	after, err := countAllGroups(db)
	if err != nil {
		return 0, err
	}
	return before - after, nil
}

// StaleTempFileAge is how old (by modification time) a leftover temporary
// file of an atomic write (.<name>.<random>.tmp, see writeFileAtomic) must
// be before RemoveStaleTempFiles deletes it: a write still in progress
// never holds a temporary file that long.
const StaleTempFileAge = 24 * time.Hour

// tempFileRe matches the temporary files writeFileAtomic creates next to
// their target: a leading dot, the target name, a random part and the
// .tmp suffix.
var tempFileRe = regexp.MustCompile(`^\..+\.[^.]+\.tmp$`)

// RemoveStaleTempFiles deletes the leftovers of interrupted atomic writes
// in the year / month directories of the address: the temporary files
// (.<name>.<random>.tmp) whose modification time is older than
// StaleTempFileAge. Month and year directories left empty go with them. It
// is part of the daily cleanup job (design 5.6 / 7.1), reports one progress
// line when something was removed and returns how many files went. A
// missing directory is not an error; files that cannot be removed are
// reported joined and tried again next time.
func RemoveStaleTempFiles(mailsRoot, address string, progress Progress) (int, error) {
	dir := MailboxDir(mailsRoot, address)
	cutoff := time.Now().Add(-StaleTempFileAge)
	removed := 0
	touched := map[string]bool{}
	var errs []error
	err := walkMailboxFiles(dir, func(sub string, e os.DirEntry) {
		if !tempFileRe.MatchString(e.Name()) {
			return
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return
		}
		if err := os.Remove(filepath.Join(sub, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %s: %w", e.Name(), err))
			return
		}
		touched[sub] = true
		removed++
	})
	if err != nil {
		errs = append(errs, err)
	}
	for sub := range touched {
		removeEmptyMonthDir(sub)
	}
	if removed > 0 {
		report(progress, fmt.Sprintf("removed %d stale temporary file(s)", removed))
	}
	return removed, errors.Join(errs...)
}

// keepLabel describes a retention for progress lines: a whole number of
// days as "N days", anything else as the duration itself.
func keepLabel(keep time.Duration) string {
	const day = 24 * time.Hour
	if keep%day != 0 {
		return keep.String()
	}
	days := int(keep / day)
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}
