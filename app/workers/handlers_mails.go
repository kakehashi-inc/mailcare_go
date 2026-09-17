package workers

import (
	"database/sql"
	"encoding/json"
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
		writeError(w, http.StatusBadRequest, "invalid message key")
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
		writeError(w, http.StatusNotFound, "message not found")
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
	page, perPage := 1, defaultPerPage
	if s := q.Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid page")
			return
		}
		page = n
	}
	if s := q.Get("per_page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxPerPage {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("per_page must be between 1 and %d", maxPerPage))
			return
		}
		perPage = n
	}
	if len(q.Get("q")) > 200 {
		writeError(w, http.StatusBadRequest, "query too long")
		return
	}
	filter := models.MessageFilter{
		Query: q.Get("q"), OnlyBounce: q.Get("only_bounce") == "1", GroupKey: q.Get("group"),
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
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": toMessageDTOs(messages), "total": total, "page": page, "per_page": perPage,
	})
}

func (c *core) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	mb, idx, m, ok := c.messageFromPath(w, r)
	if !ok {
		return
	}
	defer idx.Close()
	var bounce *models.Bounce
	if m.IsBounce {
		b, err := models.GetBounceByMessageID(idx, m.ID)
		if err != nil && err != sql.ErrNoRows {
			writeInternalError(w, "failed to load bounce details", err)
			return
		}
		bounce = b
	}
	text := ""
	if data, err := mailengine.ReadMessageFile(c.mailsRoot, mb.Address, m.MessageKey, "txt"); err == nil {
		text = string(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		writeInternalError(w, "failed to read the message text", err)
		return
	}
	headers := map[string]any{}
	if data, err := mailengine.ReadMessageFile(c.mailsRoot, mb.Address, m.MessageKey, "json"); err == nil {
		if jerr := json.Unmarshal(data, &headers); jerr != nil {
			headers = map[string]any{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		writeInternalError(w, "failed to read the message metadata", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": toMessageDTO(m), "bounce": toBounceDTO(bounce), "text": text, "has_html": m.HasHTML, "headers": headers,
	})
}

// handleMessageHTML serves the sanitized HTML body for a sandboxed iframe.
func (c *core) handleMessageHTML(w http.ResponseWriter, r *http.Request) {
	mb, idx, m, ok := c.messageFromPath(w, r)
	if !ok {
		return
	}
	idx.Close()
	data, err := mailengine.ReadMessageFile(c.mailsRoot, mb.Address, m.MessageKey, "html")
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "this message has no HTML part")
		return
	}
	if err != nil {
		writeInternalError(w, "failed to read the message HTML", err)
		return
	}
	safe := htmlPolicy.SanitizeBytes(data)
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
		writeError(w, http.StatusNotFound, "the raw message file is missing")
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
