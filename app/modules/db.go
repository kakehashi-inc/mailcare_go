package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"mailcare/app/modules/dbschema"
)

// ResolveDBPath returns the path to the database (<data>/DBFileName).
func ResolveDBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DBFileName), nil
}

// OpenDB opens the SQLite database and runs migrations.
// If dbPath is empty the default path is used.
func OpenDB(dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		var err error
		dbPath, err = ResolveDBPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}

	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := RestrictDBFiles(dbPath); err != nil {
		log.Printf("warning: could not restrict the permissions of %s: %v", dbPath, err)
	}
	return db, nil
}

// dbFilePerm is the mode of the master database and its SQLite side files:
// they hold the secret key and the encrypted passwords, so only the owner
// may read them.
const dbFilePerm = 0o600

// RestrictDBFiles sets the master database file, and its -wal / -shm side
// files when present, to dbFilePerm. A missing side file is not an error.
func RestrictDBFiles(dbPath string) error {
	var errs []error
	for _, name := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(name, dbFilePerm); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DataDirPermissionWarning returns a warning when the data directory is
// readable or writable by other users (its mode is not 0700), and "" when it
// is private or the check does not apply (Windows has no such mode bits).
func DataDirPermissionWarning(dir string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	st, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("data directory %s has mode %04o; it holds the database and the mails, so make it private (chmod 700)", dir, perm)
	}
	return ""
}

// LogMigrations makes the applied migrations appear in the log. The server
// sets it; CLI commands stay quiet so that their output stays clean.
var LogMigrations bool

// runMigrations applies the pending migrations of the master database
// (embedded/migrations/master) and logs each applied file when LogMigrations is
// set.
func runMigrations(db *sql.DB) error {
	applied, err := dbschema.Apply(context.Background(), db, dbschema.Master())
	if err != nil {
		return fmt.Errorf("migrate the database: %w", err)
	}
	if LogMigrations {
		for _, name := range applied {
			log.Printf("applied migration %s", name)
		}
	}
	return nil
}
