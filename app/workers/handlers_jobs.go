package workers

import (
	"database/sql"
	"net/http"
	"strconv"

	"mailcare/app/models"
	"mailcare/app/modules"
)

const (
	defaultJobLimit = 50
	maxJobLimit     = 500
	maxJobPage      = 1000000 // bounds the offset of the history
)

// requestedBy tags a job with the Web user who asked for it.
func requestedBy(r *http.Request) string {
	if u := userFrom(r); u != nil {
		return "web:" + u.Username
	}
	return "control"
}

// enqueueAndRespond queues a job and answers {job, created}. An identical
// active job answers 200 with created=false; a new one answers 201.
func (c *core) enqueueAndRespond(w http.ResponseWriter, r *http.Request, kind string, mailboxID int64, target string) {
	job, created, err := c.jm.Enqueue(kind, mailboxID, target, requestedBy(r))
	if err != nil {
		writeErrorMessage(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"job": toJobDTO(job, c.mailboxAddresses()), "created": created})
}

// handleListJobs lists jobs (administrators only). state selects the list:
// "active" answers every queued and running job (oldest first), "finished"
// one page (page, per_page) of the finished jobs matching status ("" / all,
// done, error, canceled) with the total and the count per status, and no state the
// newest jobs of any status (at most limit).
func (c *core) handleListJobs(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("state") {
	case "":
	case "active":
		jobs, err := models.ListActiveJobs(c.db)
		if err != nil {
			writeInternalError(w, "failed to list jobs", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": toJobDTOs(jobs, c.mailboxAddresses())})
		return
	case "finished":
		c.listFinishedJobs(w, r)
		return
	default:
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	limit := defaultJobLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxJobLimit {
			writeError(w, http.StatusBadRequest, "system.invalidRequest")
			return
		}
		limit = n
	}
	jobs, err := models.ListJobs(c.db, limit)
	if err != nil {
		writeInternalError(w, "failed to list jobs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": toJobDTOs(jobs, c.mailboxAddresses())})
}

// listFinishedJobs answers one page of the finished-job history.
func (c *core) listFinishedJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	switch status {
	case "", "all":
		status = models.JobHistoryAll
	case models.JobHistoryDone, models.JobHistoryError, models.JobHistoryCanceled:
	default:
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	page, perPage := 1, defaultJobLimit
	for _, p := range []struct {
		name string
		dst  *int
		max  int
	}{{"page", &page, maxJobPage}, {"per_page", &perPage, maxJobLimit}} {
		s := q.Get(p.name)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > p.max {
			writeError(w, http.StatusBadRequest, "system.invalidRequest")
			return
		}
		*p.dst = n
	}
	jobs, total, err := models.ListFinishedJobs(c.db, status, (page-1)*perPage, perPage)
	if err != nil {
		writeInternalError(w, "failed to list jobs", err)
		return
	}
	counts, err := models.CountFinishedJobs(c.db)
	if err != nil {
		writeInternalError(w, "failed to count jobs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": toJobDTOs(jobs, c.mailboxAddresses()), "total": total, "page": page, "per_page": perPage, "counts": counts,
	})
}

// handleCreateJob queues a job of any kind (administrators only; the route
// is wrapped in requireAdmin).
func (c *core) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var body modules.JobRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := modules.ValidateJobKind(body.Kind); err != nil {
		writeErrorMessage(w, r, err)
		return
	}
	if body.MailboxID < 0 {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	c.enqueueAndRespond(w, r, body.Kind, body.MailboxID, body.Target)
}

// jobFromPath loads the job named by {id}, answering 404 when absent.
func (c *core) jobFromPath(w http.ResponseWriter, r *http.Request) (*models.Job, bool) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return nil, false
	}
	job, err := models.GetJobByID(c.db, id)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "system.notFound")
		return nil, false
	}
	if err != nil {
		writeInternalError(w, "failed to load job", err)
		return nil, false
	}
	return job, true
}

func (c *core) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, ok := c.jobFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": toJobDTO(job, c.mailboxAddresses())})
}

// handleCancelJob cancels a queued job (a running job cannot be stopped).
func (c *core) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, ok := c.jobFromPath(w, r)
	if !ok {
		return
	}
	changed, err := models.CancelQueuedJob(c.db, job.ID)
	if err != nil {
		writeInternalError(w, "failed to cancel job", err)
		return
	}
	if !changed {
		writeError(w, http.StatusConflict, "result.job.notQueued")
		return
	}
	fresh, err := models.GetJobByID(c.db, job.ID)
	if err != nil {
		writeInternalError(w, "failed to reload job", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": toJobDTO(fresh, c.mailboxAddresses())})
}
