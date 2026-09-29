// Package dbschema applies the goose migrations of MailCare's two kinds of
// SQLite database:
//
//	migrations/master/  the master database (data/mailcare.db): users,
//	                    tokens, settings, mailboxes, jobs
//	migrations/index/   every per-mailbox index (data/mails/<address>.sqlite):
//	                    messages, bounces, groups, agent reports
//
// The SQL files live in embedded/migrations/ and are embedded into the
// binary by package main (embed.go), which passes them in as FS. Each
// database records the versions applied to it in its own goose_db_version
// table (by number: the directory a file lives in is not recorded), and
// Apply brings it up to date whenever it is opened. Add a new file per
// change, numbered sequentially within its directory
// (0002_<description>.sql, ...); never edit an applied migration.
package dbschema

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// FS holds migrations/master/*.sql and migrations/index/*.sql (the
// embedded/ directory, set by package main; tests set it to a directory).
var FS fs.FS

// errNoMigrations is returned by Apply when FS was not set.
var errNoMigrations = errors.New("dbschema: the embedded migrations are not available (dbschema.FS is not set)")

// Master returns the migrations of the master database (nil when FS is
// not set).
func Master() fs.FS { return sub("migrations/master") }

// Index returns the migrations of a per-mailbox index (nil when FS is not
// set).
func Index() fs.FS { return sub("migrations/index") }

func sub(dir string) fs.FS {
	if FS == nil {
		return nil
	}
	s, err := fs.Sub(FS, dir)
	if err != nil {
		return nil
	}
	return s
}

// Apply runs the pending migrations of fsys on db and returns the names of
// the files it applied (empty when the database was up to date; an error
// when fsys is nil, i.e. FS was not set). It uses a goose provider (no
// global goose state), so the master database and any number of indexes
// can be migrated independently. The provider is not closed: that would
// close db.
func Apply(ctx context.Context, db *sql.DB, fsys fs.FS) ([]string, error) {
	if fsys == nil {
		return nil, errNoMigrations
	}
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
