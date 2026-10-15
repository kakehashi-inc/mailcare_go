package dnscheck

import (
	"context"
	"database/sql"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"mailcare/app/modules/dbschema"
)

// TestMain wires in the files the binary embeds (embed.go), read from the
// repository: the migrations. Every Checker and SystemHostResolver a test
// makes asks offlineSystem as its OS resolver unless the test sets another.
func TestMain(m *testing.M) {
	root := filepath.Join("..", "..", "..")
	dbschema.FS = os.DirFS(filepath.Join(root, "embedded"))
	defaultSystemResolver = func() systemResolver { return offlineSystem{} }
	os.Exit(m.Run())
}

// offlineSystem is the OS resolver of the tests that set none: it fails
// every question at once, as an unreachable resolver would, so that no test
// asks the resolver of the host and the fake resolvers decide.
type offlineSystem struct{}

func offlineError(name string) error {
	return &net.DNSError{Err: "no OS resolver in the tests", Name: name, IsTemporary: true}
}

func (offlineSystem) LookupHost(_ context.Context, host string) ([]string, error) {
	return nil, offlineError(host)
}

func (offlineSystem) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	return nil, offlineError(name)
}

func (offlineSystem) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	return nil, offlineError(name)
}

func (offlineSystem) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return nil, offlineError(host)
}

func (offlineSystem) LookupTXT(_ context.Context, name string) ([]string, error) {
	return nil, offlineError(name)
}

// openTestMasterDB opens a fresh master database the way modules.OpenDB
// does (same DSN, migrations applied); package modules imports this one and
// cannot be imported here.
func openTestMasterDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailcare.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := dbschema.Apply(context.Background(), db, dbschema.Master()); err != nil {
		t.Fatal(err)
	}
	return db
}
