package modules

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// testSamples are the raw messages seeded into a mailbox directory: two
// bounces and one ordinary mail.
var testSamples = []string{"postfix_dsn.eml", "exim_bounce.eml", "normal.eml"}

// seedMailbox registers a mailbox and copies the sample .eml files into its
// raw directory under the current data directory. It returns the mailbox and
// the mails root.
func seedMailbox(t *testing.T, db *sql.DB, key []byte, address string, samples []string) (*models.Mailbox, string) {
	t.Helper()
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: address, ImapHost: "imap.example.test",
		ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	mailsRoot, err := MailsDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := mailengine.MailboxDir(mailsRoot, mb.Address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i, name := range samples {
		raw, err := os.ReadFile(filepath.Join("mailengine", "testdata", name))
		if err != nil {
			t.Fatalf("read sample %s: %v", name, err)
		}
		msgKey := mailengine.MessageKey(time.Date(2025, 9, 1+i, 0, 0, 0, 0, time.UTC), "INBOX", 1, uint32(i+1))
		if !mailengine.ValidMessageKey(msgKey) {
			t.Fatalf("invalid message key %s", msgKey)
		}
		if err := os.WriteFile(filepath.Join(dir, msgKey+".eml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return mb, mailsRoot
}

func newTestJobManager(t *testing.T, db *sql.DB) (*JobManager, []byte) {
	t.Helper()
	key, err := LoadSecretKey()
	if err != nil {
		t.Fatal(err)
	}
	mailsRoot, err := MailsDir()
	if err != nil {
		t.Fatal(err)
	}
	agentRoot, err := AgentDir()
	if err != nil {
		t.Fatal(err)
	}
	return NewJobManager(db, key, mailsRoot, agentRoot, nil), key
}

// closedPort returns a loopback port that nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// unreachableMailbox registers a mailbox whose IMAP server is a closed local
// port (every fetch fails at once, without any network).
func unreachableMailbox(t *testing.T, db *sql.DB, key []byte, address, host string, port int) *models.Mailbox {
	t.Helper()
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: address, ImapHost: host, ImapPort: port,
		ImapSecurity: IMAPSecurityNone, ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatal(err)
	}
	return mb
}

// mailboxJob builds an unsaved job of a kind for a mailbox.
func mailboxJob(kind string, mb *models.Mailbox, target string) *models.Job {
	return &models.Job{Kind: kind, MailboxID: sql.NullInt64{Int64: mb.ID, Valid: true}, Target: target, RequestedBy: "cli"}
}

// waitForIdle waits until no job is queued or running.
func waitForIdle(t *testing.T, db *sql.DB, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		active, err := models.ListActiveJobs(db)
		if err != nil {
			t.Fatal(err)
		}
		if len(active) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	active, _ := models.ListActiveJobs(db)
	t.Fatalf("jobs still active after %s: %d", timeout, len(active))
}

func TestEnqueueJobValidation(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if _, _, err := jm.Enqueue("bogus", 0, "", "t"); err == nil || !strings.Contains(err.Error(), "unknown job kind") {
		t.Errorf("unknown kind: %v", err)
	}
	if _, _, err := jm.Enqueue(JobKindAnalyze, 0, "0123456789abcdef", "t"); err == nil || !strings.Contains(err.Error(), "requires a mailbox") {
		t.Errorf("analyze of one group without mailbox: %v", err)
	}
	if _, _, err := jm.Enqueue(JobKindSync, 999, "", "t"); err == nil || !strings.Contains(err.Error(), "mailbox not found") {
		t.Errorf("unknown mailbox: %v", err)
	}
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: "a@example.test", ImapHost: "h", ImapUsername: "u", ImapPassword: "p"})
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := jm.Enqueue(JobKindSync, mb.ID, "ignored-target", "web:alice")
	if err != nil || !created {
		t.Fatalf("first enqueue: created=%v err=%v", created, err)
	}
	if first.Target != "" {
		t.Errorf("target not cleared for a sync job: %q", first.Target)
	}
	if first.Status != JobStatusQueued || first.RequestedBy != "web:alice" || first.MailboxID.Int64 != mb.ID {
		t.Errorf("unexpected job %+v", first)
	}
	dup, created, err := jm.Enqueue(JobKindSync, mb.ID, "", "cli")
	if err != nil || created || dup.ID != first.ID {
		t.Errorf("duplicate: created=%v id=%d (want %d) err=%v", created, dup.ID, first.ID, err)
	}
	// Every kind is accepted; fetch and sync of the same mailbox are distinct.
	for _, kind := range []string{JobKindFetch, JobKindGroup, JobKindReindex, JobKindReclassify} {
		if _, created, err := jm.Enqueue(kind, mb.ID, "", "cli"); err != nil || !created {
			t.Errorf("%s: created=%v err=%v", kind, created, err)
		}
	}
	// A different target of an analyze job is a different job; the same one is suppressed.
	a1, created, err := jm.Enqueue(JobKindAnalyze, mb.ID, "abc", "cli")
	if err != nil || !created {
		t.Fatalf("analyze abc: %v %v", created, err)
	}
	if _, created, _ := jm.Enqueue(JobKindAnalyze, mb.ID, "def", "cli"); !created {
		t.Errorf("analyze def should be a new job")
	}
	if again, created, _ := jm.Enqueue(JobKindAnalyze, mb.ID, "abc", "cli"); created || again.ID != a1.ID {
		t.Errorf("analyze abc duplicated")
	}
	// An all-mailbox (expansion) sync and a per-mailbox sync are distinct;
	// analyze without a mailbox is an expansion job for "" and "*".
	if _, created, err := jm.Enqueue(JobKindSync, 0, "", "scheduler"); err != nil || !created {
		t.Errorf("all-mailbox sync: %v %v", created, err)
	}
	if j, created, err := jm.Enqueue(JobKindAnalyze, 0, analyzeAllTarget, "cli"); err != nil || !created || j.MailboxID.Valid || j.Target != analyzeAllTarget {
		t.Errorf("all-mailbox analyze: %+v %v %v", j, created, err)
	}
	// Once the job finished an identical one can be queued again.
	if err := models.FinishJob(db, first.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, created, err := jm.Enqueue(JobKindSync, mb.ID, "", "cli"); err != nil || !created {
		t.Errorf("re-enqueue after finish: %v %v", created, err)
	}
}

func TestRunJobSyncRecordsConnectionError(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb := unreachableMailbox(t, db, key, "down@example.test", "127.0.0.1", closedPort(t))
	var lines []string
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := jm.RunJob(ctx, mailboxJob(JobKindSync, mb, ""), func(m string) { lines = append(lines, m) })
	if err == nil {
		t.Fatalf("expected an error, got result %q", result)
	}
	if !strings.Contains(err.Error(), "down@example.test") {
		t.Errorf("error does not name the mailbox: %v", err)
	}
	fresh, err := models.GetMailboxByID(db, mb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.LastFetchError == "" || !fresh.LastFetchedAt.Valid {
		t.Errorf("mailbox row not updated: error %q fetched %v", fresh.LastFetchError, fresh.LastFetchedAt.Valid)
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "[down@example.test] error:") {
		t.Errorf("progress lacks the error line:\n%s", joined)
	}
	// A fetch job records the error the same way.
	if _, err := jm.RunJob(ctx, mailboxJob(JobKindFetch, mb, ""), nil); err == nil || !strings.Contains(err.Error(), "down@example.test") {
		t.Errorf("fetch: %v", err)
	}
	// A disabled mailbox is skipped by an all-mailbox sync.
	if err := models.UpdateMailbox(db, &models.Mailbox{ID: mb.ID, Address: mb.Address, ImapHost: mb.ImapHost,
		ImapPort: mb.ImapPort, ImapSecurity: mb.ImapSecurity, ImapUsername: mb.ImapUsername, Folder: mb.Folder,
		Enabled: false, InitialDays: mb.InitialDays, RecentDays: mb.RecentDays}); err != nil {
		t.Fatal(err)
	}
	if result, err := jm.RunJob(ctx, &models.Job{Kind: JobKindSync}, nil); err != nil || !strings.Contains(result, "no enabled mailbox") {
		t.Errorf("all-mailbox sync with only a disabled mailbox: %q, %v", result, err)
	}
	if active, _ := models.ListActiveJobs(db); len(active) != 0 {
		t.Errorf("child queued for a disabled mailbox: %+v", active[0])
	}
}

func TestExpansionJobCreatesChildren(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	enabled := unreachableMailbox(t, db, key, "one@example.test", "127.0.0.1", closedPort(t))
	disabled := unreachableMailbox(t, db, key, "two@example.test", "127.0.0.1", closedPort(t))
	if err := models.UpdateMailbox(db, &models.Mailbox{ID: disabled.ID, Address: disabled.Address, ImapHost: disabled.ImapHost,
		ImapPort: disabled.ImapPort, ImapSecurity: disabled.ImapSecurity, ImapUsername: disabled.ImapUsername,
		Folder: disabled.Folder, Enabled: false, InitialDays: disabled.InitialDays, RecentDays: disabled.RecentDays}); err != nil {
		t.Fatal(err)
	}
	parent, created, err := jm.Enqueue(JobKindSync, 0, "", "web:alice")
	if err != nil || !created {
		t.Fatal(err)
	}
	var lines []string
	result, err := jm.RunJob(context.Background(), parent, func(m string) { lines = append(lines, m) })
	if err != nil || result != "queued 1 jobs" {
		t.Fatalf("sync expansion: %q, %v\n%s", result, err, strings.Join(lines, "\n"))
	}
	active, err := models.ListActiveJobs(db)
	if err != nil {
		t.Fatal(err)
	}
	var children []*models.Job
	for _, j := range active {
		if j.ID != parent.ID {
			children = append(children, j)
		}
	}
	if len(children) != 1 || children[0].Kind != JobKindSync || children[0].MailboxID.Int64 != enabled.ID ||
		children[0].RequestedBy != "web:alice" || children[0].Status != JobStatusQueued {
		t.Fatalf("children: %+v", children)
	}
	// Running the expansion again does not duplicate the queued child.
	if result, err := jm.RunJob(context.Background(), parent, nil); err != nil || result != "queued 0 jobs" {
		t.Errorf("second expansion: %q, %v", result, err)
	}
	// Reindex / reclassify / group / analyze expand over every mailbox, and
	// the analyze children keep the target.
	for _, kind := range []string{JobKindReindex, JobKindReclassify, JobKindGroup} {
		if result, err := jm.RunJob(context.Background(), &models.Job{Kind: kind, RequestedBy: "cli"}, nil); err != nil || result != "queued 2 jobs" {
			t.Errorf("%s expansion: %q, %v", kind, result, err)
		}
	}
	if result, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindAnalyze, Target: analyzeAllTarget, RequestedBy: "cli"}, nil); err != nil || result != "queued 2 jobs" {
		t.Errorf("analyze expansion: %q, %v", result, err)
	}
	active, _ = models.ListActiveJobs(db)
	analyzeChildren := 0
	for _, j := range active {
		if j.Kind == JobKindAnalyze && j.MailboxID.Valid {
			analyzeChildren++
			if j.Target != analyzeAllTarget || j.RequestedBy != "cli" {
				t.Errorf("analyze child %+v", j)
			}
		}
	}
	if analyzeChildren != 2 {
		t.Errorf("analyze children: %d", analyzeChildren)
	}
	// Without any mailbox the expansion says so.
	for _, mb := range []*models.Mailbox{enabled, disabled} {
		if err := models.DeleteMailbox(db, mb.ID); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindReindex}, nil); err != nil || !strings.Contains(result, "no mailbox") {
		t.Errorf("expansion without mailboxes: %q, %v", result, err)
	}
}

