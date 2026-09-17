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
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"job": toJobDTO(job, c.mailboxAddresses()), "created": created})
}

func (c *core) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit := defaultJobLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxJobLimit {
			writeError(w, http.StatusBadRequest, "invalid limit")
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

// handleCreateJob queues a job. Check jobs may be queued by any user; the
// other kinds need an administrator.
func (c *core) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var body modules.JobRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := modules.ValidateJobKind(body.Kind); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Kind != modules.JobKindCheck && userFrom(r).Role != modules.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if body.MailboxID < 0 {
		writeError(w, http.StatusBadRequest, "invalid mailbox_id")
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
		writeError(w, http.StatusNotFound, "job not found")
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
		writeError(w, http.StatusConflict, "only queued jobs can be canceled")
		return
	}
	fresh, err := models.GetJobByID(c.db, job.ID)
	if err != nil {
		writeInternalError(w, "failed to reload job", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": toJobDTO(fresh, c.mailboxAddresses())})
}
