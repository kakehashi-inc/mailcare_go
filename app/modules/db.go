package modules

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
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

	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrateLogger suppresses goose's "no migrations to run" chatter.
type migrateLogger struct{}

func (*migrateLogger) Fatalf(format string, v ...any) { log.Fatalf(format, v...) }
func (*migrateLogger) Printf(format string, v ...any) {
	if strings.Contains(format, "no migrations to run") {
		return
	}
	log.Printf(format, v...)
}

func runMigrations(db *sql.DB) error {
	if MigrationsFS == nil {
		return fmt.Errorf("migrations filesystem not initialized")
	}
	goose.SetBaseFS(MigrationsFS)
	goose.SetLogger(&migrateLogger{})
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	return goose.Up(db, "app/migrations")
}
