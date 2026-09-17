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

// Scheduler queues a check job for every mailbox at each configured check
// time (HH:MM, local wall clock). It re-reads check_times on every tick so a
// change in the settings takes effect immediately, and it never fires the
// same time twice within one minute.
type Scheduler struct {
	db  *sql.DB
	jm  *JobManager
	now func() time.Time

	mu        sync.Mutex
	lastTick  time.Time
	lastFired map[string]string // HH:MM -> "YYYY-MM-DD HH:MM" of the last firing
	cancel    context.CancelFunc
	wg        sync.WaitGroup
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

// Tick evaluates the check times once: every configured time that arrived
// since the previous tick queues one check job (all mailboxes).
func (s *Scheduler) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	due := dueCheckTimes(s.lastTick, now, ResolveCheckTimes(s.db), s.lastFired)
	s.lastTick = now
	if len(due) == 0 {
		return
	}
	for _, key := range due {
		s.lastFired[key.time] = key.minute
	}
	job, created, err := s.jm.Enqueue(JobKindCheck, 0, "", RequestedByScheduler)
	switch {
	case err != nil:
		log.Printf("scheduler: failed to queue the check job: %v", err)
	case created:
		log.Printf("scheduler: queued check job #%d", job.ID)
	default:
		log.Printf("scheduler: check job #%d is already %s", job.ID, job.Status)
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
