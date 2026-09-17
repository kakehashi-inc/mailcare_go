package modules

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"mailcare/app/models"
)

// --- job-running commands (sync / fetch / group / reindex / reclassify / analyze / jobs) ---

// jobSpec is one job to run or submit.
type jobSpec struct {
	kind      string
	mailboxID int64
	address   string // for display; "" = every mailbox (expansion job)
	target    string
}

func (s jobSpec) label() string {
	label := s.kind
	if s.address != "" {
		label += " " + s.address
	}
	if s.target != "" {
		label += " " + s.target
	}
	return label
}

// echoProgress prints a progress line to stdout.
func echoProgress(line string) {
	fmt.Println("  " + line)
}

// runJobs runs the specs: through the running server when one answers on
// the control port (waiting for completion when wait is true), otherwise in
// this process. It returns a non-nil error when any job failed.
func runJobs(db *sql.DB, specs []jobSpec, wait bool) error {
	port := ResolveWebPort(db, 0)
	ctx, cancel := signalContext()
	defer cancel()
	failed := 0
	if IsServerRunning(port) {
		fmt.Printf("Server is running on port %d; submitting to it.\n", port)
		for _, spec := range specs {
			job, created, err := SubmitJobToServer(port, JobRequest{Kind: spec.kind, MailboxID: spec.mailboxID, Target: spec.target})
			if err != nil {
				return NewExitError(ExitExec, err.Error())
			}
			if created {
				fmt.Printf("Queued job #%d (%s)\n", job.ID, spec.label())
			} else {
				fmt.Printf("Job #%d (%s) is already %s\n", job.ID, spec.label(), job.Status)
			}
			if !wait {
				continue
			}
			n, err := waitForJobTree(ctx, db, port, job.ID)
			if err != nil {
				if ctx.Err() != nil {
					fmt.Println("Stopped waiting; the jobs keep running on the server.")
					return NewExitError(ExitGeneral, "interrupted")
				}
				return NewExitError(ExitExec, err.Error())
			}
			failed += n
		}
	} else {
		jm, err := newCLIJobManager(db)
		if err != nil {
			return err
		}
		for _, spec := range specs {
			fmt.Printf("Running %s in this process...\n", spec.label())
			job, err := jm.RunJobInline(ctx, spec.kind, spec.mailboxID, spec.target, RequestedByCLI, echoProgress)
			if err != nil && job == nil {
				return NewExitError(ExitArgument, err.Error())
			}
			if err != nil && ctx.Err() != nil {
				return NewExitError(ExitGeneral, "interrupted")
			}
			if err != nil && job.Status == JobStatusQueued {
				return NewExitError(ExitArgument, err.Error())
			}
			if !printJobOutcome(job.ID, job.Status, job.Result, job.ErrorMessage) {
				failed++
			}
			failed += reportFollowUps(db, job.ID)
		}
	}
	if failed > 0 {
		return NewExitErrorf(ExitExec, "%d job(s) failed", failed)
	}
	return nil
}

// waitForJobTree waits for a job submitted to the server and then for the
// jobs it caused (the children of an expansion job and the follow-up
// analysis, recognizable by the CLI requester tag and a later id), printing
// the progress and outcome of each. It returns the number of failed jobs.
func waitForJobTree(ctx context.Context, db *sql.DB, port int, id int64) (int, error) {
	failed := 0
	final, err := WaitForJob(ctx, port, id, echoProgress)
	if err != nil {
		return failed, err
	}
	if !printJobOutcome(final.ID, final.Status, final.Result, final.ErrorMessage) {
		failed++
	}
	waited := map[int64]bool{id: true}
	for {
		next, err := nextFollowUp(db, id, waited)
		if err != nil {
			return failed, err
		}
		if next == nil {
			return failed, nil
		}
		waited[next.ID] = true
		fmt.Printf("--- job #%d (%s)\n", next.ID, jobLabel(db, next))
		final, err := WaitForJob(ctx, port, next.ID, echoProgress)
		if err != nil {
			return failed, err
		}
		if !printJobOutcome(final.ID, final.Status, final.Result, final.ErrorMessage) {
			failed++
		}
	}
}