func TestRunJobReindexAndAnalyze(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	job := mailboxJob(JobKindReindex, mb, "")
	var lines []string
	result, err := jm.RunJob(context.Background(), job, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("reindex: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if !strings.Contains(result, "messages 3, bounces 2, groups ") {
		t.Errorf("result = %q", result)
	}
	if active, _ := models.ListActiveJobs(db); len(active) != 0 {
		t.Errorf("analyze queued although the agent is disabled: %+v", active[0])
	}
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := models.ListGroups(idx, models.GroupFilter{})
	unclassified, _ := models.CountUnclassifiedMessages(idx)
	idx.Close()
	if err != nil || len(groups) == 0 {
		t.Fatalf("groups: %d, %v", len(groups), err)
	}
	if unclassified != 0 {
		t.Errorf("reindex left %d unclassified messages", unclassified)
	}
	if !strings.Contains(result, "groups "+strconv.Itoa(len(groups))) {
		t.Errorf("result %q does not match %d groups", result, len(groups))
	}

	// Reclassify and group of the mailbox work the same way.
	result, err = jm.RunJob(context.Background(), mailboxJob(JobKindReclassify, mb, ""), nil)
	if err != nil || !strings.Contains(result, "bounces 2, groups "+strconv.Itoa(len(groups))) {
		t.Errorf("reclassify: %q, %v", result, err)
	}
	result, err = jm.RunJob(context.Background(), mailboxJob(JobKindGroup, mb, ""), nil)
	if err != nil || !strings.HasPrefix(result, "processed 0, ") {
		t.Errorf("group with nothing to do: %q, %v", result, err)
	}

	// Analyze with an unregistered provider fails clearly, even for an
	// explicit request, and touches no report.
	if err := models.SetSetting(db, SettingAgentProvider, "no-such-provider"); err != nil {
		t.Fatal(err)
	}
	analyze := mailboxJob(JobKindAnalyze, mb, analyzeAllTarget)
	if _, err := jm.RunJob(context.Background(), analyze, nil); err == nil || !strings.Contains(err.Error(), `agent provider "no-such-provider" is not registered`) {
		t.Errorf("analyze with an unknown provider: %v", err)
	}
	// With the agent enabled but the provider unknown, a rebuild queues no
	// analysis and says why.
	if err := SetAgentEnabled(db, true); err != nil {
		t.Fatal(err)
	}
	lines = nil
	if _, err := jm.RunJob(context.Background(), job, func(m string) { lines = append(lines, m) }); err != nil {
		t.Fatal(err)
	}
	if active, _ := models.ListActiveJobs(db); len(active) != 0 {
		t.Errorf("analyze queued with an unavailable provider")
	}
	if !strings.Contains(strings.Join(lines, "\n"), "not available; analysis skipped") {
		t.Errorf("no skip notice:\n%s", strings.Join(lines, "\n"))
	}
	// Unknown kinds and unknown mailboxes are rejected by the dispatcher.
	if _, err := jm.RunJob(context.Background(), &models.Job{Kind: "bogus"}, nil); err == nil {
		t.Errorf("unknown kind accepted")
	}
	if _, err := jm.RunJob(context.Background(), &models.Job{Kind: JobKindGroup, MailboxID: sql.NullInt64{Int64: 999, Valid: true}}, nil); err == nil || !strings.Contains(err.Error(), "mailbox not found") {
		t.Errorf("unknown mailbox: %v", err)
	}
}

func TestRunJobInlineRunsFollowUps(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, _ := seedMailbox(t, db, key, "ops@example.test", testSamples)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	// While the first job runs, a follow-up job by the same requester is
	// queued (as the sync / group / rebuild jobs do for analysis); the inline
	// runner must execute it before returning.
	var once sync.Once
	var lines []string
	echo := func(m string) {
		lines = append(lines, m)
		once.Do(func() {
			if _, _, err := jm.Enqueue(JobKindReclassify, mb.ID, "", RequestedByCLI); err != nil {
				t.Errorf("queue follow-up: %v", err)
			}
			// A job of another requester must be left alone.
			if _, _, err := jm.Enqueue(JobKindSync, 0, "", "web:someone"); err != nil {
				t.Errorf("queue foreign job: %v", err)
			}
		})
	}
	job, err := jm.RunJobInline(context.Background(), JobKindReindex, mb.ID, "", RequestedByCLI, echo)
	if err != nil {
		t.Fatalf("inline: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if job.Status != JobStatusDone || !strings.Contains(job.Result, "messages 3") || job.Progress == "" {
		t.Errorf("first job %+v", job)
	}
	all, err := models.ListJobs(db, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("jobs: %d, %v", len(all), err)
	}
	byKind := map[string]*models.Job{}
	for _, j := range all {
		byKind[j.Kind] = j
	}
	if byKind[JobKindReclassify].Status != JobStatusDone {
		t.Errorf("follow-up not run: %+v", byKind[JobKindReclassify])
	}
	if byKind[JobKindSync].Status != JobStatusQueued {
		t.Errorf("foreign job touched: %+v", byKind[JobKindSync])
	}
	if !strings.Contains(strings.Join(lines, "\n"), "--- job #") {
		t.Errorf("follow-up banner missing:\n%s", strings.Join(lines, "\n"))
	}
	if locks := jm.HeldLocks(); len(locks) != 0 {
		t.Errorf("locks left behind: %v", locks)
	}
	// An identical queued job is reported instead of run twice.
	if _, err := jm.RunJobInline(context.Background(), JobKindSync, 0, "", RequestedByCLI, nil); err == nil || !strings.Contains(err.Error(), "already queued") {
		t.Errorf("duplicate inline: %v", err)
	}
	// An inline expansion job runs its children in the same call.
	lines = nil
	parent, err := jm.RunJobInline(context.Background(), JobKindGroup, 0, "", RequestedByCLI, func(m string) { lines = append(lines, m) })
	if err != nil || parent.Status != JobStatusDone || parent.Result != "queued 1 jobs" {
		t.Fatalf("inline expansion: %+v, %v", parent, err)
	}
	all, _ = models.ListJobs(db, 10)
	childDone := false
	for _, j := range all {
		if j.Kind == JobKindGroup && j.MailboxID.Valid && j.ID > parent.ID && j.Status == JobStatusDone {
			childDone = true
		}
	}
	if !childDone {
		t.Errorf("child of the inline expansion not run:\n%s", strings.Join(lines, "\n"))
	}
}

func TestResourceLocksSerializeJobs(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	port := closedPort(t)
	a := unreachableMailbox(t, db, key, "a@example.test", "IMAP.Example.Test", port)
	b := unreachableMailbox(t, db, key, "b@example.test", "imap.example.test", port)
	c := unreachableMailbox(t, db, key, "c@example.test", "other.example.test", port)

	// Fetch on the same IMAP host (case-insensitive) waits; a group job on
	// another mailbox runs alongside.
	fetchA, _, _ := jm.Enqueue(JobKindFetch, a.ID, "", "cli")
	fetchB, _, _ := jm.Enqueue(JobKindFetch, b.ID, "", "cli")
	groupC, _, _ := jm.Enqueue(JobKindGroup, c.ID, "", "cli")
	claim := func(want *models.Job) *models.Job {
		t.Helper()
		got, err := jm.claimRunnable(nil)
		if err != nil {
			t.Fatal(err)
		}
		if want == nil && got != nil {
			t.Fatalf("claimed #%d (%s) although nothing should be runnable; locks %v", got.ID, got.Kind, jm.HeldLocks())
		}
		if want != nil && (got == nil || got.ID != want.ID) {
			t.Fatalf("claimed %+v, want #%d (%s); locks %v", got, want.ID, want.Kind, jm.HeldLocks())
		}
		return got
	}
	claim(fetchA)
	if locks := jm.HeldLocks(); strings.Join(locks, ",") != "host:imap.example.test,mailbox:a@example.test" {
		t.Errorf("locks after fetch A: %v", locks)
	}
	claim(groupC)
	claim(nil)
	jm.releaseLocks(fetchA.ID)
	claim(fetchB)
	jm.releaseLocks(fetchB.ID)
	jm.releaseLocks(groupC.ID)
	claim(nil)
	for _, j := range []*models.Job{fetchA, fetchB, groupC} {
		if err := models.FinishJob(db, j.ID, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	// The agent key serializes analyze jobs of different mailboxes and keeps
	// a reindex from running while an analysis does; a sync of the analyzed
	// mailbox is not blocked by it.
	analyzeA, _, _ := jm.Enqueue(JobKindAnalyze, a.ID, "", "cli")
	analyzeB, _, _ := jm.Enqueue(JobKindAnalyze, b.ID, "", "cli")
	reindexC, _, _ := jm.Enqueue(JobKindReindex, c.ID, "", "cli")
	syncA, _, _ := jm.Enqueue(JobKindSync, a.ID, "", "cli")
	claim(analyzeA)
	if locks := jm.HeldLocks(); strings.Join(locks, ",") != "agent,analyze:a@example.test" {
		t.Errorf("locks after analyze A: %v", locks)
	}
	claim(syncA)
	claim(nil)
	jm.releaseLocks(analyzeA.ID)
	claim(analyzeB)
	claim(nil)
	jm.releaseLocks(analyzeB.ID)
	claim(reindexC)
	if locks := jm.HeldLocks(); strings.Join(locks, ",") != "agent,host:imap.example.test,mailbox:a@example.test,mailbox:c@example.test" {
		t.Errorf("locks with sync A and reindex C: %v", locks)
	}
	jm.releaseLocks(reindexC.ID)
	jm.releaseLocks(syncA.ID)
	if locks := jm.HeldLocks(); len(locks) != 0 {
		t.Errorf("locks not released: %v", locks)
	}
	// Expansion jobs hold nothing.
	if keys := jm.resourceKeys(&models.Job{Kind: JobKindSync}); len(keys) != 0 {
		t.Errorf("expansion job keys: %v", keys)
	}
}

func TestWorkerPoolNeverOverlapsSameHost(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	port := closedPort(t)
	a := unreachableMailbox(t, db, key, "a@example.test", "127.0.0.1", port)
	b := unreachableMailbox(t, db, key, "b@example.test", "127.0.0.1", port)
	jm.SetWorkers(4)
	fetchA, _, _ := jm.Enqueue(JobKindFetch, a.ID, "", "cli")
	fetchB, _, _ := jm.Enqueue(JobKindFetch, b.ID, "", "cli")
	jm.Start()
	defer jm.Stop()
	waitForIdle(t, db, 30*time.Second)
	first, _ := models.GetJobByID(db, fetchA.ID)
	second, _ := models.GetJobByID(db, fetchB.ID)
	for _, j := range []*models.Job{first, second} {
		if j.Status != JobStatusError || !j.StartedAt.Valid || !j.FinishedAt.Valid {
			t.Fatalf("job %+v should have failed against the closed port", j)
		}
	}
	if second.StartedAt.Time.Before(first.FinishedAt.Time) {
		t.Errorf("fetch B started at %v before fetch A finished at %v (same host)", second.StartedAt.Time, first.FinishedAt.Time)
	}
	if locks := jm.HeldLocks(); len(locks) != 0 {
		t.Errorf("locks not released: %v", locks)
	}
}

func TestWorkersSetting(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	if jm.Workers() != DefaultWorkers || ResolveWorkers(db) != DefaultWorkers {
		t.Errorf("default workers: %d / %d", jm.Workers(), ResolveWorkers(db))
	}
	if err := SaveWorkers(db, 3); err != nil {
		t.Fatal(err)
	}
	if ResolveWorkers(db) != 3 || models.GetSetting(db, SettingWorkers) != "3" {
		t.Errorf("saved workers: %d %q", ResolveWorkers(db), models.GetSetting(db, SettingWorkers))
	}
	if fresh, _ := newTestJobManager(t, db); fresh.Workers() != 3 {
		t.Errorf("new manager workers: %d", fresh.Workers())
	}
	if err := SaveWorkers(db, DefaultWorkers); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingWorkers); found {
		t.Errorf("default workers still stored")
	}
	jm.SetWorkers(99)
	if jm.Workers() != MaxWorkers {
		t.Errorf("clamped workers: %d", jm.Workers())
	}
	jm.SetWorkers(0)
	if jm.Workers() != DefaultWorkers {
		t.Errorf("zero workers: %d", jm.Workers())
	}
	for _, bad := range []string{"0", "17", "x", ""} {
		if _, err := ParseWorkers(bad); err == nil {
			t.Errorf("ParseWorkers(%q): expected an error", bad)
		}
	}
	if n, err := ParseWorkers(" 8 "); err != nil || n != 8 {
		t.Errorf("ParseWorkers(8): %d, %v", n, err)
	}

	// With one worker two independent jobs run one after the other.
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	a, _ := seedMailbox(t, db, key, "a@example.test", testSamples[:1])
	b, _ := seedMailbox(t, db, key, "b@example.test", testSamples[:1])
	jm.SetWorkers(1)
	groupA, _, _ := jm.Enqueue(JobKindGroup, a.ID, "", "cli")
	groupB, _, _ := jm.Enqueue(JobKindGroup, b.ID, "", "cli")
	jm.Start()
	defer jm.Stop()
	waitForIdle(t, db, 30*time.Second)
	first, _ := models.GetJobByID(db, groupA.ID)
	second, _ := models.GetJobByID(db, groupB.ID)
	if first.Status != JobStatusDone || second.Status != JobStatusDone {
		t.Fatalf("group jobs: %+v / %+v", first, second)
	}
	if second.StartedAt.Time.Before(first.FinishedAt.Time) {
		t.Errorf("with one worker job B started at %v before job A finished at %v", second.StartedAt.Time, first.FinishedAt.Time)
	}
	// Raising the count while running is picked up by the dispatcher.
	jm.SetWorkers(2)
	c, _ := seedMailbox(t, db, key, "c@example.test", testSamples[:1])
	if _, _, err := jm.Enqueue(JobKindGroup, c.ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	waitForIdle(t, db, 30*time.Second)
	if jm.Workers() != 2 {
		t.Errorf("workers after SetWorkers(2): %d", jm.Workers())
	}
}

func TestResetStaleJobs(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples[:1])
	if _, _, err := jm.Enqueue(JobKindSync, mb.ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	running, err := models.ClaimNextJob(db)
	if err != nil || running == nil {
		t.Fatalf("claim: %v", err)
	}
	old, _, err := jm.Enqueue(JobKindReindex, mb.ID, "", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := models.FinishJob(db, old.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET finished_at = ? WHERE id = ?`, time.Now().Add(-40*24*time.Hour).UTC(), old.ID); err != nil {
		t.Fatal(err)
	}
	// A running agent report in the index is reset too.
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := models.UpsertGroup(idx, &models.BounceGroup{GroupKey: "0123456789abcdef", Category: CategoryIPBlocked,
		UnitValue: "203.0.113.5", Responsible: ResponsibleSender, State: GroupStateOpen}); err != nil {
		t.Fatal(err)
	}
	rep := &models.AgentReport{GroupKey: "0123456789abcdef", Provider: "codex"}
	if err := models.InsertAgentReport(idx, rep); err != nil {
		t.Fatal(err)
	}
	idx.Close()

	ResetStaleJobs(db, mailsRoot)

	j, err := models.GetJobByID(db, running.ID)
	if err != nil || j.Status != JobStatusError || !strings.Contains(j.ErrorMessage, "restart") || !j.FinishedAt.Valid {
		t.Errorf("running job not reset: %+v, %v", j, err)
	}
	if _, err := models.GetJobByID(db, old.ID); err != sql.ErrNoRows {
		t.Errorf("old finished job not deleted: %v", err)
	}
	idx, err = mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	latest, err := models.LatestAgentReport(idx, "0123456789abcdef")
	if err != nil || latest.Status != "error" {
		t.Errorf("running report not reset: %+v, %v", latest, err)
	}
}

func TestNewProgressLines(t *testing.T) {
	if got := newProgressLines("", "a\nb"); len(got) != 2 {
		t.Errorf("first read: %v", got)
	}
	if got := newProgressLines("a\nb", "a\nb\nc\nd"); len(got) != 2 || got[0] != "c" {
		t.Errorf("appended: %v", got)
	}
	if got := newProgressLines("a\nb", "b\nc"); len(got) != 1 || got[0] != "c" {
		t.Errorf("scrolled: %v", got)
	}
	if got := newProgressLines("a\nb", "a\nb"); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}
	if got := newProgressLines("x", ""); got != nil {
		t.Errorf("empty: %v", got)
	}
}
