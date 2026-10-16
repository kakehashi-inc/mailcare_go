package dbschema

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/pressly/goose/v3"

	"mailcare/app/modules/filelock"
)

// Applying the migrations of one database is serialized across processes
// (the server starting while "mailcare service status" opens the same
// database, two jobs opening a mailbox index for the first time): each
// would otherwise find the same versions pending, apply them both and record
// every version twice in goose_db_version, and a migration that cannot run
// twice would fail. The lock is an exclusive operating system lock
// (filelock) on a file next to the database: the operating system releases
// it when its holder closes it or ends, also after a crash, so it never goes
// stale and a waiter waits exactly as long as the holder migrates, however
// many databases it migrates. The holder removes the file before it
// releases the lock; a waiter that got the lock of a file removed meanwhile
// tries the current one.

// LockSuffix is appended to the path of a database for its migration lock.
const LockSuffix = ".migrate.lock"

// lockPoll is how often a held lock is tried again (the pace of the wait,
// which lasts until the lock is released; a variable so that the tests can
// change it).
var lockPoll = 50 * time.Millisecond

// ApplyLocked is Apply for the database file at dbPath, serialized across
// processes by the lock file dbPath + LockSuffix. A database that is up to
// date is not locked at all (upToDate only reads). Otherwise the lock is
// taken, waiting while another process holds it (until it is released or
// ctx ends), the pending migrations are looked up again (the other holder
// has usually applied them meanwhile) and applied, and the lock file is
// removed, whatever the outcome.
func ApplyLocked(ctx context.Context, db *sql.DB, fsys fs.FS, dbPath string) ([]string, error) {
	if fsys == nil {
		return nil, errNoMigrations
	}
	if upToDate(ctx, db, fsys, dbPath) {
		return []string{}, nil
	}
	release, err := acquireLock(ctx, dbPath+LockSuffix)
	if err != nil {
		return nil, err
	}
	defer release()
	// A separate provider: the one of upToDate may have failed halfway.
	p, err := newProvider(db, fsys)
	if err != nil {
		return nil, err
	}
	if pending, err := p.HasPending(ctx); err != nil || !pending {
		return []string{}, err
	}
	return up(ctx, p)
}

// upToDate reports whether the database at dbPath has no pending migration,
// reading only: a database file that does not exist yet (or is empty), one
// without the version table of goose and any error (another process may be
// creating the file or switching it to WAL at that moment) count as not up
// to date, so that whatever writes, the first connection to a new file
// included, happens under the lock.
func upToDate(ctx context.Context, db *sql.DB, fsys fs.FS, dbPath string) bool {
	if info, err := os.Stat(dbPath); err != nil || info.Size() == 0 {
		return false
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).
		Scan(&tables); err != nil || tables == 0 {
		return false
	}
	p, err := newProvider(db, fsys)
	if err != nil {
		return false
	}
	pending, err := p.HasPending(ctx)
	return err == nil && !pending
}

// newProvider returns the goose provider of the migrations of fsys on db
// (no global goose state).
func newProvider(db *sql.DB, fsys fs.FS) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectSQLite3, db, fsys, goose.WithDisableGlobalRegistry(true))
}

// acquireLock takes the lock on the file at path (creating it), waiting
// while another process holds it until it is released or ctx ends. release
// removes the file and releases the lock (on Windows the file goes once it
// is closed).
func acquireLock(ctx context.Context, path string) (release func(), err error) {
	for {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open the migration lock %s: %w", path, err)
		}
		err = filelock.TryLock(f)
		switch {
		case err == nil:
			if !samePath(f, path) {
				// The previous holder removed this file after this process
				// opened it: lock the file now at path instead.
				_ = filelock.Unlock(f)
				f.Close()
				continue
			}
			if err := f.Truncate(0); err == nil {
				_, _ = fmt.Fprintf(f, "%d %s %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano), lockToken())
			}
			return func() {
				// Removed while still locked: a waiter then retries the new
				// file. Windows refuses to remove an open file: there it goes
				// once closed (a waiter that opened it meanwhile keeps it).
				removeErr := os.Remove(path)
				_ = filelock.Unlock(f)
				f.Close()
				if removeErr != nil {
					_ = os.Remove(path)
				}
			}, nil
		case errors.Is(err, filelock.ErrHeld):
			f.Close()
		default:
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// samePath reports whether path still names the open file f.
func samePath(f *os.File, path string) bool {
	opened, err := f.Stat()
	if err != nil {
		return false
	}
	current, err := os.Stat(path)
	return err == nil && os.SameFile(opened, current)
}

// lockToken returns a random token that identifies one holder of a lock.
func lockToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (it crashes the program instead)
	return hex.EncodeToString(b[:])
}