// nextFollowUp returns the oldest job queued by the CLI after job afterID
// that was not waited for yet (nil when there is none).
func nextFollowUp(db *sql.DB, afterID int64, waited map[int64]bool) (*models.Job, error) {
	jobs, err := models.ListJobs(db, 500)
	if err != nil {
		return nil, err
	}
	var next *models.Job
	for _, j := range jobs {
		if j.ID <= afterID || j.RequestedBy != RequestedByCLI || waited[j.ID] {
			continue
		}
		if next == nil || j.ID < next.ID {
			next = j
		}
	}
	return next, nil
}

// reportFollowUps prints the outcome of the jobs an inline run executed
// after the first one (children and follow-ups; their progress was echoed
// while they ran) and returns how many failed.
func reportFollowUps(db *sql.DB, firstID int64) int {
	jobs, err := models.ListJobs(db, 500)
	if err != nil {
		return 0
	}
	failed := 0
	for i := len(jobs) - 1; i >= 0; i-- {
		j := jobs[i]
		if j.ID <= firstID || j.RequestedBy != RequestedByCLI || j.Status == JobStatusQueued {
			continue
		}
		if !printJobOutcome(j.ID, j.Status, j.Result, j.ErrorMessage) {
			failed++
		}
	}
	return failed
}

// jobLabel renders "kind address target" for a job.
func jobLabel(db *sql.DB, j *models.Job) string {
	label := j.Kind
	if j.MailboxID.Valid {
		if mb, err := models.GetMailboxByID(db, j.MailboxID.Int64); err == nil {
			label += " " + mb.Address
		}
	}
	if j.Target != "" {
		label += " " + j.Target
	}
	return label
}

// printJobOutcome prints the final state of a job and reports success.
func printJobOutcome(id int64, status, result, errMsg string) bool {
	switch status {
	case JobStatusDone:
		if result == "" {
			result = "done"
		}
		fmt.Printf("Job #%d done: %s\n", id, result)
		return true
	case JobStatusCanceled:
		fmt.Printf("Job #%d was canceled\n", id)
		return false
	default:
		if result != "" {
			fmt.Printf("Job #%d %s: %s\n", id, status, result)
		} else {
			fmt.Printf("Job #%d %s\n", id, status)
		}
		if errMsg != "" {
			fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
		}
		return false
	}
}

// specsForAddresses builds one spec per address, or a single expansion spec
// (every mailbox) when no address is given.
func specsForAddresses(db *sql.DB, kind string, addresses []string) ([]jobSpec, error) {
	if len(addresses) == 0 {
		return []jobSpec{{kind: kind}}, nil
	}
	var specs []jobSpec
	for _, a := range addresses {
		mb, err := findMailbox(db, a)
		if err != nil {
			return nil, err
		}
		specs = append(specs, jobSpec{kind: kind, mailboxID: mb.ID, address: mb.Address})
	}
	return specs, nil
}

// runKindForAddresses opens the database and runs kind for the addresses.
func runKindForAddresses(kind string, addresses []string, wait bool) error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	specs, err := specsForAddresses(db, kind, addresses)
	if err != nil {
		return err
	}
	return runJobs(db, specs, wait)
}

// SyncCmd fetches new mail, groups the bounces and queues the analysis.
type SyncCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every enabled address)"`
	Wait      bool     `help:"When submitted to a running server, wait for completion and show progress"`
}

func (c *SyncCmd) Run() error {
	return runKindForAddresses(JobKindSync, c.Addresses, c.Wait)
}

// FetchCmd downloads new mail without grouping it.
type FetchCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every enabled address)"`
}

func (c *FetchCmd) Run() error {
	return runKindForAddresses(JobKindFetch, c.Addresses, true)
}

// GroupCmd runs the grouping phase: classifies and groups the mail not
// grouped yet, then queues the analysis. The detail of one group is shown by
// "groups ADDRESS KEY" (cli_groups.go).
type GroupCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every address)"`
}

