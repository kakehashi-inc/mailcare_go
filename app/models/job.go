package models

import (
	"database/sql"
	"time"
)

// Job is a row of the jobs table: one unit of background work (sync, fetch,
// group, analyze, reindex, reclassify, notify) with its progress and outcome.
// Jobs are executed by the job workers (app/modules/jobs.go).
//
// Columns: the kind, the target mailbox, the extra target, the status, the
// requester and the three timestamps. Progress, result and error text are
// stored together in the detail_info JSON column (jobDetails).
type Job struct {
	ID          int64         `json:"id"`
	Kind        string        `json:"kind"`
	MailboxID   sql.NullInt64 `json:"-"`      // NULL = expand to every mailbox (notify: always NULL)
	Target      string        `json:"target"` // analyze: group key / "*" / ""; notify: "" or "test:<address>"
	Status      string        `json:"status"`
	RequestedBy string        `json:"requested_by"`
	CreatedAt   time.Time     `json:"created_at"`
	StartedAt   sql.NullTime  `json:"-"`
	FinishedAt  sql.NullTime  `json:"-"`

	// Stored in the detail_info JSON column.
	Progress     string `json:"progress"`
	Result       string `json:"result"`
	ErrorMessage string `json:"error_message"`
}

// jobDetails is the JSON shape of jobs.detail_info.
type jobDetails struct {
	Progress string `json:"progress,omitempty"`
	Result   string `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (j *Job) applyDetails(raw string) {
	var o jobDetails
	unmarshalJSON(raw, &o)
	j.Progress, j.Result, j.ErrorMessage = o.Progress, o.Result, o.Error
}

const jobColumns = `id, kind, mailbox_id, target, status, requested_by, created_at, started_at, finished_at, detail_info`

// InsertJob queues a job and fills in its ID.
func InsertJob(db *sql.DB, j *Job) error {
	now := time.Now().UTC()
	if j.Status == "" {
		j.Status = "queued"
	}
	res, err := db.Exec(
		`INSERT INTO jobs (kind, mailbox_id, target, status, requested_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		j.Kind, j.MailboxID, truncateRunes(j.Target, 320), j.Status, truncateRunes(j.RequestedBy, 80), now,
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
	return ClaimNextRunnableJob(db, nil)
}

// ClaimNextRunnableJob walks the queued jobs oldest first and atomically
// moves the first one accepted by canRun to running, returning it, or nil
// when no queued job is runnable. canRun decides whether the job may start
// now (the job manager checks the resource keys of the job against the jobs
// already running). The claim itself is an atomic conditional update, so two
// callers can never claim the same job.
func ClaimNextRunnableJob(db *sql.DB, canRun func(*Job) bool) (*Job, error) {
	queued, err := queryJobs(db, `SELECT `+jobColumns+` FROM jobs WHERE status = 'queued' ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
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

// ClaimJobByID atomically moves one specific queued job to running and returns
// it, or nil when it is no longer queued (used by the in-process CLI runner).
func ClaimJobByID(db *sql.DB, id int64) (*Job, error) {
	res, err := db.Exec(`UPDATE jobs SET status = 'running', started_at = ? WHERE id = ? AND status = 'queued'`,
		time.Now().UTC(), id)
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

// UpdateJobProgress replaces the progress text of a running job.
func UpdateJobProgress(db *sql.DB, id int64, progress string) error {
	_, err := db.Exec(`UPDATE jobs SET detail_info = json_set(detail_info, '$.progress', ?) WHERE id = ?`, progress, id)
	return err
}

// FinishJob marks a job done (errMsg empty) or error, with its result text.
func FinishJob(db *sql.DB, id int64, result, errMsg string) error {
	status := "done"
	if errMsg != "" {
		status = "error"
	}
	_, err := db.Exec(
		`UPDATE jobs SET status = ?, detail_info = json_set(detail_info, '$.result', ?, '$.error', ?), finished_at = ? WHERE id = ?`,
		status, result, errMsg, time.Now().UTC(), id,
	)
	return err
}

// CancelQueuedJob cancels a job that has not started yet. It reports whether a
// row was changed.
func CancelQueuedJob(db *sql.DB, id int64) (bool, error) {
	res, err := db.Exec(`UPDATE jobs SET status = 'canceled', finished_at = ? WHERE id = ? AND status = 'queued'`,
		time.Now().UTC(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ResetRunningJobs marks jobs left in running state (e.g. after a crash) as
// error so they are not shown as in progress forever.
func ResetRunningJobs(db *sql.DB, reason string) error {
	_, err := db.Exec(`UPDATE jobs SET status = 'error', detail_info = json_set(detail_info, '$.error', ?), finished_at = ?
		WHERE status = 'running'`, reason, time.Now().UTC())
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
	return queryJobs(db, `SELECT `+jobColumns+` FROM jobs ORDER BY id DESC LIMIT ?`, limit)
}

// ListActiveJobs returns queued and running jobs, oldest first.
func ListActiveJobs(db *sql.DB) ([]*Job, error) {
	return queryJobs(db, `SELECT `+jobColumns+` FROM jobs WHERE status IN ('queued','running') ORDER BY id ASC`)
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

func queryJobs(db *sql.DB, query string, args ...any) ([]*Job, error) {
	rows, err := db.Query(query, args...)
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

func scanJob(s rowScanner) (*Job, error) {
	j := &Job{}
	var details string
	if err := s.Scan(&j.ID, &j.Kind, &j.MailboxID, &j.Target, &j.Status, &j.RequestedBy, &j.CreatedAt, &j.StartedAt,
		&j.FinishedAt, &details); err != nil {
		return nil, err
	}
	j.applyDetails(details)
	return j, nil
}
