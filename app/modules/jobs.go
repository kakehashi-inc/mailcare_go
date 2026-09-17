package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strings"
	"sync"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/agent"
	"mailcare/app/modules/mailengine"
)

// Jobs (jobs table).
//
// A job is one unit of background work: check / reindex / reclassify /
// analyze. Jobs are queued in the master database and executed one at a time
// by a single worker goroutine (JobManager) so that a per-mailbox index is
// never written by two operations at once. The CLI uses the same runner in
// process (RunJobInline) when no server is running.

const (
	// progressFlushInterval throttles progress writes to the jobs table.
	progressFlushInterval = 500 * time.Millisecond
	// progressMaxLines bounds the progress text kept per job (newest lines).
	progressMaxLines = 200
	// jobRetention is how long finished jobs are kept.
	jobRetention = 30 * 24 * time.Hour
	// analyzeAllTarget is the analyze job target meaning "every group".
	analyzeAllTarget = "*"
)

// Requester tags stored in jobs.requested_by.
const (
	RequestedByCLI       = "cli"
	RequestedByScheduler = "scheduler"
)

// ErrJobKind is returned for an unknown job kind.
var ErrJobKind = errors.New("unknown job kind")

// ValidateJobKind reports whether kind is one of the known job kinds.
func ValidateJobKind(kind string) error {
	switch kind {
	case JobKindCheck, JobKindReindex, JobKindReclassify, JobKindAnalyze:
		return nil
	}
	return fmt.Errorf("%w %q", ErrJobKind, kind)
}

// JobManager queues and runs jobs.
type JobManager struct {
	db        *sql.DB
	key       []byte
	mailsRoot string
	agentRoot string
	templates fs.FS

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}
}

// NewJobManager creates a manager over the master database. key is the
// master key (to decrypt IMAP passwords); templates may be nil.
func NewJobManager(db *sql.DB, key []byte, mailsRoot, agentRoot string, templates fs.FS) *JobManager {
	return &JobManager{db: db, key: key, mailsRoot: mailsRoot, agentRoot: agentRoot, templates: templates,
		wake: make(chan struct{}, 1)}
}

// Start launches the worker goroutine. It is a no-op when already running.
func (m *JobManager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go m.loop(ctx)
}

// Stop cancels the running job (if any) and waits for the worker to exit.
func (m *JobManager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	m.wg.Wait()
}

