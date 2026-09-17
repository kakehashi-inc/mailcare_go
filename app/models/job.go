package models

import (
	"database/sql"
	"time"
)

// Job is a row of the jobs table: one unit of background work (mail check,
// reindex, reclassify, agent analysis) with its progress and outcome. Jobs are
// processed one at a time by the job worker (app/modules/jobs.go).
type Job struct {
	ID           int64         `json:"id"`
	Kind         string        `json:"kind"`
	MailboxID    sql.NullInt64 `json:"-"`
	Target       string        `json:"target"` // optional: a group key (analyze) or "" for all
	Status       string        `json:"status"`
	Progress     string        `json:"progress"`
	Result       string        `json:"result"`
	ErrorMessage string        `json:"error_message"`
	RequestedBy  string        `json:"requested_by"`
	CreatedAt    time.Time     `json:"created_at"`
	StartedAt    sql.NullTime  `json:"-"`
	FinishedAt   sql.NullTime  `json:"-"`
}

const jobColumns = `id, kind, mailbox_id, target, status, progress, result, error_message, requested_by,
	created_at, started_at, finished_at`

// InsertJob queues a job and fills in its ID.
func InsertJob(db *sql.DB, j *Job) error {
	now := time.Now().UTC()
	if j.Status == "" {
		j.Status = "queued"
	}
	res, err := db.Exec(
		`INSERT INTO jobs (kind, mailbox_id, target, status, requested_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		j.Kind, j.MailboxID, j.Target, j.Status, j.RequestedBy, now,
	)
	if err != nil {
		return err
	}
	j.ID, _ = res.LastInsertId()
	j.CreatedAt = now
	return nil
}

// ClaimNextJob atomically moves the oldest queued job to running and returns
// it, or nil when nothing is queued.
func ClaimNextJob(db *sql.DB) (*Job, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRow(`SELECT ` + jobColumns + ` FROM jobs WHERE status = 'queued' ORDER BY created_at ASC, id ASC LIMIT 1`))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := tx.Exec(`UPDATE jobs SET status = 'running', started_at = ? WHERE id = ? AND status = 'queued'`, now, j.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.Status = "running"
	j.StartedAt = sql.NullTime{Time: now, Valid: true}
	return j, nil
}

// UpdateJobProgress replaces the progress text of a running job.
func UpdateJobProgress(db *sql.DB, id int64, progress string) error {
	_, err := db.Exec(`UPDATE jobs SET progress = ? WHERE id = ?`, progress, id)
	return err
}

// FinishJob marks a job done (errMsg empty) or error, with its result text.
func FinishJob(db *sql.DB, id int64, result, errMsg string) error {
	status := "done"
	if errMsg != "" {
		status = "error"
	}
	_, err := db.Exec(
		`UPDATE jobs SET status = ?, result = ?, error_message = ?, finished_at = ? WHERE id = ?`,
		status, result, errMsg, time.Now().UTC(), id,
	)
	return err
}

// CancelQueuedJob cancels a job that has not started yet. It reports whether a
// row was changed.
func CancelQueuedJob(db *sql.DB, id int64) (bool, error) {
	res, err := db.Exec(
		`UPDATE jobs SET status = 'canceled', finished_at = ? WHERE id = ? AND status = 'queued'`,
		time.Now().UTC(), id,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ResetRunningJobs marks jobs left in running state (e.g. after a crash) as
// error so they are not shown as in progress forever.
func ResetRunningJobs(db *sql.DB, reason string) error {
	_, err := db.Exec(
		`UPDATE jobs SET status = 'error', error_message = ?, finished_at = ? WHERE status = 'running'`,
		reason, time.Now().UTC(),
	)
	return err
}

// GetJobByID returns one job (sql.ErrNoRows when absent).
func GetJobByID(db *sql.DB, id int64) (*Job, error) {
	return scanJob(db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
}

// ListJobs returns the newest jobs first, at most limit rows.
func ListJobs(db *sql.DB, limit int) ([]*Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`SELECT `+jobColumns+` FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListActiveJobs returns queued and running jobs, oldest first.
func ListActiveJobs(db *sql.DB) ([]*Job, error) {
	rows, err := db.Query(`SELECT ` + jobColumns + ` FROM jobs WHERE status IN ('queued','running') ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// HasActiveJob reports whether a queued or running job of the kind exists for
// the mailbox (mailboxID 0 matches jobs without a mailbox) and target.
func HasActiveJob(db *sql.DB, kind string, mailboxID int64, target string) (bool, error) {
	var n int
	var err error
	if mailboxID == 0 {
		err = db.QueryRow(
			`SELECT COUNT(*) FROM jobs WHERE kind = ? AND mailbox_id IS NULL AND target = ? AND status IN ('queued','running')`,
			kind, target).Scan(&n)
	} else {
		err = db.QueryRow(
			`SELECT COUNT(*) FROM jobs WHERE kind = ? AND mailbox_id = ? AND target = ? AND status IN ('queued','running')`,
			kind, mailboxID, target).Scan(&n)
	}
	return n > 0, err
}

// DeleteOldJobs removes finished jobs older than the given time.
func DeleteOldJobs(db *sql.DB, before time.Time) error {
	_, err := db.Exec(
		`DELETE FROM jobs WHERE status IN ('done','error','canceled') AND finished_at IS NOT NULL AND finished_at < ?`,
		before.UTC(),
	)
	return err
}

func scanJob(s rowScanner) (*Job, error) {
	j := &Job{}
	if err := s.Scan(&j.ID, &j.Kind, &j.MailboxID, &j.Target, &j.Status, &j.Progress, &j.Result, &j.ErrorMessage,
		&j.RequestedBy, &j.CreatedAt, &j.StartedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	return j, nil
}

// ClaimJobByID atomically moves one specific queued job to running and returns
// it, or nil when it is no longer queued (used by the in-process CLI runner).
func ClaimJobByID(db *sql.DB, id int64) (*Job, error) {
	now := time.Now().UTC()
	res, err := db.Exec(`UPDATE jobs SET status = 'running', started_at = ? WHERE id = ? AND status = 'queued'`, now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return GetJobByID(db, id)
}

// NextQueuedJobAfter returns the oldest queued job created after the job with
// id afterID by the given requester, or nil when there is none (used by the
// in-process CLI runner to execute the follow-up jobs it queued itself).
func NextQueuedJobAfter(db *sql.DB, afterID int64, requestedBy string) (*Job, error) {
	j, err := scanJob(db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE status = 'queued' AND id > ? AND requested_by = ?
		ORDER BY id ASC LIMIT 1`, afterID, requestedBy))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// ClaimNextRunnableJob walks the queued jobs oldest first and atomically
// moves the first one accepted by canRun to running, returning it, or nil
// when no queued job is runnable. canRun decides whether the job may start
// now (the job manager checks the resource keys of the job against the jobs
// already running). The claim itself is an atomic conditional update, so two
// callers can never claim the same job.
func ClaimNextRunnableJob(db *sql.DB, canRun func(*Job) bool) (*Job, error) {
	rows, err := db.Query(`SELECT ` + jobColumns + ` FROM jobs WHERE status = 'queued' ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	var queued []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		queued = append(queued, j)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, j := range queued {
		if canRun != nil && !canRun(j) {
			continue
		}
		claimed, err := ClaimJobByID(db, j.ID)
		if err != nil {
			return nil, err
		}
		if claimed != nil {
			return claimed, nil
		}
	}
	return nil, nil
}
