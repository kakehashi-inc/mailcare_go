package models

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mailcare/app/modules/dbschema"
)

// TestLikeContainsEscapesWildcards checks that a search string containing
// the LIKE wildcards or the escape character matches only literally.
func TestLikeContainsEscapesWildcards(t *testing.T) {
	for in, want := range map[string]string{
		"abc":        "%abc%",
		"50%":        `%50\%%`,
		"a_b":        `%a\_b%`,
		`C:\path`:    `%C:\\path%`,
		`%_\`:        `%\%\_\\%`,
		"plain text": "%plain text%",
	} {
		if got := likeContains(in); got != want {
			t.Errorf("likeContains(%q) = %q, want %q", in, got, want)
		}
	}

	db, err := OpenMailIndex(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	subjects := []string{"100% sure", "100 percent", "a_b", "axb", `back\slash`, "backslash"}
	for i, subject := range subjects {
		m := &Message{MessageKey: "20260901-12000" + string(rune('0'+i)) + "_aaaaaaaaaaaa", UID: uint32(i + 1), UIDValidity: 1,
			Subject: subject, Date: time.Now().UTC()}
		if err := InsertMessage(db, m); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string]int{"100%": 1, "a_b": 1, `back\`: 1, "100": 2, "b": 4, "zzz": 0} {
		_, total, err := ListMessages(db, MessageFilter{Query: q})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if total != want {
			t.Errorf("query %q matched %d messages, want %d", q, total, want)
		}
	}
	for i, unit := range []string{"50%", "50 percent", "x_y", "xzy"} {
		if err := UpsertGroup(db, &BounceGroup{GroupKey: "000000000000000" + string(rune('0'+i)), Category: "user_unknown",
			UnitValue: unit}); err != nil {
			t.Fatal(err)
		}
	}
	for q, want := range map[string]int{"50%": 1, "x_y": 1, "50": 2} {
		groups, err := ListGroups(db, GroupFilter{Query: q})
		if err != nil {
			t.Fatalf("group query %q: %v", q, err)
		}
		if len(groups) != want {
			t.Errorf("group query %q matched %d groups, want %d", q, len(groups), want)
		}
	}
}

// TestOpenMailIndexConcurrent opens an un-migrated index from several
// openers at once (two jobs, a reindex): every migration is recorded once
// and the lock file next to the index is gone afterwards.
func TestOpenMailIndexConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mails", "a@example.jp.sqlite")
	const openers = 6
	errs := make([]error, openers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range openers {
		wg.Go(func() {
			<-start
			db, err := OpenMailIndex(path)
			if err == nil {
				db.Close()
			}
			errs[i] = err
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("opener %d: %v", i, err)
		}
	}
	db, err := OpenMailIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var duplicated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT version_id FROM goose_db_version GROUP BY version_id HAVING COUNT(*) > 1)`).Scan(&duplicated); err != nil || duplicated != 0 {
		t.Errorf("%d versions recorded more than once (%v)", duplicated, err)
	}
	if _, err := os.Stat(path + dbschema.LockSuffix); !os.IsNotExist(err) {
		t.Errorf("the lock file is left behind: %v", err)
	}
}

// A statement waits for the lock of another connection without a limit:
// the busy timeout is the largest SQLite takes.
func TestSQLiteDSNBusyTimeout(t *testing.T) {
	db, err := OpenMailIndex(filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ms int64
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil || ms != math.MaxInt32 {
		t.Errorf("busy_timeout = %d (err %v), want %d", ms, err, int64(math.MaxInt32))
	}
}
