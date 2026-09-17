package modules

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"mailcare/app/models"
)

// --- job-running commands (check / reindex / reclassify / analyze / jobs) ---

// jobSpec is one job to run or submit.
type jobSpec struct {
	kind      string
	mailboxID int64
	address   string // for display; "" = every mailbox
	target    string
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
			label := spec.kind
			if spec.address != "" {
				label += " " + spec.address
			}
			if spec.target != "" {
				label += " " + spec.target
			}
			if created {
				fmt.Printf("Queued job #%d (%s)\n", job.ID, label)
			} else {
				fmt.Printf("Job #%d (%s) is already %s\n", job.ID, label, job.Status)
			}
			if !wait {
				continue
			}
			final, err := WaitForJob(ctx, port, job.ID, echoProgress)
			if err != nil {
				if ctx.Err() != nil {
					fmt.Println("Stopped waiting; the job keeps running on the server.")
					return NewExitError(ExitGeneral, "interrupted")
				}
				return NewExitError(ExitExec, err.Error())
			}
			if !printJobOutcome(final.ID, final.Status, final.Result, final.ErrorMessage) {
				failed++
			}
		}
	} else {
		jm, err := newCLIJobManager(db)
		if err != nil {
			return err
		}
		for _, spec := range specs {
			label := spec.kind
			if spec.address != "" {
				label += " " + spec.address
			}
			if spec.target != "" {
				label += " " + spec.target
			}
			fmt.Printf("Running %s in this process...\n", label)
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
		}
	}
	if failed > 0 {
		return NewExitErrorf(ExitExec, "%d job(s) failed", failed)
	}
	return nil
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

// specsForAddresses builds one spec per address, or a single all-mailbox
// spec when no address is given.
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

// CheckCmd fetches new mail.
type CheckCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every enabled address)"`
	Wait      bool     `help:"When submitted to a running server, wait for completion and show progress"`
}

func (c *CheckCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	specs, err := specsForAddresses(db, JobKindCheck, c.Addresses)
	if err != nil {
		return err
	}
	return runJobs(db, specs, c.Wait)
}

// ReindexCmd rebuilds the index from the raw files.
type ReindexCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every address)"`
}

func (c *ReindexCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	specs, err := specsForAddresses(db, JobKindReindex, c.Addresses)
	if err != nil {
		return err
	}
	return runJobs(db, specs, true)
}

// ReclassifyCmd re-runs bounce detection and grouping.
type ReclassifyCmd struct {
	Addresses []string `arg:"" optional:"" help:"Mail addresses (default: every address)"`
}

func (c *ReclassifyCmd) Run() error {
	db, err := openDBForCLI()
	if err != nil {
		return err
	}
	defer db.Close()
	specs, err := specsForAddresses(db, JobKindReclassify, c.Addresses)
	if err != nil {
		return err
	}
	return runJobs(db, specs, true)
}

// AnalyzeCmd runs the agent over bounce groups.
type AnalyzeCmd struct {
	Address string `arg:"" optional:"" help:"Mail address (default: every address)"`
	Group   string `help:"Analyze only this group key"`
	All     bool   `help:"Analyze every group, not only those flagged for analysis"`
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
	var mailboxes []*models.Mailbox
	if c.Address != "" {
		mb, err := findMailbox(db, c.Address)
		if err != nil {
			return err
		}
		mailboxes = []*models.Mailbox{mb}
	} else if mailboxes, err = models.ListMailboxes(db); err != nil {
		return NewExitError(ExitGeneral, err.Error())
	}
	if len(mailboxes) == 0 {
		fmt.Println("No mailboxes.")
		return nil
	}
	var specs []jobSpec
	for _, mb := range mailboxes {
		specs = append(specs, jobSpec{kind: JobKindAnalyze, mailboxID: mb.ID, address: mb.Address, target: target})
	}
	return runJobs(db, specs, true)
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
				"requested_by": j.RequestedBy, "created_at": j.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
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
