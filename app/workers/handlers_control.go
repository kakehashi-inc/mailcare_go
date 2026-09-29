package workers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/message"
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
	// data_dir lets the CLI tell whether this server is the one of its own
	// data directory before it hands jobs over or stops it.
	writeJSON(w, http.StatusOK, modules.ServerStatus{
		Status: "running", Name: modules.AppName, Version: modules.AppVersion,
		WebListen: fmt.Sprintf("%s:%d", c.webListen, c.webPort), DataDir: c.dataDir,
		Uptime: formatDuration(time.Since(c.startTime)), Users: users, Tokens: tokens,
		Mailboxes: len(mailboxes), ActiveJobs: len(active), NextCheckAt: next,
	})
}

// The control endpoints answer errors as English text for the CLI
// (writeControlError), not with the codes of the Web API.

// handleControlCreateJob queues a job on behalf of the local CLI.
func (c *core) handleControlCreateJob(w http.ResponseWriter, r *http.Request) {
	var body modules.JobRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&body); err != nil {
		writeControlError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := modules.ValidateJobKind(body.Kind); err != nil {
		writeControlError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.MailboxID < 0 {
		writeControlError(w, http.StatusBadRequest, "invalid mailbox_id")
		return
	}
	job, created, err := c.jm.Enqueue(body.Kind, body.MailboxID, body.Target, modules.RequestedByCLI)
	if err != nil {
		if _, ok := message.As(err); !ok {
			log.Printf("control: failed to queue a job: %v", err)
		}
		writeControlError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"job": toJobDTO(job, c.mailboxAddresses()), "created": created})
}

func (c *core) handleControlGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeControlError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := models.GetJobByID(c.db, id)
	if err == sql.ErrNoRows {
		writeControlError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		log.Printf("control: failed to load job %d: %v", id, err)
		writeControlError(w, http.StatusInternalServerError, "failed to load job")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": toJobDTO(job, c.mailboxAddresses())})
}
