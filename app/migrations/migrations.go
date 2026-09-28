// Package migrations holds the goose migrations of MailCare's two kinds of
// SQLite database and applies them:
//
//	master/  the master database (data/mailcare.db): users, tokens,
//	         settings, mailboxes, jobs
//	index/   every per-mailbox index (data/mails/<address>.sqlite): messages,
//	         bounces, groups, agent reports
//
// The SQL files are embedded into the binary. Each database records the
// versions applied to it in its own goose_db_version table (by number: the
// directory a file lives in is not recorded), and Apply brings it up to date
// whenever it is opened. Add a new file per change, numbered sequentially
// within its directory (0002_<description>.sql, ...); never edit an applied
// migration.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed master/*.sql
var masterFS embed.FS

//go:embed index/*.sql
var indexFS embed.FS

// Master returns the migrations of the master database.
func Master() fs.FS { return sub(masterFS, "master") }

// Index returns the migrations of a per-mailbox index.
func Index() fs.FS { return sub(indexFS, "index") }

func sub(fsys embed.FS, dir string) fs.FS {
	s, err := fs.Sub(fsys, dir)
	if err != nil {
		// The directory is embedded at build time; a failure is a
		// programming error.
		panic(fmt.Sprintf("migrations: %v", err))
	}
	return s
}

// Apply runs the pending migrations of fsys on db and returns the names of
// the files it applied (empty when the database was up to date). It uses a
// goose provider (no global goose state), so the master database and any
// number of indexes can be migrated independently. The provider is not
// closed: that would close db.
func Apply(ctx context.Context, db *sql.DB, fsys fs.FS) ([]string, error) {
	p, err := goose.NewProvider(goose.DialectSQLite3, db, fsys, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return nil, err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, err
	}
	applied := make([]string, 0, len(results))
	for _, r := range results {
		if r.Source != nil {
			applied = append(applied, r.Source.Path)
		}
	}
	return applied, nil
}
