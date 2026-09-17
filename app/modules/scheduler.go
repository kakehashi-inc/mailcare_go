package modules

import (
	"context"
	"database/sql"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"mailcare/app/models"
)

// Scheduler queues a sync job for every enabled mailbox at each configured
// check time (HH:MM, local wall clock), a notify job when the notify time
// arrives and the notification interval has elapsed (NotifyDue), and one
// cleanup job (every mailbox) on the first tick of each local day
// (CleanupDue; the date is recorded in the cleanup_last_run_date setting so
// a restart does not repeat it and a day the server was down is caught up
// at start). It re-reads the settings on every tick so a change takes
// effect immediately, and it never fires the same time twice within one
// minute.
type Scheduler struct {
	db  *sql.DB
	jm  *JobManager
	now func() time.Time

	mu           sync.Mutex
	lastTick     time.Time
	lastFired    map[string]string // HH:MM -> "YYYY-MM-DD HH:MM" of the last firing
	notifyFired  string            // "YYYY-MM-DD HH:MM" of the last notify firing
	cleanupFired string            // "YYYY-MM-DD" of the last cleanup queued by this process
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// NewScheduler creates a scheduler that submits jobs through jm.
func NewScheduler(db *sql.DB, jm *JobManager) *Scheduler {
	return &Scheduler{db: db, jm: jm, now: time.Now, lastFired: map[string]string{}}
}

// Start launches the ticking goroutine. Times that already passed today do
// not fire retroactively; the daily cleanup is queued at once when none was
// queued today yet (the server was down when the day began).
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	s.lastTick = s.now()
	s.queueCleanupIfDue(s.lastTick)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(SchedulerTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Tick()
			}
		}
	}()
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	s.wg.Wait()
}

// Tick evaluates the check times, the notify time and the daily cleanup
// once: every configured check time that arrived since the previous tick
// queues one sync job (mailbox NULL: expanded into one child per enabled
// mailbox by the job manager), a due notify time queues one notify job, and
// the first tick of a new local day queues one cleanup job (mailbox NULL:
// one child per mailbox, disabled ones included).
func (s *Scheduler) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	last := s.lastTick
	s.lastTick = now
	if due := dueCheckTimes(last, now, ResolveCheckTimes(s.db), s.lastFired); len(due) > 0 {
		for _, key := range due {
			s.lastFired[key.time] = key.minute
		}
		s.enqueue(JobKindSync)
	}
	s.queueCleanupIfDue(now)
	settings, err := ResolveNotificationSettings(s.db, nil)
	if err != nil {
		log.Printf("scheduler: failed to read the notification settings: %v", err)
		return
	}
	if key, due := NotifyDue(last, now, settings, s.notifyFired); due {
		s.notifyFired = key
		s.enqueue(JobKindNotify)
	}
}

// queueCleanupIfDue queues the daily cleanup when CleanupDue says so and
// records the date (settings cleanup_last_run_date). The caller holds s.mu.
func (s *Scheduler) queueCleanupIfDue(now time.Time) {
	date, due := CleanupDue(now, models.GetSetting(s.db, SettingCleanupLastRunDate), s.cleanupFired)
	if !due {
		return
	}
	if !s.enqueue(JobKindCleanup) {
		return
	}
	s.cleanupFired = date
	if err := models.SetSetting(s.db, SettingCleanupLastRunDate, date); err != nil {
		log.Printf("scheduler: failed to record the cleanup date: %v", err)
	}
}

// enqueue queues one job of a kind without a mailbox on behalf of the
// scheduler and logs the outcome. It reports whether such a job is now
// queued or running (false when the queue could not be reached).
func (s *Scheduler) enqueue(kind string) bool {
	job, created, err := s.jm.Enqueue(kind, 0, "", RequestedByScheduler)
	switch {
	case err != nil:
		log.Printf("scheduler: failed to queue the %s job: %v", kind, err)
		return false
	case created:
		log.Printf("scheduler: queued %s job #%d", kind, job.ID)
	default:
		log.Printf("scheduler: %s job #%d is already %s", kind, job.ID, job.Status)
	}
	return true
}

// CleanupDue decides whether the scheduler should queue the daily cleanup at
// now: the local date of now differs from the date of the last queued
// cleanup (lastRun, the cleanup_last_run_date setting, "" = never) and this
// process did not queue one for that date yet (fired). It returns the date
// to record ("YYYY-MM-DD").
func CleanupDue(now time.Time, lastRun, fired string) (string, bool) {
	today := now.In(time.Local).Format("2006-01-02")
	if today == lastRun || today == fired {
		return "", false
	}
	return today, true
}

// NextCheckAt returns the next scheduled check (false when no time is set).
func (s *Scheduler) NextCheckAt() (time.Time, bool) {
	return NextCheckAt(s.now(), ResolveCheckTimes(s.db))
}

type dueTime struct {
	time   string // HH:MM
	minute string // the occurrence, "2006-01-02 15:04"
}

// dueCheckTimes returns the check times whose occurrence lies in (last, now]
// and was not fired for that minute yet. Both today's and yesterday's
// occurrence are considered so a tick spanning midnight is not missed.
func dueCheckTimes(last, now time.Time, times []string, fired map[string]string) []dueTime {
	now = now.In(time.Local)
	var due []dueTime
	for _, t := range times {
		hh, mm, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		h, err1 := strconv.Atoi(hh)
		m, err2 := strconv.Atoi(mm)
		if err1 != nil || err2 != nil {
			continue
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
		for _, occ := range []time.Time{today.AddDate(0, 0, -1), today} {
			if !occ.After(last) || occ.After(now) {
				continue
			}
			minute := occ.Format("2006-01-02 15:04")
			if fired[t] == minute {
				continue
			}
			due = append(due, dueTime{time: t, minute: minute})
		}
	}
	return due
}
