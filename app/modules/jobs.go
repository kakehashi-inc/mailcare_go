package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/agent"
	"mailcare/app/modules/mailengine"
)

// Jobs (jobs table).
//
// A job is one unit of background work: sync / fetch / group / analyze /
// reindex / reclassify / notify / cleanup. Jobs are queued in the master database and executed
// by the JobManager, which runs up to "workers" jobs at once. Jobs that touch
// the same IMAP server, the same mailbox index or the agent CLI hold resource
// keys (see resourceKeys) and are serialized against each other. A job
// without a mailbox is an expansion job: it queues one child per target
// mailbox and finishes. The CLI uses the same runner in process
// (RunJobInline) when no server is running.

const (
	// progressFlushInterval throttles progress writes to the jobs table.
	progressFlushInterval = 500 * time.Millisecond
	// progressMaxLines bounds the progress text kept per job (newest lines).
	progressMaxLines = 100
	// progressTimeSep separates the UTC timestamp (RFC 3339) that starts each
	// stored progress line from the message.
	progressTimeSep = "\t"
	// progressEchoLayout is the local time format of progress lines printed
	// by the CLI (the Web screen formats them in the user's time zone).
	progressEchoLayout = "01-02 15:04:05"
	// jobRetention is how long finished jobs are kept (the daily cleanup job
	// removes older ones).
	jobRetention = 30 * 24 * time.Hour
	// analyzeAllTarget is the analyze job target meaning "every actionable group".
	analyzeAllTarget = "*"
)

// Resource keys held by running jobs (see the system design document (Documents) 7.2).
const (
	lockAgent         = "agent"
	lockNotify        = "notify"
	lockHostPrefix    = "host:"
	lockMailboxPrefix = "mailbox:"
	lockAnalyzePrefix = "analyze:"
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
	case JobKindSync, JobKindFetch, JobKindGroup, JobKindAnalyze, JobKindReindex, JobKindReclassify, JobKindNotify, JobKindCleanup:
		return nil
	}
	return fmt.Errorf("%w %q", ErrJobKind, kind)
}

// JobOfDeletedMailbox reports whether a job without a mailbox id belonged
// to a mailbox that was deleted since: deleting a mailbox keeps its job
// history with mailbox_id set to NULL (the system design document, 7.1),
// which makes those rows look like expansion jobs. A job with a parent (a
// child of an expansion job or a follow-up analysis) always had a mailbox,
// and so did a queued job that was canceled without a result (the deletion
// cancels the waiting jobs). Expansion jobs and notify jobs never have one.
func JobOfDeletedMailbox(j *models.Job) bool {
	if j.MailboxID.Valid || j.Kind == JobKindNotify {
		return false
	}
	return j.ParentID != 0 || (j.Status == JobStatusCanceled && j.Result == "")
}

// NotifyTestTarget builds the notify job target that sends a test mail to
// one address instead of the notification.
func NotifyTestTarget(address string) string {
	return notifyTestPrefix + address
}

// normalizeNotifyTarget validates a notify job target: "" (send the
// notification) or "test:<address>".
func normalizeNotifyTarget(target string) (string, error) {
	if target == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(target, notifyTestPrefix)
	if !ok {
		return "", fmt.Errorf("invalid notify target %q (use \"\" or \"test:<address>\")", target)
	}
	address, err := NormalizeEmail(raw)
	if err != nil {
		return "", err
	}
	if address == "" {
		return "", errors.New("the test mail needs a recipient address")
	}
	return NotifyTestTarget(address), nil
}

// JobManager queues and runs jobs.
type JobManager struct {
	db        *sql.DB
	key       []byte
	mailsRoot string
	agentRoot string
	templates fs.FS

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	wake    chan struct{}
	workers int
	running int

	// claimMu serializes claim attempts so the lock check and the claim of
	// a job form one step within this process.
	claimMu sync.Mutex
	// lockMu guards locks: resource key -> id of the job holding it.
	lockMu sync.Mutex
	locks  map[string]int64
}

