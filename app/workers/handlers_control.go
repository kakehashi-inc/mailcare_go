package workers

import (
	"fmt"
	"net/http"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// Control endpoints: reachable from loopback only (see webMiddleware), used
// by "service stop" / "service status" and by the CLI to hand jobs to the
// running server instead of touching the indexes from a second process.

func (c *core) handleControlShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "shutting down"})
	go c.shutdown()
}

func (c *core) handleControlStatus(w http.ResponseWriter, r *http.Request) {
	users, _ := models.CountUsers(c.db)
	tokens, _ := models.CountTokens(c.db)
	mailboxes, _ := models.ListMailboxes(c.db)
	active, _ := models.ListActiveJobs(c.db)
	next := ""
	if s := c.nextCheckAt(); s != nil {
		next = *s
	}
	writeJSON(w, http.StatusOK, modules.ServerStatus{
		Status: "running", Name: modules.AppName, Version: modules.AppVersion,
		WebListen: fmt.Sprintf("%s:%d", c.webListen, c.webPort), Uptime: formatDuration(time.Since(c.startTime)),
		Users: users, Tokens: tokens, Mailboxes: len(mailboxes), ActiveJobs: len(active), NextCheckAt: next,
	})
}

// handleControlCreateJob queues a job on behalf of the local CLI.
func (c *core) handleControlCreateJob(w http.ResponseWriter, r *http.Request) {
	var body modules.JobRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := modules.ValidateJobKind(body.Kind); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.MailboxID < 0 {
		writeError(w, http.StatusBadRequest, "invalid mailbox_id")
		return
	}
	job, created, err := c.jm.Enqueue(body.Kind, body.MailboxID, body.Target, modules.RequestedByCLI)
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

func (c *core) handleControlGetJob(w http.ResponseWriter, r *http.Request) {
	c.handleGetJob(w, r)
}
