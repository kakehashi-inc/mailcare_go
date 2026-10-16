package workers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"

	"github.com/microcosm-cc/bluemonday"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// messageKeyRe bounds the shape of a message key accepted in a path
// (YYYYMMDD-HHMMSS_<12 hex>).
var messageKeyRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}_[0-9a-f]{12}$`)

// htmlCSP is sent with sanitized message HTML so that nothing in it can load
// remote resources or run scripts even if the sanitizer missed something.
const htmlCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:"

// htmlPolicy sanitizes message HTML for display in a sandboxed iframe.
var htmlPolicy = bluemonday.UGCPolicy()

const (
	defaultPerPage = 50
	maxPerPage     = 200
)

// messageFromPath opens the index of the mailbox and loads the message named
// by {key}. The caller closes the returned index.
func (c *core) messageFromPath(w http.ResponseWriter, r *http.Request) (*models.Mailbox, *sql.DB, *models.Message, bool) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return nil, nil, nil, false
	}
	key := r.PathValue("key")
	if !messageKeyRe.MatchString(key) {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return nil, nil, nil, false
	}
	idx, err := c.openIndex(r, mb)
	if err != nil {
		writeInternalError(w, "failed to open the mail index", err)
		return nil, nil, nil, false
	}
	m, err := models.GetMessageByKey(idx, key)
	if err == sql.ErrNoRows {
		idx.Close()
		writeError(w, http.StatusNotFound, "system.notFound")
		return nil, nil, nil, false
	}
	if err != nil {
		idx.Close()
		writeInternalError(w, "failed to load message", err)
		return nil, nil, nil, false
	}
	return mb, idx, m, true
}

func (c *core) handleListMessages(w http.ResponseWriter, r *http.Request) {
	mb, ok := c.mailboxFromPath(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, perPage, ok := readPage(q, defaultPerPage, maxPerPage)
	if !ok {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	kind := q.Get("kind")
	switch kind {
	case "", "all":
		kind = models.MessageKindAll
	case models.MessageKindTarget, models.MessageKindJunk, models.MessageKindOther:
	default:
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	filter := models.MessageFilter{
		Query: q.Get("q"), Kind: kind, GroupKey: q.Get("group"), TargetRules: mailengine.TargetRules(),
		Offset: (page - 1) * perPage, Limit: perPage,
	}
	idx, err := c.openIndex(r, mb)
	if err != nil {
		writeInternalError(w, "failed to open the mail index", err)
		return
	}
	defer idx.Close()
	messages, total, err := models.ListMessages(idx, filter)
	if err != nil {
		writeInternalError(w, "failed to list messages", err)
		return
	}
	// counts: the same search and group filter per kind (the switch shows them).
	counts, err := models.CountMessagesByKind(idx, filter)
	if err != nil {
		writeInternalError(w, "failed to count messages", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": toMessageDTOs(messages), "total": total, "page": page, "per_page": perPage, "counts": counts,
	})
}

func (c *core) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	mb, idx, m, ok := c.messageFromPath(w, r)
	if !ok {
		return
	}
	defer idx.Close()
	// One bounce per recipient block; "bounce" is the first of them.
	bounces := []*BounceDTO{}
	if m.IsBounce {
		rows, err := models.ListBouncesByMessage(idx, m.ID)
		if err != nil {
			writeInternalError(w, "failed to load bounce details", err)
			return
		}
		// The responsible party belongs to the group the recipient block
		// was bundled into; the groups of every block are read at once.
		responsibles, err := models.GroupResponsiblesOfMessage(idx, m.ID)
		if err != nil {
			writeInternalError(w, "failed to load the bounce group", err)
			return
		}
		for _, b := range rows {
			bounces = append(bounces, toBounceDTO(b, responsibles[b.GroupKey]))
		}
	}
	var bounce *BounceDTO
	if len(bounces) > 0 {
		bounce = bounces[0]
	}
	// The decoded text sections (<key>-1.txt, <key>-2.txt, ...), as many as
	// the index counted; a message without text answers an empty list.
	sections := []string{}
	if m.TextCount > 0 {
		list, err := mailengine.ReadBodySections(c.mailsRoot, mb.Address, m.MessageKey, "txt", m.TextCount)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeInternalError(w, "failed to read the message text", err)
			return
		}
		if list != nil {
			sections = list
		}
	}
	// The headers are decoded from the .eml on request (nothing but the raw
	// file and the body sections is stored next to the index).
	headers := map[string]string{}
	if raw, err := mailengine.ReadMessageFile(c.mailsRoot, mb.Address, m.MessageKey, "eml"); err == nil {
		if pm := mailengine.ParseMessage(raw); pm != nil && pm.Headers != nil {
			headers = pm.Headers
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		writeInternalError(w, "failed to read the raw message", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": toMessageDTO(m), "bounce": bounce, "bounces": bounces, "text_sections": sections, "headers": headers,
	})
}

// handleMessageHTML serves one sanitized HTML section (<key>-{n}.html) for
// a sandboxed iframe. n counts from 1 up to messages.html_count; a number
// outside that range, or a section file that is missing, answers 404.
func (c *core) handleMessageHTML(w http.ResponseWriter, r *http.Request) {
	mb, idx, m, ok := c.messageFromPath(w, r)
	if !ok {
		return
	}
	idx.Close()
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	if n > m.HTMLCount {
		writeError(w, http.StatusNotFound, "system.notFound")
		return
	}
	section, err := mailengine.ReadBodySection(c.mailsRoot, mb.Address, m.MessageKey, "html", n)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "system.notFound")
		return
	}
	if err != nil {
		writeInternalError(w, "failed to read the message HTML", err)
		return
	}
	safe := htmlPolicy.SanitizeBytes([]byte(section))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", htmlCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(safe)
}

// handleMessageRaw sends the original .eml as a download.
func (c *core) handleMessageRaw(w http.ResponseWriter, r *http.Request) {
	mb, idx, m, ok := c.messageFromPath(w, r)
	if !ok {
		return
	}
	idx.Close()
	data, err := mailengine.ReadMessageFile(c.mailsRoot, mb.Address, m.MessageKey, "eml")
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "system.notFound")
		return
	}
	if err != nil {
		writeInternalError(w, "failed to read the raw message", err)
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.eml"`, m.MessageKey))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
