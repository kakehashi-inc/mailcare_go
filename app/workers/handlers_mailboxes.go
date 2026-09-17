package workers

import (
	"log"
	"net/http"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// mailboxStats opens the index of a mailbox and counts its contents. An index
// that cannot be opened yields zeros (logged) so one broken mailbox does not
// hide the others.
func (c *core) mailboxStats(r *http.Request, mb *models.Mailbox) *MailboxStatsDTO {
	st := &MailboxStatsDTO{}
	idx, err := c.openIndex(r, mb)
	if err != nil {
		log.Printf("index of %s could not be opened: %v", mb.Address, err)
		return st
	}
	defer idx.Close()
	if st.Messages, st.Bounces, err = models.CountMessages(idx); err != nil {
		log.Printf("failed to count messages of %s: %v", mb.Address, err)
	}
	if st.Groups, err = models.CountGroups(idx); err != nil {
		log.Printf("failed to count groups of %s: %v", mb.Address, err)
	}
	return st
}

func (c *core) handleListMailboxes(w http.ResponseWriter, r *http.Request) {
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		writeInternalError(w, "failed to list mailboxes", err)
		return
	}
	withStats := r.URL.Query().Get("stats") == "1"
	out := make([]MailboxDTO, 0, len(mailboxes))
	for _, mb := range mailboxes {
		dto := toMailboxDTO(mb)
		if withStats {
			dto.Stats = c.mailboxStats(r, mb)
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailboxes": out})
}

func (c *core) handleCreateMailbox(w http.ResponseWriter, r *http.Request) {
	var in modules.MailboxInput
	if !decodeJSON(w, r, &in) {
		return
	}
	mb, err := modules.CreateMailbox(c.db, c.key, &in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"mailbox": toMailboxDTO(mb)})
}

func (c *core) handleGetMailbox(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	dto := toMailboxDTO(mb)
	dto.Stats = c.mailboxStats(r, mb)
	writeJSON(w, http.StatusOK, map[string]any{"mailbox": dto})
}

func (c *core) handleUpdateMailbox(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	var in modules.MailboxInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := modules.UpdateMailbox(c.db, c.key, c.mailsRoot, mb, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mailbox": toMailboxDTO(mb)})
}

func (c *core) handleDeleteMailbox(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	keep := r.URL.Query().Get("keep_data") == "1"
	if err := modules.DeleteMailbox(c.db, c.mailsRoot, mb, keep); err != nil {
		writeInternalError(w, "failed to delete mailbox", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestMailbox tries the IMAP connection with the given settings (the
// stored password is used when id is given and the password is empty).
func (c *core) handleTestMailbox(w http.ResponseWriter, r *http.Request) {
	var in modules.MailboxInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := modules.TestMailboxConnection(r.Context(), c.db, c.key, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCheckMailbox queues a check job for one mailbox.
func (c *core) handleCheckMailbox(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	c.enqueueAndRespond(w, r, modules.JobKindCheck, mb.ID, "")
}