func (m *JobManager) loop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(JobPollInterval)
	defer ticker.Stop()
	for {
		for ctx.Err() == nil {
			job, err := models.ClaimNextJob(m.db)
			if err != nil {
				log.Printf("job claim failed: %v", err)
				break
			}
			if job == nil {
				break
			}
			m.execute(ctx, job, nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

// Enqueue queues a job unless an identical queued/running job exists, in
// which case that job is returned with created=false. mailboxID 0 means "no
// mailbox" (every mailbox).
func (m *JobManager) Enqueue(kind string, mailboxID int64, target, requestedBy string) (job *models.Job, created bool, err error) {
	return EnqueueJob(m.db, kind, mailboxID, target, requestedBy, m.wake)
}

// EnqueueJob is Enqueue without a manager (used by the CLI and the server
// alike). wake may be nil.
func EnqueueJob(db *sql.DB, kind string, mailboxID int64, target, requestedBy string, wake chan struct{}) (*models.Job, bool, error) {
	if err := ValidateJobKind(kind); err != nil {
		return nil, false, err
	}
	if kind == JobKindAnalyze && mailboxID == 0 {
		return nil, false, errors.New("analyze requires a mailbox")
	}
	if kind != JobKindAnalyze {
		target = ""
	}
	if mailboxID != 0 {
		if _, err := models.GetMailboxByID(db, mailboxID); err == sql.ErrNoRows {
			return nil, false, errors.New("mailbox not found")
		} else if err != nil {
			return nil, false, err
		}
	}
	active, err := models.HasActiveJob(db, kind, mailboxID, target)
	if err != nil {
		return nil, false, err
	}
	if active {
		existing, err := findActiveJob(db, kind, mailboxID, target)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
			return existing, false, nil
		}
	}
	job := &models.Job{Kind: kind, Target: target, RequestedBy: requestedBy}
	if mailboxID != 0 {
		job.MailboxID = sql.NullInt64{Int64: mailboxID, Valid: true}
	}
	if err := models.InsertJob(db, job); err != nil {
		return nil, false, err
	}
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return job, true, nil
}

func findActiveJob(db *sql.DB, kind string, mailboxID int64, target string) (*models.Job, error) {
	jobs, err := models.ListActiveJobs(db)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Kind == kind && j.Target == target && j.MailboxID.Int64 == mailboxID {
			return j, nil
		}
	}
	return nil, nil
}

// ResetStaleJobs is run at server start: jobs and agent reports left running
// by a previous process are marked as error, and old finished jobs are
// deleted.
func ResetStaleJobs(db *sql.DB, mailsRoot string) {
	const reason = "interrupted by a server restart"
	if err := models.ResetRunningJobs(db, reason); err != nil {
		log.Printf("failed to reset running jobs: %v", err)
	}
	if err := models.DeleteOldJobs(db, time.Now().Add(-jobRetention)); err != nil {
		log.Printf("failed to delete old jobs: %v", err)
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		log.Printf("failed to list mailboxes: %v", err)
		return
	}
	for _, mb := range mailboxes {
		ctx, cancel := context.WithTimeout(context.Background(), IMAPTimeout)
		idx, err := mailengine.OpenIndex(ctx, mailsRoot, mb.Address, nil)
		cancel()
		if err != nil {
			log.Printf("index of %s could not be opened: %v", mb.Address, err)
			continue
		}
		if err := models.ResetRunningAgentReports(idx, reason); err != nil {
			log.Printf("failed to reset running agent reports of %s: %v", mb.Address, err)
		}
		idx.Close()
	}
}

// --- Execution ---

// progressWriter collects progress lines and writes them to the jobs table at
// most once per progressFlushInterval, forwarding each line to an optional
// echo callback (the CLI prints them).
type progressWriter struct {
	db    *sql.DB
	jobID int64
	echo  func(string)

	mu      sync.Mutex
	lines   []string
	dirty   bool
	last    time.Time
	timer   *time.Timer
	stopped bool
}

func newProgressWriter(db *sql.DB, jobID int64, echo func(string)) *progressWriter {
	return &progressWriter{db: db, jobID: jobID, echo: echo}
}

// Add records one progress line.
func (p *progressWriter) Add(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	if p.echo != nil {
		p.echo(msg)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.lines = append(p.lines, msg)
	if len(p.lines) > progressMaxLines {
		p.lines = p.lines[len(p.lines)-progressMaxLines:]
	}
	p.dirty = true
	if since := time.Since(p.last); since >= progressFlushInterval {
		p.flushLocked()
		return
	} else if p.timer == nil {
		p.timer = time.AfterFunc(progressFlushInterval-since, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.timer = nil
			if p.dirty && !p.stopped {
				p.flushLocked()
			}
		})
	}
}

func (p *progressWriter) flushLocked() {
	p.dirty = false
	p.last = time.Now()
	if err := models.UpdateJobProgress(p.db, p.jobID, strings.Join(p.lines, "\n")); err != nil {
		log.Printf("failed to write job progress: %v", err)
	}
}

// Close flushes pending lines and stops the timer.
func (p *progressWriter) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if p.dirty {
		p.flushLocked()
	}
}

// Text returns the collected progress lines.
func (p *progressWriter) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.lines, "\n")
}

// execute runs one claimed job to completion and records the outcome.
func (m *JobManager) execute(ctx context.Context, job *models.Job, echo func(string)) {
	pw := newProgressWriter(m.db, job.ID, echo)
	result, err := m.RunJob(ctx, job, pw.Add)
	pw.Close()
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
		if ctx.Err() != nil && !strings.Contains(errMsg, "shutting down") {
			errMsg = "interrupted: " + errMsg
		}
	}
	if ferr := models.FinishJob(m.db, job.ID, result, errMsg); ferr != nil {
		log.Printf("failed to finish job %d: %v", job.ID, ferr)
	}
	job.Result, job.ErrorMessage, job.Progress = result, errMsg, pw.Text()
	if errMsg != "" {
		job.Status = JobStatusError
	} else {
		job.Status = JobStatusDone
	}
}