// NewJobManager creates a manager over the master database. key is the
// master key (to decrypt IMAP passwords), mailsRoot and agentRoot are the
// mails and agent directories (MailsDir / AgentDir); templates (the embedded
// agent instruction templates) may be nil. The worker count starts from the
// workers setting.
func NewJobManager(db *sql.DB, key []byte, mailsRoot, agentRoot string, templates fs.FS) *JobManager {
	return &JobManager{db: db, key: key, mailsRoot: mailsRoot, agentRoot: agentRoot, templates: templates,
		wake: make(chan struct{}, 1), workers: ResolveWorkers(db), locks: map[string]int64{}}
}

// Workers returns the number of jobs that may run at once.
func (m *JobManager) Workers() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workers
}

// SetWorkers changes the number of jobs that may run at once (clamped to
// 1..MaxWorkers). It takes effect immediately: a running dispatcher starts
// more jobs when the number grew, and lets running jobs finish when it
// shrank.
func (m *JobManager) SetWorkers(n int) {
	n = ClampWorkers(n)
	m.mu.Lock()
	m.workers = n
	m.mu.Unlock()
	m.notify()
}

// Start launches the dispatcher goroutine. It is a no-op when already running.
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

// Stop cancels the running jobs (if any) and waits for the dispatcher and
// every job goroutine to exit.
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

// notify wakes the dispatcher (non-blocking).
func (m *JobManager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *JobManager) loop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(JobPollInterval)
	defer ticker.Stop()
	for {
		m.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.wake:
		}
	}
}

// dispatch starts runnable jobs until the worker count is reached or no
// queued job can run with the resource keys currently held.
func (m *JobManager) dispatch(ctx context.Context) {
	for ctx.Err() == nil {
		m.mu.Lock()
		free := m.running < m.workers
		m.mu.Unlock()
		if !free {
			return
		}
		job, err := m.claimRunnable(nil)
		if err != nil {
			log.Printf("job claim failed: %v", err)
			return
		}
		if job == nil {
			return
		}
		m.mu.Lock()
		m.running++
		m.mu.Unlock()
		m.wg.Add(1)
		go func(job *models.Job) {
			defer m.wg.Done()
			m.execute(ctx, job, nil)
			m.releaseLocks(job.ID)
			m.mu.Lock()
			m.running--
			m.mu.Unlock()
			m.notify()
		}(job)
	}
}

// claimRunnable claims the oldest queued job whose resource keys are free
// (and that accept accepts, when given) and reserves its keys. It returns
// nil when nothing is runnable.
func (m *JobManager) claimRunnable(accept func(*models.Job) bool) (*models.Job, error) {
	m.claimMu.Lock()
	defer m.claimMu.Unlock()
	keysOf := map[int64][]string{}
	job, err := models.ClaimNextRunnableJob(m.db, func(j *models.Job) bool {
		if accept != nil && !accept(j) {
			return false
		}
		keys := m.resourceKeys(j)
		keysOf[j.ID] = keys
		return m.locksFree(keys)
	})
	if err != nil || job == nil {
		return nil, err
	}
	m.acquireLocks(job.ID, keysOf[job.ID])
	return job, nil
}

// claimByID claims one specific queued job and reserves its resource keys
// (used by the in-process CLI runner, which waits for nothing: no other job
// runs in that process).
func (m *JobManager) claimByID(id int64) (*models.Job, error) {
	m.claimMu.Lock()
	defer m.claimMu.Unlock()
	job, err := models.ClaimJobByID(m.db, id)
	if err != nil || job == nil {
		return nil, err
	}
	m.acquireLocks(job.ID, m.resourceKeys(job))
	return job, nil
}

// resourceKeys returns the resource keys a job holds while it runs. Mailbox
// host and address are read from the mailboxes row at claim time; a job
// whose mailbox is gone holds nothing (it fails when it runs).
func (m *JobManager) resourceKeys(job *models.Job) []string {
	if job.Kind == JobKindNotify {
		return []string{lockNotify}
	}
	if !job.MailboxID.Valid {
		return nil
	}
	mb, err := models.GetMailboxByID(m.db, job.MailboxID.Int64)
	if err != nil {
		return nil
	}
	return jobResourceKeys(job.Kind, mb)
}

