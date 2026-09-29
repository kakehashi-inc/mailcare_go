package workers

import (
	"database/sql"
	"log"
	"net/http"

	"mailcare/app/models"
	"mailcare/app/modules"
	"mailcare/app/modules/mailengine"
)

// mailboxStats opens the index of a mailbox and counts its contents. An index
// that cannot be opened yields zeros (logged) so one broken mailbox does not
// hide the others.
func (c *core) mailboxStats(r *http.Request, mb *models.Mailbox) *MailboxStatsDTO {
	idx, err := c.openIndex(r, mb)
	if err != nil {
		log.Printf("index of %s could not be opened: %v", mb.Address, err)
		return &MailboxStatsDTO{}
	}
	defer idx.Close()
	return indexStats(idx, mb)
}

// indexStats counts the contents of an open index. Group counts cover the
// actionable groups; the excluded (recipient-side) groups are counted apart.
func indexStats(idx *sql.DB, mb *models.Mailbox) *MailboxStatsDTO {
	st := &MailboxStatsDTO{}
	address := mb.Address
	var err error
	if st.Messages, st.Bounces, err = models.CountMessages(idx); err != nil {
		log.Printf("failed to count messages of %s: %v", address, err)
	}
	if st.Unclassified, err = models.CountUnclassifiedMessages(idx); err != nil {
		log.Printf("failed to count unclassified messages of %s: %v", address, err)
	}
	if st.TargetMessages, err = mailengine.CountTargetMessages(idx); err != nil {
		log.Printf("failed to count target messages of %s: %v", address, err)
	}
	if st.JunkMessages, err = mailengine.CountJunkMessages(idx); err != nil {
		log.Printf("failed to count junk messages of %s: %v", address, err)
	}
	if st.Groups, err = models.CountGroups(idx, models.GroupScopeActionable); err != nil {
		log.Printf("failed to count groups of %s: %v", address, err)
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
	u := userFrom(r)
	out := make([]MailboxDTO, 0, len(mailboxes))
	for _, mb := range mailboxes {
		dto := mailboxDTOFor(mb, u)
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
		writeErrorMessage(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"mailbox": toMailboxDTO(mb)})
}

func (c *core) handleGetMailbox(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	dto := mailboxDTOFor(mb, userFrom(r))
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
	if err := modules.UpdateMailbox(c.db, c.key, c.mailsRoot, c.agentRoot, mb, &in); err != nil {
		writeErrorMessage(w, r, err)
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
	if err := modules.DeleteMailbox(c.db, c.mailsRoot, c.agentRoot, mb, keep); err != nil {
		writeErrorMessage(w, r, err)
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
		writeErrorMessage(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSyncMailbox queues a sync job (fetch, group, analyze) for one mailbox.
func (c *core) handleSyncMailbox(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	c.enqueueAndRespond(w, r, modules.JobKindSync, mb.ID, "")
}
