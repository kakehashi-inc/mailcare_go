package modules

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

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
	key, err := LoadSecretKey(db)
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
	// Reindex / reclassify / group / cleanup / analyze expand over every
	// mailbox (the disabled one included), and the analyze children keep
	// the target.
	for _, kind := range []string{JobKindReindex, JobKindReclassify, JobKindGroup, JobKindCleanup} {
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
			running, err := models.ListActiveJobs(db)
			if err != nil || len(running) != 1 {
				t.Fatalf("running job: %d, %v", len(running), err)
			}
			if _, _, err := jm.enqueueChild(JobKindReclassify, mb.ID, "", running[0]); err != nil {
				t.Errorf("queue follow-up: %v", err)
			}
			// A job queued by someone else meanwhile (here another CLI
			// process, same requester tag) must be left alone.
			if _, _, err := jm.Enqueue(JobKindSync, 0, "", RequestedByCLI); err != nil {
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
	if byKind[JobKindReclassify].Status != JobStatusDone || byKind[JobKindReclassify].ParentID != job.ID {
		t.Errorf("follow-up not run: %+v", byKind[JobKindReclassify])
	}
	if byKind[JobKindSync].Status != JobStatusQueued {
		t.Errorf("foreign job touched: %+v", byKind[JobKindSync])
	}
	if !strings.Contains(strings.Join(lines, "\n"), "--- job #") {
		t.Errorf("follow-up banner missing:\n%s", strings.Join(lines, "\n"))
	}
	if locks := jm.heldLocks(); len(locks) != 0 {
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
			t.Fatalf("claimed #%d (%s) although nothing should be runnable; locks %v", got.ID, got.Kind, jm.heldLocks())
		}
		if want != nil && (got == nil || got.ID != want.ID) {
			t.Fatalf("claimed %+v, want #%d (%s); locks %v", got, want.ID, want.Kind, jm.heldLocks())
		}
		return got
	}
	claim(fetchA)
	if locks := jm.heldLocks(); strings.Join(locks, ",") != "host:imap.example.test,mailbox:a@example.test" {
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
	if locks := jm.heldLocks(); strings.Join(locks, ",") != "agent,analyze:a@example.test" {
		t.Errorf("locks after analyze A: %v", locks)
	}
	claim(syncA)
	claim(nil)
	jm.releaseLocks(analyzeA.ID)
	claim(analyzeB)
	claim(nil)
	jm.releaseLocks(analyzeB.ID)
	claim(reindexC)
	if locks := jm.heldLocks(); strings.Join(locks, ",") != "agent,host:imap.example.test,mailbox:a@example.test,mailbox:c@example.test" {
		t.Errorf("locks with sync A and reindex C: %v", locks)
	}
	jm.releaseLocks(reindexC.ID)
	jm.releaseLocks(syncA.ID)
	if locks := jm.heldLocks(); len(locks) != 0 {
		t.Errorf("locks not released: %v", locks)
	}
	// Expansion jobs hold nothing.
	if keys := jm.resourceKeys(&models.Job{Kind: JobKindSync}); len(keys) != 0 {
		t.Errorf("expansion job keys: %v", keys)
	}
	// A cleanup holds the mailbox key and the agent key (like a reindex): it
	// waits for a running analysis of any address and blocks the jobs that
	// read or rewrite the mails of its own address.
	if keys := jobResourceKeys(JobKindCleanup, a); strings.Join(keys, ",") != "mailbox:a@example.test,agent" {
		t.Errorf("cleanup keys: %v", keys)
	}
	for _, j := range []*models.Job{analyzeA, analyzeB, reindexC, syncA} {
		if err := models.FinishJob(db, j.ID, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	analyzeB2, _, _ := jm.Enqueue(JobKindAnalyze, b.ID, "", "cli")
	cleanupA, _, _ := jm.Enqueue(JobKindCleanup, a.ID, "", "cli")
	claim(analyzeB2)
	claim(nil) // the cleanup waits for the agent key held by the analysis of another address
	jm.releaseLocks(analyzeB2.ID)
	claim(cleanupA)
	if locks := jm.heldLocks(); strings.Join(locks, ",") != "agent,mailbox:a@example.test" {
		t.Errorf("locks with cleanup A: %v", locks)
	}
	groupA2, _, _ := jm.Enqueue(JobKindGroup, a.ID, "", "cli")
	claim(nil) // the group job of the same address waits for the mailbox key
	jm.releaseLocks(cleanupA.ID)
	claim(groupA2)
	jm.releaseLocks(groupA2.ID)
	if locks := jm.heldLocks(); len(locks) != 0 {
		t.Errorf("locks not released: %v", locks)
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
	if locks := jm.heldLocks(); len(locks) != 0 {
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
	running, err := models.ClaimNextRunnableJob(db, nil)
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
	// Old finished jobs are the daily cleanup's business, not the start-up's.
	if _, err := models.GetJobByID(db, old.ID); err != nil {
		t.Errorf("start-up removed the old finished job: %v", err)
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

// makeAgentRun creates an agent run directory of the given age.
func makeAgentRun(t *testing.T, agentRoot, address, group, run string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(AgentWorkspaceDir(agentRoot, address), group, run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "RESULT.log"), []byte("Result: Success\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(dir, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestCleanupJobAppliesRetentions: a cleanup job removes the mails of the
// mailbox older than mail_keep_days (the emptied groups with their reports)
// and the agent run directories older than agent_keep_days, leaves the
// other mailbox alone, and reports the counts in its result line. Fetch and
// reindex jobs no longer touch either retention.
func TestCleanupJobAppliesRetentions(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples)
	other, _ := seedMailbox(t, db, key, "other@example.test", testSamples[:1])
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	// The samples are dated September 2025: with a 30 day retention every
	// one of them is expired, but a reindex job leaves them alone.
	if err := SaveMailKeepDays(db, 30); err != nil {
		t.Fatal(err)
	}
	if err := SaveAgentKeepDays(db, 2); err != nil {
		t.Fatal(err)
	}
	var lines []string
	result, err := jm.RunJob(context.Background(), mailboxJob(JobKindReindex, mb, ""), func(m string) { lines = append(lines, m) })
	if err != nil || !strings.HasPrefix(result, "messages 3, bounces 2, groups ") {
		t.Fatalf("reindex: %q, %v", result, err)
	}
	if joined := strings.Join(lines, "\n"); strings.Contains(joined, "removed") {
		t.Errorf("reindex applied a retention:\n%s", joined)
	}
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msgs, _ := models.ListAllMessages(idx); len(msgs) != 3 {
		t.Fatalf("messages after reindex: %d", len(msgs))
	}
	groups, _ := models.ListGroups(idx, models.GroupFilter{})
	if len(groups) != 2 {
		t.Fatalf("groups after reindex: %d", len(groups))
	}
	rep := &models.AgentReport{GroupKey: groups[0].GroupKey, Provider: "codex"}
	if err := models.InsertAgentReport(idx, rep); err != nil {
		t.Fatal(err)
	}
	idx.Close()
	expired := makeAgentRun(t, jm.agentRoot, mb.Address, "0123456789abcdef", "1", 3*24*time.Hour)
	recent := makeAgentRun(t, jm.agentRoot, mb.Address, "0123456789abcdef", "2", time.Hour)
	orphan := makeAgentRun(t, jm.agentRoot, mb.Address, "fedcba9876543210", "3", 10*24*time.Hour)
	foreign := makeAgentRun(t, jm.agentRoot, other.Address, "0123456789abcdef", "4", 10*24*time.Hour)
	// Two finished jobs: one older than the job retention, one recent.
	oldJob, recentJob := finishedJob(t, db, jm, mb, 40*24*time.Hour), finishedJob(t, db, jm, mb, time.Hour)
	// The leftover of an interrupted write, older than a day: removed and
	// reported, but not part of the result counts.
	staleTemp := filepath.Join(mailengine.MailboxDir(mailsRoot, mb.Address), ".20250901-000000_aaaaaaaaaaaa.eml.123.tmp")
	if err := os.WriteFile(staleTemp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-2 * mailengine.StaleTempFileAge)
	if err := os.Chtimes(staleTemp, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	lines = nil
	result, err = jm.RunJob(context.Background(), mailboxJob(JobKindCleanup, mb, ""), func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("cleanup: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if result != "removed 3 messages, 2 groups, 2 agent workspaces, 1 jobs" {
		t.Errorf("result = %q", result)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"[ops@example.test] cleaning up (mail retention 30 days, agent workspace retention 2 days, job history 30 days)",
		"[ops@example.test] removed 3 message(s) older than 30 days, 2 group(s) left empty and removed",
		"[ops@example.test] removed 1 stale temporary file(s)",
		"[ops@example.test] removed 2 expired agent workspace(s)",
		"[ops@example.test] removed 1 finished job(s) older than 30 days",
		"[ops@example.test] " + result,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress lacks %q:\n%s", want, joined)
		}
	}
	idx, err = mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := models.ListAllMessages(idx)
	groups, _ = models.ListGroups(idx, models.GroupFilter{})
	reports, _ := models.ListAgentReports(idx, rep.GroupKey)
	idx.Close()
	if len(msgs) != 0 || len(groups) != 0 || len(reports) != 0 {
		t.Errorf("index after the cleanup: %d messages, %d groups, %d reports", len(msgs), len(groups), len(reports))
	}
	if entries, err := os.ReadDir(mailengine.MailboxDir(mailsRoot, mb.Address)); err != nil || len(entries) != 0 {
		t.Errorf("mailbox directory after the cleanup: %d entries, %v", len(entries), err)
	}
	for _, gone := range []string{expired, orphan, filepath.Dir(orphan)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed: %v", gone, err)
		}
	}
	for _, kept := range []string{recent, foreign} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}
	// The other mailbox was not touched by this child: its mail is still
	// there until its own cleanup runs.
	if entries, err := os.ReadDir(mailengine.MailboxDir(mailsRoot, other.Address)); err != nil || len(entries) != 1 {
		t.Errorf("other mailbox directory: %d entries, %v", len(entries), err)
	}
	if _, err := models.GetJobByID(db, oldJob.ID); err != sql.ErrNoRows {
		t.Errorf("old finished job not deleted: %v", err)
	}
	if _, err := models.GetJobByID(db, recentJob.ID); err != nil {
		t.Errorf("recent finished job deleted: %v", err)
	}
	// Nothing left: the counts are zero and the progress stays short.
	lines = nil
	result, err = jm.RunJob(context.Background(), mailboxJob(JobKindCleanup, mb, ""), func(m string) { lines = append(lines, m) })
	if err != nil || result != "removed 0 messages, 0 groups, 0 agent workspaces, 0 jobs" || len(lines) != 2 {
		t.Errorf("second cleanup: %q, %v, lines %q", result, err, lines)
	}
	// Without any mailbox the expansion job prunes the job history itself.
	for _, m := range []*models.Mailbox{mb, other} {
		if err := models.DeleteMailbox(db, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE jobs SET finished_at = ? WHERE id = ?`, time.Now().Add(-40*24*time.Hour).UTC(), recentJob.ID); err != nil {
		t.Fatal(err)
	}
	result, err = jm.RunJob(context.Background(), &models.Job{Kind: JobKindCleanup, RequestedBy: "cli"}, nil)
	if err != nil || result != "queued 0 jobs (no mailbox), removed 1 jobs" {
		t.Errorf("cleanup expansion without mailboxes: %q, %v", result, err)
	}
	if _, err := models.GetJobByID(db, recentJob.ID); err != sql.ErrNoRows {
		t.Errorf("expansion did not prune the job history: %v", err)
	}
}

// finishedJob queues a reindex job for the mailbox, finishes it and sets its
// finish time to now - age.
func finishedJob(t *testing.T, db *sql.DB, jm *JobManager, mb *models.Mailbox, age time.Duration) *models.Job {
	t.Helper()
	job, _, err := jm.Enqueue(JobKindReindex, mb.ID, "", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := models.FinishJob(db, job.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET finished_at = ? WHERE id = ?`, time.Now().Add(-age).UTC(), job.ID); err != nil {
		t.Fatal(err)
	}
	return job
}

// TestCleanupJobReportsFailures: when the mails cannot be removed the
// cleanup still removes the expired agent workspaces, records the counts
// and fails the job; the rows stay for the next run.
func TestCleanupJobReportsFailures(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test user cannot write")
	}
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mb, mailsRoot := seedMailbox(t, db, key, "ops@example.test", testSamples[:1])
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveMailKeepDays(db, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := jm.RunJob(context.Background(), mailboxJob(JobKindReindex, mb, ""), nil); err != nil {
		t.Fatal(err)
	}
	expired := makeAgentRun(t, jm.agentRoot, mb.Address, "0123456789abcdef", "1", 40*24*time.Hour)
	dir := mailengine.MailboxDir(mailsRoot, mb.Address)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	var lines []string
	result, err := jm.RunJob(context.Background(), mailboxJob(JobKindCleanup, mb, ""), func(m string) { lines = append(lines, m) })
	if err == nil || !strings.Contains(err.Error(), "mail retention: ") {
		t.Fatalf("expected a retention error, got %v\n%s", err, strings.Join(lines, "\n"))
	}
	if result != "removed 0 messages, 0 groups, 1 agent workspaces, 0 jobs" {
		t.Errorf("result = %q", result)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Errorf("expired workspace kept after the failed mail retention: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if msgs, _ := models.ListAllMessages(idx); len(msgs) != 1 {
		t.Errorf("a failed cleanup removed the row: %d messages left", len(msgs))
	}
}

// TestKeepDaysLabel checks the wording of the retention in progress lines.
func TestKeepDaysLabel(t *testing.T) {
	for keep, want := range map[time.Duration]string{
		24 * time.Hour:      "1 day",
		2 * 24 * time.Hour:  "2 days",
		30 * 24 * time.Hour: "30 days",
		0:                   "0 days",
	} {
		if got := keepDaysLabel(keep); got != want {
			t.Errorf("keepDaysLabel(%s) = %q, want %q", keep, got, want)
		}
	}
}

func TestAgentKeepDaysSetting(t *testing.T) {
	db := newTestDB(t)
	if got := ResolveAgentKeepDays(db); got != DefaultAgentKeepDays {
		t.Errorf("default agent_keep_days = %d", got)
	}
	for _, bad := range []string{"0", "366", "-1", "x", ""} {
		if _, err := ParseAgentKeepDays(bad); err == nil {
			t.Errorf("ParseAgentKeepDays(%q) accepted", bad)
		}
	}
	if err := SaveAgentKeepDays(db, 400); err == nil {
		t.Error("SaveAgentKeepDays(400) accepted")
	}
	shown, err := ApplySetting(db, SettingAgentKeepDays, " 7 ", nil)
	if err != nil || shown != "7" || ResolveAgentKeepDays(db) != 7 {
		t.Errorf("settings set agent_keep_days 7: %q %v -> %d", shown, err, ResolveAgentKeepDays(db))
	}
	if _, err := ApplySetting(db, SettingAgentKeepDays, "0", nil); err == nil {
		t.Error("settings set agent_keep_days 0 accepted")
	}
	// The default value is not stored; a stored value out of range falls
	// back to the default.
	if _, err := ApplySetting(db, SettingAgentKeepDays, strconv.Itoa(DefaultAgentKeepDays), nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingAgentKeepDays); found {
		t.Error("default agent_keep_days must not be stored")
	}
	if err := models.SetSetting(db, SettingAgentKeepDays, "1000"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveAgentKeepDays(db); got != DefaultAgentKeepDays {
		t.Errorf("out-of-range stored value resolved to %d", got)
	}
	if _, err := ApplySetting(db, SettingAgentKeepDays, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingAgentKeepDays); found {
		t.Error("an empty value must delete the row")
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

func TestMailKeepDaysSetting(t *testing.T) {
	db := newTestDB(t)
	if got := ResolveMailKeepDays(db); got != DefaultMailKeepDays {
		t.Errorf("default mail_keep_days = %d", got)
	}
	for _, bad := range []string{"0", strconv.Itoa(MaxMailKeepDays + 1), "-1", "x", "", "1.5"} {
		if _, err := ParseMailKeepDays(bad); err == nil {
			t.Errorf("ParseMailKeepDays(%q) accepted", bad)
		}
	}
	if n, err := ParseMailKeepDays(" 365 "); err != nil || n != 365 {
		t.Errorf("ParseMailKeepDays(365): %d, %v", n, err)
	}
	for _, bad := range []int{0, MinMailKeepDays - 1, MaxMailKeepDays + 1} {
		if err := SaveMailKeepDays(db, bad); err == nil {
			t.Errorf("SaveMailKeepDays(%d) accepted", bad)
		}
	}
	shown, err := ApplySetting(db, SettingMailKeepDays, " 90 ", nil)
	if err != nil || shown != "90" || ResolveMailKeepDays(db) != 90 || models.GetSetting(db, SettingMailKeepDays) != "90" {
		t.Errorf("settings set mail_keep_days 90: %q %v -> %d (stored %q)", shown, err, ResolveMailKeepDays(db), models.GetSetting(db, SettingMailKeepDays))
	}
	if _, err := ApplySetting(db, SettingMailKeepDays, "0", nil); err == nil {
		t.Error("settings set mail_keep_days 0 accepted")
	}
	var exitErr *ExitError
	if _, err := ApplySetting(db, SettingMailKeepDays, "3651", nil); !errors.As(err, &exitErr) || exitErr.Code != ExitArgument {
		t.Errorf("out-of-range value is not an argument error: %v", err)
	}
	if ResolveMailKeepDays(db) != 90 {
		t.Errorf("a rejected value changed the setting: %d", ResolveMailKeepDays(db))
	}
	// The default value is not stored; a stored value out of range falls
	// back to the default; an empty value deletes the row.
	if _, err := ApplySetting(db, SettingMailKeepDays, strconv.Itoa(DefaultMailKeepDays), nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingMailKeepDays); found {
		t.Error("default mail_keep_days must not be stored")
	}
	if err := SaveMailKeepDays(db, MaxMailKeepDays); err != nil || ResolveMailKeepDays(db) != MaxMailKeepDays {
		t.Errorf("SaveMailKeepDays(max): %v -> %d", err, ResolveMailKeepDays(db))
	}
	if err := SaveMailKeepDays(db, DefaultMailKeepDays); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingMailKeepDays); found {
		t.Error("SaveMailKeepDays(default) left the row")
	}
	if err := models.SetSetting(db, SettingMailKeepDays, "100000"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveMailKeepDays(db); got != DefaultMailKeepDays {
		t.Errorf("out-of-range stored value resolved to %d", got)
	}
	if _, err := ApplySetting(db, SettingMailKeepDays, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := models.GetSettingStrict(db, SettingMailKeepDays); found {
		t.Error("an empty value must delete the row")
	}
	if !slices.Contains(SettingKeys(), SettingMailKeepDays) {
		t.Error("mail_keep_days missing from the settings keys")
	}
}

// startMemIMAP starts an in-memory IMAP server with one account (the
// address given) and an INBOX, and returns the host and port to reach it.
// The server is closed when the test ends.
func startMemIMAP(t *testing.T, address, password string) (user *imapmemserver.User, host string, port int) {
	t.Helper()
	memServer := imapmemserver.New()
	user = imapmemserver.NewUser(address, password)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	memServer.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memServer.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	addr := ln.Addr().(*net.TCPAddr)
	return user, addr.IP.String(), addr.Port
}

// memLiteral adapts a byte slice to imap.LiteralReader.
type memLiteral struct{ *bytes.Reader }

func (l memLiteral) Size() int64 { return int64(l.Reader.Len()) }

// dateHeaderRe matches the top-level Date header of a raw message (the
// first one in the file).
var dateHeaderRe = regexp.MustCompile(`(?m)^Date:[^\r\n]*`)

// redatedSample returns a testdata message with its top-level Date header
// set to now - age.
func redatedSample(t *testing.T, name string, age time.Duration) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("mailengine", "testdata", name))
	if err != nil {
		t.Fatalf("read sample %s: %v", name, err)
	}
	loc := dateHeaderRe.FindIndex(raw)
	if loc == nil {
		t.Fatalf("sample %s has no Date header", name)
	}
	out := append([]byte{}, raw[:loc[0]]...)
	out = append(out, []byte("Date: "+time.Now().Add(-age).Format(time.RFC1123Z))...)
	return append(out, raw[loc[1]:]...)
}

// TestFetchJobKeepsExpiredMail: a fetch job never searches before the mail
// retention, stores what it downloads whatever the Date header says, and
// only the cleanup job applies the retention to the stored mail.
func TestFetchJobKeepsExpiredMail(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	const address = "ops@example.test"
	user, host, port := startMemIMAP(t, address, "secret")
	mb, err := CreateMailbox(db, key, &MailboxInput{Address: address, ImapHost: host, ImapPort: port,
		ImapSecurity: IMAPSecurityNone, ImapUsername: address, ImapPassword: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveMailKeepDays(db, 30); err != nil {
		t.Fatal(err)
	}
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	const day = 24 * time.Hour
	// Received 45 days ago: outside the retention, not searched for at all.
	// Received 2 days ago: fetched. Dated 60 days ago but received an hour
	// ago: fetched (the search goes by the arrival date) and kept by the
	// fetch, removed by the cleanup.
	for _, s := range []struct {
		name     string
		dated    time.Duration
		received time.Duration
	}{{"postfix_dsn.eml", 45 * day, 45 * day}, {"exim_bounce.eml", 2 * day, 2 * day}, {"normal.eml", 60 * day, time.Hour}} {
		raw := redatedSample(t, s.name, s.dated)
		if _, err := user.Append("INBOX", memLiteral{bytes.NewReader(raw)}, &imap.AppendOptions{Time: time.Now().Add(-s.received)}); err != nil {
			t.Fatalf("append %s: %v", s.name, err)
		}
	}
	var lines []string
	result, err := jm.RunJob(context.Background(), mailboxJob(JobKindFetch, mb, ""), func(m string) { lines = append(lines, m) })
	if err != nil || result != "fetched 2, skipped 0" {
		t.Fatalf("fetch: %q, %v\n%s", result, err, strings.Join(lines, "\n"))
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "removed") {
		t.Errorf("fetch applied the retention:\n%s", joined)
	}
	wantSince := "searching since " + time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02") + " (initial window, 90 days, limited to the mail retention)"
	if !strings.Contains(joined, wantSince) {
		t.Errorf("progress lacks %q:\n%s", wantSince, joined)
	}
	mailsRoot, _ := MailsDir()
	idx, err := mailengine.OpenIndex(context.Background(), mailsRoot, mb.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if msgs, err := models.ListAllMessages(idx); err != nil || len(msgs) != 2 {
		t.Fatalf("messages after fetch: %d, %v", len(msgs), err)
	}
	// The cleanup removes the expired one and keeps the recent one.
	result, err = jm.RunJob(context.Background(), mailboxJob(JobKindCleanup, mb, ""), nil)
	if err != nil || result != "removed 1 messages, 0 groups, 0 agent workspaces, 0 jobs" {
		t.Errorf("cleanup: %q, %v", result, err)
	}
	msgs, err := models.ListAllMessages(idx)
	if err != nil || len(msgs) != 1 || !strings.Contains(msgs[0].Subject, "Mail delivery failed") {
		t.Fatalf("messages after cleanup: %d, %v", len(msgs), err)
	}
	if _, err := os.Stat(mailengine.MessageFilePath(mailsRoot, mb.Address, msgs[0].MessageKey, "eml")); err != nil {
		t.Errorf("raw file of the kept message: %v", err)
	}
}

// TestJobsOfDeletedMailboxAreLabeled checks that the job history tells the
// jobs of a deleted mailbox (mailbox_id NULL like an expansion job's) apart
// from the expansion jobs: a child of an expansion and a queued job
// canceled by the deletion are shown as "(deleted mailbox)", the expansion
// job itself and a notify job as "(all)".
func TestJobsOfDeletedMailboxAreLabeled(t *testing.T) {
	db := newTestDB(t)
	jm, key := newTestJobManager(t, db)
	mailsRoot, agentRoot, err := dataRootsForCLI()
	if err != nil {
		t.Fatal(err)
	}
	mb := unreachableMailbox(t, db, key, "gone@example.test", "127.0.0.1", closedPort(t))
	expansion, _, err := jm.Enqueue(JobKindReindex, 0, "", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := jm.RunJob(context.Background(), expansion, nil); err != nil || result != "queued 1 jobs" {
		t.Fatalf("expansion: %q, %v", result, err)
	}
	if err := models.FinishJob(db, expansion.ID, "queued 1 jobs", ""); err != nil {
		t.Fatal(err)
	}
	direct := queuedJob(t, db, JobKindSync, mb)
	notify, _, err := jm.Enqueue(JobKindNotify, 0, "", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := models.CancelQueuedJob(db, notify.ID); err != nil || !changed {
		t.Fatalf("cancel notify: %v %v", changed, err)
	}
	// The deletion cancels the queued child and the direct job and sets
	// their mailbox_id to NULL.
	if err := DeleteMailbox(db, mailsRoot, agentRoot, mb, false); err != nil {
		t.Fatal(err)
	}
	jobs, err := models.ListJobs(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]bool{expansion.ID: false, direct.ID: true, notify.ID: false}
	for _, j := range jobs {
		if j.ParentID == expansion.ID {
			want[j.ID] = true
		}
	}
	if len(want) != 4 {
		t.Fatalf("jobs after the deletion: %+v", jobs)
	}
	for _, j := range jobs {
		if j.MailboxID.Valid {
			t.Errorf("job #%d still has a mailbox: %+v", j.ID, j)
		}
		if got := JobOfDeletedMailbox(j); got != want[j.ID] {
			t.Errorf("JobOfDeletedMailbox(#%d %s %s parent %d) = %v, want %v", j.ID, j.Kind, j.Status, j.ParentID, got, want[j.ID])
		}
	}
	// A job that still has its mailbox is never reported as deleted.
	if JobOfDeletedMailbox(&models.Job{Kind: JobKindSync, MailboxID: sql.NullInt64{Int64: 1, Valid: true}, ParentID: 1}) {
		t.Error("a job with a mailbox reported as deleted")
	}

	// The CLI list shows the labels in text and JSON.
	out := captureStdout(t, func() {
		if err := (&JobsListCmd{Limit: 10}).Run(); err != nil {
			t.Errorf("jobs: %v", err)
		}
	})
	for id, deleted := range want {
		label := "(all)"
		if deleted {
			label = "(deleted mailbox)"
		}
		if !regexp.MustCompile(`(?m)^` + strconv.FormatInt(id, 10) + `\s+\S+\s+` + regexp.QuoteMeta(label) + `\s`).MatchString(out) {
			t.Errorf("job #%d not listed with %s:\n%s", id, label, out)
		}
	}
	out = captureStdout(t, func() {
		if err := (&JobsListCmd{Limit: 10, JSON: true}).Run(); err != nil {
			t.Errorf("jobs --json: %v", err)
		}
	})
	if strings.Count(out, `"mailbox_address": "(deleted mailbox)"`) != 2 || strings.Count(out, `"mailbox_address": "(all)"`) != 2 {
		t.Errorf("JSON labels:\n%s", out)
	}
}