// jobResourceKeys maps a job kind and its mailbox to the resource keys of
// the system design document (Documents) 7.2.
func jobResourceKeys(kind string, mb *models.Mailbox) []string {
	mailboxKey := lockMailboxPrefix + mb.Address
	switch kind {
	case JobKindSync, JobKindFetch:
		return []string{lockHostPrefix + strings.ToLower(strings.TrimSpace(mb.ImapHost)), mailboxKey}
	case JobKindGroup, JobKindReclassify:
		return []string{mailboxKey}
	case JobKindAnalyze:
		return []string{lockAgent, lockAnalyzePrefix + mb.Address}
	case JobKindReindex, JobKindCleanup:
		// Both rewrite or remove mails and groups the agent may be reading
		// (the prompt lists raw files), so they never overlap an analysis.
		return []string{mailboxKey, lockAgent}
	}
	return nil
}

func (m *JobManager) locksFree(keys []string) bool {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()
	for _, k := range keys {
		if _, held := m.locks[k]; held {
			return false
		}
	}
	return true
}

func (m *JobManager) acquireLocks(jobID int64, keys []string) {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()
	for _, k := range keys {
		m.locks[k] = jobID
	}
}

// releaseLocks frees every resource key held by the job.
func (m *JobManager) releaseLocks(jobID int64) {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()
	for k, id := range m.locks {
		if id == jobID {
			delete(m.locks, k)
		}
	}
}