// RunJob dispatches a job by kind and returns its result text. It does not
// touch the job row itself (execute / RunJobInline do).
func (m *JobManager) RunJob(ctx context.Context, job *models.Job, progress func(string)) (string, error) {
	if progress == nil {
		progress = func(string) {}
	}
	switch job.Kind {
	case JobKindCheck:
		return m.runCheck(ctx, job, progress)
	case JobKindReindex:
		return m.runRebuild(ctx, job, progress, mailengine.Reindex, "reindex")
	case JobKindReclassify:
		return m.runRebuild(ctx, job, progress, mailengine.Reclassify, "reclassify")
	case JobKindAnalyze:
		return m.runAnalyze(ctx, job, progress)
	}
	return "", fmt.Errorf("%w %q", ErrJobKind, job.Kind)
}

// RunJobInline creates a job row, runs it in the current process and then
// runs the follow-up jobs it queued (agent analysis). Progress lines are
// passed to echo. It is used by the CLI when no server is running.
func (m *JobManager) RunJobInline(ctx context.Context, kind string, mailboxID int64, target, requestedBy string, echo func(string)) (*models.Job, error) {
	job, created, err := EnqueueJob(m.db, kind, mailboxID, target, requestedBy, nil)
	if err != nil {
		return nil, err
	}
	if !created {
		return job, fmt.Errorf("an identical job (#%d) is already %s", job.ID, job.Status)
	}
	first := job
	for {
		claimed, err := models.ClaimJobByID(m.db, job.ID)
		if err != nil {
			return first, err
		}
		if claimed == nil {
			return first, fmt.Errorf("job #%d was taken by another process", job.ID)
		}
		if claimed.ID != first.ID && echo != nil {
			echo(fmt.Sprintf("--- job #%d (%s %s)", claimed.ID, claimed.Kind, claimed.Target))
		}
		m.execute(ctx, claimed, echo)
		if claimed.ID == first.ID {
			first = claimed
		}
		if ctx.Err() != nil {
			return first, ctx.Err()
		}
		next, err := models.NextQueuedJobAfter(m.db, first.ID, requestedBy)
		if err != nil || next == nil {
			return first, err
		}
		job = next
	}
}

// targetMailboxes resolves the mailboxes a job applies to: the one named by
// mailbox_id, or (when NULL) every mailbox; enabledOnly filters disabled ones.
func (m *JobManager) targetMailboxes(job *models.Job, enabledOnly bool) ([]*models.Mailbox, error) {
	if job.MailboxID.Valid {
		mb, err := models.GetMailboxByID(m.db, job.MailboxID.Int64)
		if err == sql.ErrNoRows {
			return nil, errors.New("mailbox not found")
		}
		if err != nil {
			return nil, err
		}
		return []*models.Mailbox{mb}, nil
	}
	all, err := models.ListMailboxes(m.db)
	if err != nil {
		return nil, err
	}
	if !enabledOnly {
		return all, nil
	}
	var out []*models.Mailbox
	for _, mb := range all {
		if mb.Enabled {
			out = append(out, mb)
		}
	}
	return out, nil
}

// agentAutoAnalysis reports whether analysis jobs should be queued after a
// check: the agent must be enabled and its CLI available.
func (m *JobManager) agentAutoAnalysis(progress func(string)) bool {
	if !ResolveAgentEnabled(m.db) {
		return false
	}
	provider := ResolveAgentProvider(m.db)
	if !agent.IsValidProvider(provider) || !agent.ProviderAvailable(provider) {
		progress(fmt.Sprintf("agent provider %q is not available; analysis skipped", provider))
		return false
	}
	return true
}

