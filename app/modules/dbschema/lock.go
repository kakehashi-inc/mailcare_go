package dbschema

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
)

// Applying the migrations of one database is serialized across processes
// (the server starting while "mailcare service status" opens the same
// database, two jobs opening a mailbox index for the first time): each
// would otherwise find the same versions pending, apply them both and record
// every version twice in goose_db_version, and a migration that cannot run
// twice would fail. SQLite offers no lock that outlives a transaction, and
// flock exists on Unix only, so the lock is a file next to the database
// created exclusively (O_CREATE|O_EXCL, an atomic test-and-create on
// Windows, macOS and Linux alike) that holds the process id, the time and
// a random token of its holder. It is removed after the migrations ran;
// one left behind by a process that died is taken over once it is older
// than lockStale.

// LockSuffix is appended to the path of a database for its migration lock.
const LockSuffix = ".migrate.lock"

// Bounds of the migration lock; variables so that the tests can shorten
// them.
var (
	lockWait  = 30 * time.Second      // how long ApplyLocked waits for another holder
	lockStale = 2 * time.Minute       // a lock older than this is taken over
	lockPoll  = 50 * time.Millisecond // how often a held lock is tried again
)

// ApplyLocked is Apply for the database file at dbPath, serialized across
// processes by the lock file dbPath + LockSuffix. A database that is up to
// date is not locked at all (upToDate only reads). Otherwise the lock is
// taken, waiting up to lockWait for another holder, the pending migrations
// are looked up again (the other holder has usually applied them meanwhile)
// and applied, and the lock file is removed, whatever the outcome.
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

// acquireLock creates the lock file, waiting up to lockWait (or until ctx
// ends) while another process holds it and taking over one older than
// lockStale. release removes the file when it is still the one created
// here.
func acquireLock(ctx context.Context, path string) (release func(), err error) {
	token := lockToken()
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%d %s %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano), token)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("write the migration lock %s: %w", path, werr)
			}
			return func() { releaseLock(path, token) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create the migration lock %s: %w", path, err)
		}
		if takeOverStale(path) {
			continue
		}
		if !time.Now().Before(deadline) {
			holder, _ := os.ReadFile(path)
			return nil, fmt.Errorf("the migrations of the database are being applied by another process (lock %s: %s); "+
				"if no MailCare process is running, remove the file", path, strings.TrimSpace(string(holder)))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// takeOverStale removes the lock file when it is older than lockStale (by
// the time its holder wrote in it, else by its modification time) and
// reports whether it did. The file is first moved aside and compared with
// what was judged: when another process took the stale lock over in
// between, the moved file is its fresh one, which is put back (unless yet
// another lock is there by then, which then stands).
func takeOverStale(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	written, ok := lockTime(data)
	if !ok {
		info, err := os.Stat(path)
		if err != nil {
			return false
		}
		written = info.ModTime()
	}
	if time.Since(written) <= lockStale {
		return false
	}
	aside := path + ".stale-" + lockToken()
	if err := os.Rename(path, aside); err != nil {
		return false
	}
	if moved, err := os.ReadFile(aside); err != nil || !bytes.Equal(moved, data) {
		_ = os.Link(aside, path)
	}
	_ = os.Remove(aside)
	return true
}

// lockTime reads the time a holder wrote in the lock file ("<pid> <time>
// <token>").
func lockTime(data []byte) (time.Time, bool) {
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, fields[1])
	return t, err == nil
}

// releaseLock removes the lock file when it still holds token (a lock taken
// over as stale meanwhile belongs to another process and stays).
func releaseLock(path, token string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if fields := strings.Fields(string(data)); len(fields) == 3 && fields[2] == token {
		_ = os.Remove(path)
	}
}

// lockToken returns a random token that identifies one holder of a lock.
func lockToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (it crashes the program instead)
	return hex.EncodeToString(b[:])
}
