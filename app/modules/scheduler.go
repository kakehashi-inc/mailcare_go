package modules

import (
	"context"
	"database/sql"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Scheduler queues a sync job for every enabled mailbox at each configured
// check time (HH:MM, local wall clock) and a notify job when the notify time
// arrives and the notification interval has elapsed (NotifyDue). It re-reads
// the settings on every tick so a change takes effect immediately, and it
// never fires the same time twice within one minute.
type Scheduler struct {
	db  *sql.DB
	jm  *JobManager
	now func() time.Time

	mu          sync.Mutex
	lastTick    time.Time
	lastFired   map[string]string // HH:MM -> "YYYY-MM-DD HH:MM" of the last firing
	notifyFired string            // "YYYY-MM-DD HH:MM" of the last notify firing
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// NewScheduler creates a scheduler that submits jobs through jm.
func NewScheduler(db *sql.DB, jm *JobManager) *Scheduler {
	return &Scheduler{db: db, jm: jm, now: time.Now, lastFired: map[string]string{}}
}

// Start launches the ticking goroutine. Times that already passed today do
// not fire retroactively.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	s.lastTick = s.now()
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

// Tick evaluates the check times and the notify time once: every configured
// check time that arrived since the previous tick queues one sync job
// (mailbox NULL: expanded into one child per enabled mailbox by the job
// manager), and a due notify time queues one notify job.
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

// enqueue queues one job of a kind without a mailbox on behalf of the
// scheduler and logs the outcome.
func (s *Scheduler) enqueue(kind string) {
	job, created, err := s.jm.Enqueue(kind, 0, "", RequestedByScheduler)
	switch {
	case err != nil:
		log.Printf("scheduler: failed to queue the %s job: %v", kind, err)
	case created:
		log.Printf("scheduler: queued %s job #%d", kind, job.ID)
	default:
		log.Printf("scheduler: %s job #%d is already %s", kind, job.ID, job.Status)
	}
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
