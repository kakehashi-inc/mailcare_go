package modules

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"mailcare/app/models"
)

func localDate(y int, mo time.Month, d, h, m, s int) time.Time {
	return time.Date(y, mo, d, h, m, s, 0, time.Local)
}

func TestNextCheckAt(t *testing.T) {
	now := localDate(2026, 9, 17, 13, 0, 0)
	next, ok := NextCheckAt(now, []string{"06:00", "12:00", "18:00"})
	if !ok || !next.Equal(localDate(2026, 9, 17, 18, 0, 0)) {
		t.Errorf("got %v (%v), want today 18:00", next, ok)
	}
	next, ok = NextCheckAt(localDate(2026, 9, 17, 19, 0, 0), []string{"06:00", "18:00"})
	if !ok || !next.Equal(localDate(2026, 9, 18, 6, 0, 0)) {
		t.Errorf("got %v (%v), want tomorrow 06:00", next, ok)
	}
	// Exactly at the check time the next occurrence is tomorrow.
	next, ok = NextCheckAt(localDate(2026, 9, 17, 18, 0, 0), []string{"18:00"})
	if !ok || !next.Equal(localDate(2026, 9, 18, 18, 0, 0)) {
		t.Errorf("at the minute: got %v (%v)", next, ok)
	}
	if _, ok := NextCheckAt(now, nil); ok {
		t.Errorf("no times: expected ok=false")
	}
	if _, ok := NextCheckAt(now, []string{"garbage"}); ok {
		t.Errorf("invalid entry only: expected ok=false")
	}
}

func TestDueCheckTimes(t *testing.T) {
	last := localDate(2026, 9, 17, 5, 59, 40)
	now := localDate(2026, 9, 17, 6, 0, 10)
	fired := map[string]string{}
	due := dueCheckTimes(last, now, []string{"06:00", "12:00", "bogus", "1:2"}, fired)
	if len(due) != 1 || due[0].time != "06:00" || due[0].minute != "2026-09-17 06:00" {
		t.Fatalf("got %+v, want 06:00 only", due)
	}
	fired["06:00"] = due[0].minute
	if again := dueCheckTimes(last, now.Add(20*time.Second), []string{"06:00"}, fired); len(again) != 0 {
		t.Errorf("fired twice in the same minute: %v", again)
	}
	// The next day the same time fires again.
	if next := dueCheckTimes(localDate(2026, 9, 18, 5, 59, 50), localDate(2026, 9, 18, 6, 0, 5), []string{"06:00"}, fired); len(next) != 1 {
		t.Errorf("next day: got %v", next)
	}
	// A time that did not arrive yet does not fire; one exactly at now does.
	if early := dueCheckTimes(last, localDate(2026, 9, 17, 5, 59, 59), []string{"06:00"}, map[string]string{}); len(early) != 0 {
		t.Errorf("before the minute: got %v", early)
	}
	if exact := dueCheckTimes(last, localDate(2026, 9, 17, 6, 0, 0), []string{"06:00"}, map[string]string{}); len(exact) != 1 {
		t.Errorf("exactly at the minute: got %v", exact)
	}
	// A time equal to last does not fire again (interval is half-open).
	if same := dueCheckTimes(localDate(2026, 9, 17, 6, 0, 0), localDate(2026, 9, 17, 6, 0, 30), []string{"06:00"}, map[string]string{}); len(same) != 0 {
		t.Errorf("occurrence equal to last fired: %v", same)
	}
	// A tick spanning midnight still catches 23:59.
	if due := dueCheckTimes(localDate(2026, 9, 17, 23, 58, 50), localDate(2026, 9, 18, 0, 0, 5), []string{"23:59"}, map[string]string{}); len(due) != 1 || due[0].minute != "2026-09-17 23:59" {
		t.Errorf("midnight crossing: got %+v", due)
	}
	if due := dueCheckTimes(last, now, nil, map[string]string{}); len(due) != 0 {
		t.Errorf("empty list: got %v", due)
	}
}

func TestSchedulerTickQueuesOneSyncJob(t *testing.T) {
	db := newTestDB(t)
	if err := SaveCheckTimes(db, []string{"06:00", "18:00"}); err != nil {
		t.Fatal(err)
	}
	jm := NewJobManager(db, nil, t.TempDir(), t.TempDir(), nil)
	s := NewScheduler(db, jm)
	clock := localDate(2026, 9, 17, 5, 59, 50)
	s.now = func() time.Time { return clock }
	s.lastTick = clock

	// Nothing is due yet.
	clock = localDate(2026, 9, 17, 5, 59, 59)
	s.Tick()
	if jobs, _ := models.ListActiveJobs(db); len(jobs) != 0 {
		t.Fatalf("job queued before the check time: %d", len(jobs))
	}
	// 06:00 arrives: exactly one sync expansion job (every enabled mailbox).
	clock = localDate(2026, 9, 17, 6, 0, 10)
	s.Tick()
	jobs, err := models.ListActiveJobs(db)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("after 06:00: %d jobs, %v", len(jobs), err)
	}
	if jobs[0].Kind != JobKindSync || jobs[0].MailboxID.Valid || jobs[0].RequestedBy != RequestedByScheduler {
		t.Errorf("unexpected job %+v", jobs[0])
	}
	// The same minute does not fire twice.
	clock = localDate(2026, 9, 17, 6, 0, 40)
	s.Tick()
	if jobs, _ = models.ListActiveJobs(db); len(jobs) != 1 {
		t.Errorf("re-queued within the same minute: %d", len(jobs))
	}
	// The next time arrives while the first job is still active: no duplicate.
	clock = localDate(2026, 9, 17, 18, 0, 5)
	s.Tick()
	if jobs, _ = models.ListActiveJobs(db); len(jobs) != 1 {
		t.Errorf("duplicate queued while the sync job was active: %d", len(jobs))
	}
	// Once it finished, the following occurrence queues a new one.
	if err := models.FinishJob(db, jobs[0].ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	clock = localDate(2026, 9, 18, 6, 0, 5)
	s.Tick()
	if jobs, _ = models.ListActiveJobs(db); len(jobs) != 1 {
		t.Errorf("next day: %d active jobs, want 1", len(jobs))
	}
	if all, _ := models.ListJobs(db, 10); len(all) != 2 {
		t.Errorf("total jobs %d, want 2", len(all))
	}
	// The next check is derived from the configured times.
	if next, ok := s.NextCheckAt(); !ok || !next.Equal(localDate(2026, 9, 18, 18, 0, 0)) {
		t.Errorf("NextCheckAt = %v (%v)", next, ok)
	}
}

// newTestDB opens a migrated master database in a fresh data directory.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	MigrationsFS = os.DirFS("../..")
	SetDataDir(t.TempDir())
	db, err := OpenDB("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