func (m *JobManager) runCheck(ctx context.Context, job *models.Job, progress func(string)) (string, error) {
	mailboxes, err := m.targetMailboxes(job, true)
	if err != nil {
		return "", err
	}
	if len(mailboxes) == 0 {
		return "no enabled mailbox", nil
	}
	var summary, failures []string
	autoAnalyze := m.agentAutoAnalysis(progress)
	for _, mb := range mailboxes {
		if ctx.Err() != nil {
			return strings.Join(summary, "; "), errors.New("server shutting down")
		}
		progress(fmt.Sprintf("[%s] checking", mb.Address))
		res, err := m.checkOne(ctx, mb, progress)
		if err != nil {
			progress(fmt.Sprintf("[%s] error: %v", mb.Address, err))
			failures = append(failures, fmt.Sprintf("%s: %v", mb.Address, err))
			continue
		}
		line := fmt.Sprintf("%s: fetched %d, bounces %d, skipped %d", mb.Address, res.Fetched, res.Bounces, res.Skipped)
		progress("[" + line + "]")
		summary = append(summary, line)
		if autoAnalyze {
			for _, key := range res.GroupsTouched {
				if _, _, err := m.Enqueue(JobKindAnalyze, mb.ID, key, job.RequestedBy); err != nil {
					progress(fmt.Sprintf("[%s] failed to queue analysis of %s: %v", mb.Address, key, err))
				}
			}
			if len(res.GroupsTouched) > 0 {
				progress(fmt.Sprintf("[%s] queued analysis of %d group(s)", mb.Address, len(res.GroupsTouched)))
			}
		}
	}
	result := strings.Join(summary, "; ")
	if len(failures) > 0 {
		return result, errors.New(strings.Join(failures, "; "))
	}
	return result, nil
}

// checkOne runs CheckMailbox for one mailbox and records the outcome on the
// mailbox row.
func (m *JobManager) checkOne(ctx context.Context, mb *models.Mailbox, progress func(string)) (*mailengine.CheckResult, error) {
	password, err := MailboxPassword(m.key, mb)
	if err != nil {
		_ = models.UpdateMailboxCheckResult(m.db, mb.ID, "error", err.Error(), mb.LastUIDValidity)
		return nil, err
	}
	res, err := mailengine.CheckMailbox(ctx, m.mailsRoot, mb, password, func(msg string) {
		progress("[" + mb.Address + "] " + msg)
	})
	if err != nil {
		_ = models.UpdateMailboxCheckResult(m.db, mb.ID, "error", err.Error(), mb.LastUIDValidity)
		return nil, err
	}
	uidValidity := mb.LastUIDValidity
	if res.UIDValidity != 0 {
		uidValidity = int64(res.UIDValidity)
	}
	if err := models.UpdateMailboxCheckResult(m.db, mb.ID, "ok", "", uidValidity); err != nil {
		return nil, err
	}
	return res, nil
}

type rebuildFunc func(ctx context.Context, mailsRoot, address string, progress mailengine.Progress) (*mailengine.ReindexResult, error)

func (m *JobManager) runRebuild(ctx context.Context, job *models.Job, progress func(string), fn rebuildFunc, name string) (string, error) {
	mailboxes, err := m.targetMailboxes(job, false)
	if err != nil {
		return "", err
	}
	if len(mailboxes) == 0 {
		return "no mailbox", nil
	}
	var summary, failures []string
	autoAnalyze := m.agentAutoAnalysis(progress)
	for _, mb := range mailboxes {
		if ctx.Err() != nil {
			return strings.Join(summary, "; "), errors.New("server shutting down")
		}
		progress(fmt.Sprintf("[%s] %s", mb.Address, name))
		res, err := fn(ctx, m.mailsRoot, mb.Address, func(msg string) { progress("[" + mb.Address + "] " + msg) })
		if err != nil {
			progress(fmt.Sprintf("[%s] error: %v", mb.Address, err))
			failures = append(failures, fmt.Sprintf("%s: %v", mb.Address, err))
			continue
		}
		line := fmt.Sprintf("%s: messages %d, bounces %d, groups %d", mb.Address, res.Messages, res.Bounces, res.Groups)
		progress("[" + line + "]")
		summary = append(summary, line)
		// Reports of groups whose key survived the rebuild are carried over,
		// so only groups flagged needs_analysis (new or grown) are analyzed.
		if autoAnalyze && res.Groups > 0 {
			if _, _, err := m.Enqueue(JobKindAnalyze, mb.ID, "", job.RequestedBy); err != nil {
				progress(fmt.Sprintf("[%s] failed to queue analysis: %v", mb.Address, err))
			} else {
				progress(fmt.Sprintf("[%s] queued analysis of groups that need it", mb.Address))
			}
		}
	}
	result := strings.Join(summary, "; ")
	if len(failures) > 0 {
		return result, errors.New(strings.Join(failures, "; "))
	}
	return result, nil
}