// heldLocks returns the resource keys currently held, sorted (for the
// tests).
func (m *JobManager) heldLocks() []string {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()
	out := make([]string, 0, len(m.locks))
	for k := range m.locks {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Enqueue queues a job unless an identical queued/running job exists, in
// which case that job is returned with created=false. mailboxID 0 means "no
// mailbox": an expansion job over every (enabled) mailbox.
func (m *JobManager) Enqueue(kind string, mailboxID int64, target, requestedBy string) (job *models.Job, created bool, err error) {
	return EnqueueJob(m.db, kind, mailboxID, target, requestedBy, 0, m.wake)
}

// enqueueChild queues a job caused by parent (same requester, parent id
// recorded) so that the CLI can follow the jobs its own request caused.
func (m *JobManager) enqueueChild(kind string, mailboxID int64, target string, parent *models.Job) (*models.Job, bool, error) {
	return EnqueueJob(m.db, kind, mailboxID, target, parent.RequestedBy, parent.ID, m.wake)
}

// EnqueueJob is Enqueue without a manager (used by the CLI and the server
// alike). wake may be nil.
func EnqueueJob(db *sql.DB, kind string, mailboxID int64, target, requestedBy string, parentID int64, wake chan struct{}) (*models.Job, bool, error) {
	if err := ValidateJobKind(kind); err != nil {
		return nil, false, err
	}
	target = strings.TrimSpace(target)
	switch kind {
	case JobKindAnalyze:
		if mailboxID == 0 && target != "" && target != analyzeAllTarget {
			return nil, false, errors.New("analyze of one group requires a mailbox")
		}
	case JobKindNotify:
		if mailboxID != 0 {
			return nil, false, errors.New("notify takes no mailbox")
		}
		var err error
		if target, err = normalizeNotifyTarget(target); err != nil {
			return nil, false, err
		}
	default:
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
	job := &models.Job{Kind: kind, Target: target, RequestedBy: requestedBy, ParentID: parentID}
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
// by a previous process are marked as error. Nothing is deleted here; the
// job history is pruned by the daily cleanup job (pruneJobHistory).
func ResetStaleJobs(db *sql.DB, mailsRoot string) {
	const reason = "interrupted by a server restart"
	if err := models.ResetRunningJobs(db, reason); err != nil {
		log.Printf("failed to reset running jobs: %v", err)
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
// echo callback (the CLI prints them). Each stored line starts with the time
// it was added (see stampProgressLine).
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
	line := stampProgressLine(time.Now(), msg)
	if p.echo != nil {
		p.echo(FormatProgressLine(line))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.lines = append(p.lines, line)
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

// stampProgressLine prefixes a progress message with its UTC time.
func stampProgressLine(t time.Time, msg string) string {
	return t.UTC().Format(time.RFC3339) + progressTimeSep + msg
}

// FormatProgressLine renders a stored progress line as "MM-dd HH:mm:ss msg"
// in local time.
func FormatProgressLine(line string) string {
	stamp, msg, _ := strings.Cut(line, progressTimeSep)
	t, _ := time.Parse(time.RFC3339, stamp)
	return t.Local().Format(progressEchoLayout) + " " + msg
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
// touch the job row itself (execute / RunJobInline do). A job without a
// mailbox expands into one child job per target mailbox.
func (m *JobManager) RunJob(ctx context.Context, job *models.Job, progress func(string)) (string, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if err := ValidateJobKind(job.Kind); err != nil {
		return "", err
	}
	if job.Kind == JobKindNotify {
		if job.MailboxID.Valid {
			return "", errors.New("notify takes no mailbox")
		}
		return m.runNotify(ctx, job, progress)
	}
	if !job.MailboxID.Valid {
		return m.runExpansion(ctx, job, progress)
	}
	mb, err := models.GetMailboxByID(m.db, job.MailboxID.Int64)
	if err == sql.ErrNoRows {
		return "", errors.New("mailbox not found")
	}
	if err != nil {
		return "", err
	}
	// A job runs for exactly one mailbox, which its row already names, so the
	// progress lines do not repeat the address.
	switch job.Kind {
	case JobKindSync:
		return m.runSync(ctx, job, mb, progress)
	case JobKindFetch:
		return m.runFetch(ctx, mb, progress)
	case JobKindGroup:
		return m.runGroup(ctx, job, mb, false, progress)
	case JobKindReclassify:
		return m.runGroup(ctx, job, mb, true, progress)
	case JobKindReindex:
		return m.runReindex(ctx, job, mb, progress)
	case JobKindAnalyze:
		return m.runAnalyze(ctx, job, mb, progress)
	case JobKindCleanup:
		return m.runCleanup(ctx, mb, progress)
	}
	return "", fmt.Errorf("%w %q", ErrJobKind, job.Kind)
}

// RunJobInline creates a job row, runs it in the current process and then
// runs the jobs it caused (children of an expansion job and the follow-up
// analysis) one after another. Progress lines are passed to echo. It is
// used by the CLI when no server is running.
func (m *JobManager) RunJobInline(ctx context.Context, kind string, mailboxID int64, target, requestedBy string, echo func(string)) (*models.Job, error) {
	job, created, err := EnqueueJob(m.db, kind, mailboxID, target, requestedBy, 0, nil)
	if err != nil {
		return nil, err
	}
	if !created {
		return job, fmt.Errorf("an identical job (#%d) is already %s", job.ID, job.Status)
	}
	first, err := m.claimByID(job.ID)
	if err != nil {
		return job, err
	}
	if first == nil {
		return job, fmt.Errorf("job #%d was taken by another process", job.ID)
	}
	m.execute(ctx, first, echo)
	m.releaseLocks(first.ID)
	// Only the jobs this run caused (children and their follow-ups) are
	// executed here; jobs another CLI process queued meanwhile are left alone.
	mine := map[int64]bool{first.ID: true}
	for ctx.Err() == nil {
		next, err := m.claimRunnable(func(j *models.Job) bool {
			return mine[j.ParentID]
		})
		if err != nil || next == nil {
			return first, err
		}
		mine[next.ID] = true
		if echo != nil {
			echo(fmt.Sprintf("--- job #%d (%s %s)", next.ID, next.Kind, strings.TrimSpace(next.Target+" "+m.addressOf(next))))
		}
		m.execute(ctx, next, echo)
		m.releaseLocks(next.ID)
	}
	return first, ctx.Err()
}

// addressOf returns the mailbox address of a job ("" when it has none).
func (m *JobManager) addressOf(job *models.Job) string {
	if !job.MailboxID.Valid {
		return ""
	}
	mb, err := models.GetMailboxByID(m.db, job.MailboxID.Int64)
	if err != nil {
		return ""
	}
	return mb.Address
}

// runExpansion queues one child job per target mailbox (enabled mailboxes
// for sync / fetch, every mailbox otherwise) with the requester of the
// parent, and finishes.
func (m *JobManager) runExpansion(ctx context.Context, job *models.Job, progress func(string)) (string, error) {
	all, err := models.ListMailboxes(m.db)
	if err != nil {
		return "", err
	}
	enabledOnly := job.Kind == JobKindSync || job.Kind == JobKindFetch
	var targets []*models.Mailbox
	for _, mb := range all {
		if enabledOnly && !mb.Enabled {
			continue
		}
		targets = append(targets, mb)
	}
	if len(targets) == 0 {
		if enabledOnly {
			return "queued 0 jobs (no enabled mailbox)", nil
		}
		if job.Kind == JobKindCleanup {
			// No child to hand the work to: the job history is pruned here
			// so that the daily cleanup still applies that retention.
			removed, err := m.pruneJobHistory(progress)
			if err != nil {
				return "queued 0 jobs (no mailbox)", err
			}
			return fmt.Sprintf("queued 0 jobs (no mailbox), removed %d jobs", removed), nil
		}
		return "queued 0 jobs (no mailbox)", nil
	}
	queued := 0
	for _, mb := range targets {
		if ctx.Err() != nil {
			return fmt.Sprintf("queued %d jobs", queued), errors.New("server shutting down")
		}
		child, created, err := m.enqueueChild(job.Kind, mb.ID, job.Target, job)
		if err != nil {
			progress(fmt.Sprintf("[%s] failed to queue %s: %v", mb.Address, job.Kind, err))
			continue
		}
		if created {
			queued++
			progress(fmt.Sprintf("[%s] queued %s job #%d", mb.Address, job.Kind, child.ID))
		} else {
			progress(fmt.Sprintf("[%s] %s job #%d is already %s", mb.Address, job.Kind, child.ID, child.Status))
		}
	}
	return fmt.Sprintf("queued %d jobs", queued), nil
}

// agentAutoAnalysis reports whether analysis jobs should be queued after a
// sync / group / rebuild: the agent must be enabled and its CLI available.
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

// queueAnalysis queues the follow-up analysis of the groups of a mailbox
// that need it, when the agent is enabled and available.
func (m *JobManager) queueAnalysis(job *models.Job, mb *models.Mailbox, progress func(string)) {
	if !m.agentAutoAnalysis(progress) {
		return
	}
	if _, _, err := m.enqueueChild(JobKindAnalyze, mb.ID, "", job); err != nil {
		progress(fmt.Sprintf("failed to queue analysis: %v", err))
		return
	}
	progress("queued analysis of groups that need it")
}

// fetchOne runs FetchMailbox for one mailbox and records the outcome on the
// mailbox row. The search window never reaches before the mail retention
// (mail_keep_days): mail older than that would only be removed again by the
// daily cleanup.
func (m *JobManager) fetchOne(ctx context.Context, mb *models.Mailbox, progress func(string)) (*mailengine.FetchResult, error) {
	password, err := MailboxPassword(m.key, mb)
	if err != nil {
		_ = models.UpdateMailboxFetchResult(m.db, mb.ID, err.Error())
		return nil, err
	}
	opts := mailengine.FetchOptions{NotBefore: time.Now().UTC().AddDate(0, 0, -ResolveMailKeepDays(m.db))}
	res, err := mailengine.FetchMailbox(ctx, m.mailsRoot, mb, password, opts, mailengine.Progress(progress))
	if err != nil {
		_ = models.UpdateMailboxFetchResult(m.db, mb.ID, err.Error())
		return nil, err
	}
	if err := models.UpdateMailboxFetchResult(m.db, mb.ID, ""); err != nil {
		return nil, err
	}
	return res, nil
}

// runFetch downloads new mail into the index (no classification).
func (m *JobManager) runFetch(ctx context.Context, mb *models.Mailbox, progress func(string)) (string, error) {
	progress("fetching")
	res, err := m.fetchOne(ctx, mb, progress)
	if err != nil {
		progress(fmt.Sprintf("error: %v", err))
		return "", fmt.Errorf("%s: %w", mb.Address, err)
	}
	line := fmt.Sprintf("fetched %d, skipped %d", res.Fetched, res.Skipped)
	progress(line)
	return line, nil
}

// runCleanup applies the retentions for one mailbox (the daily cleanup job,
// also runnable by hand): first the mails older than mail_keep_days are
// removed (mailengine.PruneMailbox: files and index rows, the groups they
// belonged to recounted, the groups left empty deleted with their reports)
// together with the leftovers of interrupted writes in the mailbox
// directory (mailengine.RemoveStaleTempFiles; counted in a progress line
// only), then the agent run directories older than agent_keep_days
// (agent.CleanupWorkspaces), then the finished jobs older than jobRetention
// (the job history is global, so every child of the daily run tries it and
// the first one does the work). A later step runs even when an earlier one
// failed; the result line counts what was removed and any failure makes the
// job fail afterwards (what could not be removed is tried again next time).
func (m *JobManager) runCleanup(ctx context.Context, mb *models.Mailbox, progress func(string)) (string, error) {
	mailKeep := time.Duration(ResolveMailKeepDays(m.db)) * 24 * time.Hour
	agentKeep := time.Duration(ResolveAgentKeepDays(m.db)) * 24 * time.Hour
	progress(fmt.Sprintf("cleaning up (mail retention %s, agent workspace retention %s, job history %s)",
		keepDaysLabel(mailKeep), keepDaysLabel(agentKeep), keepDaysLabel(jobRetention)))
	var errs []error
	pruned, err := mailengine.PruneMailbox(ctx, m.mailsRoot, mb.Address, mailKeep, mailengine.Progress(progress))
	if err != nil {
		progress(fmt.Sprintf("error: mail retention: %v", err))
		errs = append(errs, fmt.Errorf("mail retention: %w", err))
	}
	if pruned == nil {
		pruned = &mailengine.PruneResult{}
	}
	if ctx.Err() != nil {
		return fmt.Sprintf("removed %d messages, %d groups, 0 agent workspaces, 0 jobs", pruned.Removed, pruned.RemovedGroups), errors.New("server shutting down")
	}
	if _, err := mailengine.RemoveStaleTempFiles(m.mailsRoot, mb.Address, mailengine.Progress(progress)); err != nil {
		progress(fmt.Sprintf("error: temporary files: %v", err))
		errs = append(errs, fmt.Errorf("temporary files: %w", err))
	}
	workspaces, err := agent.CleanupWorkspaces(m.agentRoot, mb.Address, agentKeep)
	if workspaces > 0 {
		progress(fmt.Sprintf("removed %d expired agent workspace(s)", workspaces))
	}
	if err != nil {
		progress(fmt.Sprintf("error: agent workspaces: %v", err))
		errs = append(errs, fmt.Errorf("agent workspaces: %w", err))
	}
	jobs, err := m.pruneJobHistory(progress)
	if err != nil {
		errs = append(errs, err)
	}
	line := fmt.Sprintf("removed %d messages, %d groups, %d agent workspaces, %d jobs", pruned.Removed, pruned.RemovedGroups, workspaces, jobs)
	progress(line)
	if len(errs) > 0 {
		return line, fmt.Errorf("%s: %w", mb.Address, errors.Join(errs...))
	}
	return line, nil
}

// pruneJobHistory deletes the finished jobs older than jobRetention and
// returns how many went (a progress line reports a non-zero count).
func (m *JobManager) pruneJobHistory(progress func(string)) (int64, error) {
	removed, err := models.DeleteOldJobs(m.db, time.Now().Add(-jobRetention))
	if err != nil {
		progress(fmt.Sprintf("error: job history: %v", err))
		return 0, fmt.Errorf("job history: %w", err)
	}
	if removed > 0 {
		progress(fmt.Sprintf("removed %d finished job(s) older than %s", removed, keepDaysLabel(jobRetention)))
	}
	return removed, nil
}

// keepDaysLabel renders a retention for progress lines ("30 days", "1 day").
func keepDaysLabel(keep time.Duration) string {
	days := int(keep / (24 * time.Hour))
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

// runGroup classifies and groups the messages not grouped yet (full = false)
// or every message (full = true, reclassify), then queues the analysis.
func (m *JobManager) runGroup(ctx context.Context, job *models.Job, mb *models.Mailbox, full bool, progress func(string)) (string, error) {
	if full {
		progress("reclassifying")
	} else {
		progress("grouping")
	}
	res, err := mailengine.GroupMailbox(ctx, m.mailsRoot, mb.Address, full, mailengine.Progress(progress))
	if err != nil {
		progress(fmt.Sprintf("error: %v", err))
		return "", fmt.Errorf("%s: %w", mb.Address, err)
	}
	line := fmt.Sprintf("processed %d, bounces %d, groups %d", res.Processed, res.Bounces, res.Groups)
	progress(line)
	m.queueAnalysis(job, mb, progress)
	return line, nil
}

// runSync fetches, groups the new messages and queues the analysis when an
// actionable group gained messages.
func (m *JobManager) runSync(ctx context.Context, job *models.Job, mb *models.Mailbox, progress func(string)) (string, error) {
	progress("fetching")
	fetched, err := m.fetchOne(ctx, mb, progress)
	if err != nil {
		progress(fmt.Sprintf("error: %v", err))
		return "", fmt.Errorf("%s: %w", mb.Address, err)
	}
	progress(fmt.Sprintf("fetched %d, skipped %d", fetched.Fetched, fetched.Skipped))
	if ctx.Err() != nil {
		return fmt.Sprintf("fetched %d, skipped %d", fetched.Fetched, fetched.Skipped), errors.New("server shutting down")
	}
	progress("grouping")
	grouped, err := mailengine.GroupMailbox(ctx, m.mailsRoot, mb.Address, false, mailengine.Progress(progress))
	if err != nil {
		progress(fmt.Sprintf("error: %v", err))
		return fmt.Sprintf("fetched %d, skipped %d", fetched.Fetched, fetched.Skipped), fmt.Errorf("%s: %w", mb.Address, err)
	}
	line := fmt.Sprintf("fetched %d, skipped %d, processed %d, bounces %d, groups %d",
		fetched.Fetched, fetched.Skipped, grouped.Processed, grouped.Bounces, grouped.Groups)
	progress(line)
	// Queue the analysis when any actionable group is flagged: groups that
	// gained messages in this run and groups whose last analysis failed.
	pending, err := m.countGroupsNeedingAnalysis(ctx, mb.Address)
	if err != nil {
		progress(fmt.Sprintf("error: %v", err))
		return line, fmt.Errorf("%s: %w", mb.Address, err)
	}
	if pending > 0 {
		progress(fmt.Sprintf("%d group(s) need analysis", pending))
		m.queueAnalysis(job, mb, progress)
	}
	return line, nil
}

// countGroupsNeedingAnalysis returns how many actionable open groups of the
// address are flagged needs_analysis.
func (m *JobManager) countGroupsNeedingAnalysis(ctx context.Context, address string) (int, error) {
	idx, err := mailengine.OpenIndex(ctx, m.mailsRoot, address, nil)
	if err != nil {
		return 0, err
	}
	defer idx.Close()
	groups, err := models.ListGroupsNeedingAnalysis(idx)
	if err != nil {
		return 0, err
	}
	return len(groups), nil
}

// runReindex rebuilds the index from the raw files, then queues the analysis
// (reports of groups whose key survived are carried over, so only groups
// flagged needs_analysis are analyzed).
func (m *JobManager) runReindex(ctx context.Context, job *models.Job, mb *models.Mailbox, progress func(string)) (string, error) {
	progress("reindexing")
	res, err := mailengine.Reindex(ctx, m.mailsRoot, mb.Address, mailengine.Progress(progress))
	if err != nil {
		progress(fmt.Sprintf("error: %v", err))
		return "", fmt.Errorf("%s: %w", mb.Address, err)
	}
	line := fmt.Sprintf("messages %d, bounces %d, groups %d", res.Messages, res.Bounces, res.Groups)
	if res.Skipped > 0 {
		line += fmt.Sprintf(", skipped %d", res.Skipped)
	}
	progress(line)
	m.queueAnalysis(job, mb, progress)
	return line, nil
}

// runAnalyze runs the agent over the groups named by the target: "" = the
// actionable groups flagged for analysis, "*" = every actionable group, or
// one group key. The run directories are left alone here; the daily cleanup
// job removes the ones older than agent_keep_days (runCleanup).
func (m *JobManager) runAnalyze(ctx context.Context, job *models.Job, mb *models.Mailbox, progress func(string)) (string, error) {
	provider := ResolveAgentProvider(m.db)
	if !agent.IsValidProvider(provider) {
		return "", fmt.Errorf("agent provider %q is not registered", provider)
	}
	if !agent.ProviderAvailable(provider) {
		return "", fmt.Errorf("agent provider %q is not available on this machine", provider)
	}
	idx, err := mailengine.OpenIndex(ctx, m.mailsRoot, mb.Address, mailengine.Progress(progress))
	if err != nil {
		return "", fmt.Errorf("failed to open the index of %s: %w", mb.Address, err)
	}
	defer idx.Close()

	var groups []*models.BounceGroup
	switch job.Target {
	case "":
		groups, err = models.ListGroupsNeedingAnalysis(idx)
	case analyzeAllTarget:
		actionable := true
		groups, err = models.ListGroups(idx, models.GroupFilter{Actionable: &actionable})
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
	progress(fmt.Sprintf("analyzing %d group(s) with %s", len(groups), provider))
	done, failed := 0, 0
	var failures []string
	for i, g := range groups {
		if ctx.Err() != nil {
			return fmt.Sprintf("analyzed %d, failed %d", done, failed), errors.New("server shutting down")
		}
		progress(fmt.Sprintf("(%d/%d) %s", i+1, len(groups), g.Label()))
		report, err := agent.AnalyzeGroup(ctx, agent.AnalyzeInput{
			MailsRoot: m.mailsRoot, AgentRoot: m.agentRoot, TemplatesFS: m.templates,
			Address: mb.Address, Index: idx, GroupKey: g.GroupKey, Provider: provider, Language: "ja",
		}, progress)
		switch {
		case err != nil:
			failed++
			failures = append(failures, fmt.Sprintf("%s: %v", g.GroupKey, err))
			progress(fmt.Sprintf("%s: error: %v", g.GroupKey, err))
		case report != nil && report.Status == "error":
			failed++
			failures = append(failures, fmt.Sprintf("%s: %s", g.GroupKey, report.ErrorMessage))
			progress(fmt.Sprintf("%s: agent failed: %s", g.GroupKey, report.ErrorMessage))
		default:
			done++
			progress(fmt.Sprintf("%s: completed", g.GroupKey))
		}
	}
	result := fmt.Sprintf("analyzed %d, failed %d", done, failed)
	if failed > 0 {
		return result, errors.New(strings.Join(failures, "; "))
	}
	return result, nil
}

// runNotify sends the notification mail (target "") or a test mail (target
// "test:<address>"). A notification that the send conditions rule out is
// not an error: the result records the reason.
func (m *JobManager) runNotify(ctx context.Context, job *models.Job, progress func(string)) (string, error) {
	if address, ok := strings.CutPrefix(job.Target, notifyTestPrefix); ok {
		s, err := ResolveNotificationSettings(m.db, m.key)
		if err != nil {
			return "", err
		}
		progress("sending a test mail to " + address)
		if err := TestSMTP(ctx, s.SMTP, address); err != nil {
			progress(fmt.Sprintf("error: %v", err))
			return "", err
		}
		line := "sent a test mail to " + address
		progress(line)
		return line, nil
	}
	if job.Target != "" {
		return "", fmt.Errorf("invalid notify target %q", job.Target)
	}
	progress("collecting the groups that need attention")
	res, err := SendNotification(ctx, m.db, m.key, m.mailsRoot, time.Now())
	if err != nil {
		progress(fmt.Sprintf("error: %v", err))
		return "", err
	}
	if !res.Sent {
		line := "skipped: " + res.Skipped
		progress(line)
		return line, nil
	}
	line := fmt.Sprintf("sent %d group(s) to %s", res.Groups, strings.Join(res.Recipients, ", "))
	progress(line)
	return line, nil
}