func (c *GroupCmd) Run() error {
	return runKindForAddresses(JobKindGroup, c.Addresses, true)
}

// ReindexCmd rebuilds the index from the raw files.
type ReindexCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every address)"`
}

func (c *ReindexCmd) Run() error {
	return runKindForAddresses(JobKindReindex, c.Addresses, true)
}

// ReclassifyCmd re-runs bounce detection and grouping over every mail.
type ReclassifyCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every address)"`
}

func (c *ReclassifyCmd) Run() error {
	return runKindForAddresses(JobKindReclassify, c.Addresses, true)
}

// AnalyzeCmd runs the agent over bounce groups.
type AnalyzeCmd struct {
	Address string `arg:"" optional:"" help:"Mail address (default: every address)"`
	Group   string `help:"Analyze only this group key"`
	All     bool   `help:"Analyze every actionable group, not only those flagged for analysis"`
}

func (c *AnalyzeCmd) Run() error {
	if c.Group != "" && c.All {
		return NewExitError(ExitArgument, "--group and --all are mutually exclusive")
	}
	if c.Group != "" && c.Address == "" {
		return NewExitError(ExitArgument, "--group requires a mail address")
	}
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	target := ""
	if c.All {
		target = analyzeAllTarget
	} else if c.Group != "" {
		target = strings.TrimSpace(c.Group)
	}
	spec := jobSpec{kind: JobKindAnalyze, target: target}
	if c.Address != "" {
		mb, err := findMailbox(db, c.Address)
		if err != nil {
			return err
		}
		spec.mailboxID, spec.address = mb.ID, mb.Address
	}
	return runJobs(db, []jobSpec{spec}, true)
}

// JobsCmd lists the job history.
type JobsCmd struct {
	Limit int  `help:"Number of jobs to show" default:"20"`
	JSON  bool `help:"Output as JSON"`
}

func (c *JobsCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	jobs, err := models.ListJobs(db, c.Limit)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	addresses := map[int64]string{}
	for _, mb := range mailboxes {
		addresses[mb.ID] = mb.Address
	}
	addressOf := func(j *models.Job) string {
		if !j.MailboxID.Valid {
			return "(all)"
		}
		if a, ok := addresses[j.MailboxID.Int64]; ok {
			return a
		}
		return fmt.Sprintf("#%d (deleted)", j.MailboxID.Int64)
	}
	if c.JSON {
		out := make([]map[string]interface{}, 0, len(jobs))
		for _, j := range jobs {
			var mailboxID interface{}
			if j.MailboxID.Valid {
				mailboxID = j.MailboxID.Int64
			}
			out = append(out, map[string]interface{}{
				"id": j.ID, "kind": j.Kind, "mailbox_id": mailboxID, "mailbox_address": addressOf(j), "target": j.Target,
				"status": j.Status, "progress": j.Progress, "result": j.Result, "error_message": j.ErrorMessage,
				"requested_by": j.RequestedBy, "created_at": j.CreatedAt.UTC().Format(time.RFC3339),
				"started_at": rfc3339OrNull(j.StartedAt), "finished_at": rfc3339OrNull(j.FinishedAt),
			})
		}
		printJSON(out)
		return nil
	}
	if len(jobs) == 0 {
		fmt.Println("No jobs.")
		return nil
	}
	fmt.Printf("%-6s %-10s %-32s %-9s %-20s %s\n", "ID", "KIND", "MAILBOX", "STATUS", "CREATED", "RESULT / ERROR")
	for _, j := range jobs {
		outcome := j.Result
		if j.ErrorMessage != "" {
			outcome = "error: " + j.ErrorMessage
		}
		fmt.Printf("%-6d %-10s %-32s %-9s %-20s %s\n", j.ID, j.Kind, clip(addressOf(j), 32), j.Status,
			j.CreatedAt.Local().Format(cliTimeFmt), clip(outcome, 60))
	}
	return nil
}