func (m *JobManager) runAnalyze(ctx context.Context, job *models.Job, progress func(string)) (string, error) {
	if !job.MailboxID.Valid {
		return "", errors.New("analyze requires a mailbox")
	}
	mb, err := models.GetMailboxByID(m.db, job.MailboxID.Int64)
	if err == sql.ErrNoRows {
		return "", errors.New("mailbox not found")
	}
	if err != nil {
		return "", err
	}
	provider := ResolveAgentProvider(m.db)
	if !agent.IsValidProvider(provider) {
		return "", fmt.Errorf("agent provider %q is not registered", provider)
	}
	if !agent.ProviderAvailable(provider) {
		return "", fmt.Errorf("agent provider %q is not available on this machine", provider)
	}
	idx, err := mailengine.OpenIndex(ctx, m.mailsRoot, mb.Address, func(msg string) { progress("[" + mb.Address + "] " + msg) })
	if err != nil {
		return "", fmt.Errorf("failed to open the index of %s: %w", mb.Address, err)
	}
	defer idx.Close()

	var groups []*models.BounceGroup
	switch job.Target {
	case "":
		groups, err = models.ListGroupsNeedingAnalysis(idx)
	case analyzeAllTarget:
		groups, err = models.ListGroups(idx, models.GroupFilter{})
	default:
		var g *models.BounceGroup
		g, err = models.GetGroup(idx, job.Target)
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("group %q not found", job.Target)
		}
		if err == nil {
			groups = []*models.BounceGroup{g}
		}
	}
	if err != nil {
		return "", err
	}
	if len(groups) == 0 {
		return "no group needs analysis", nil
	}
	progress(fmt.Sprintf("[%s] analyzing %d group(s) with %s", mb.Address, len(groups), provider))
	done, failed := 0, 0
	var failures []string
	for i, g := range groups {
		if ctx.Err() != nil {
			return fmt.Sprintf("analyzed %d, failed %d", done, failed), errors.New("server shutting down")
		}
		progress(fmt.Sprintf("[%s] (%d/%d) %s", mb.Address, i+1, len(groups), g.Title))
		report, err := agent.AnalyzeGroup(ctx, agent.AnalyzeInput{
			MailsRoot: m.mailsRoot, AgentRoot: m.agentRoot, TemplatesFS: m.templates,
			Address: mb.Address, Index: idx, GroupKey: g.GroupKey, Provider: provider, Language: "ja",
		}, func(msg string) { progress("[" + mb.Address + "] " + msg) })
		switch {
		case err != nil:
			failed++
			failures = append(failures, fmt.Sprintf("%s: %v", g.GroupKey, err))
			progress(fmt.Sprintf("[%s] %s: error: %v", mb.Address, g.GroupKey, err))
		case report != nil && report.Status == "error":
			failed++
			failures = append(failures, fmt.Sprintf("%s: %s", g.GroupKey, report.ErrorMessage))
			progress(fmt.Sprintf("[%s] %s: agent failed: %s", mb.Address, g.GroupKey, report.ErrorMessage))
		default:
			done++
			progress(fmt.Sprintf("[%s] %s: completed", mb.Address, g.GroupKey))
		}
	}
	result := fmt.Sprintf("analyzed %d, failed %d", done, failed)
	if failed > 0 {
		return result, errors.New(strings.Join(failures, "; "))
	}
	return result, nil
}
