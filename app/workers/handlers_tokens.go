package workers

import (
	"database/sql"
	"net/http"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// Tokens are managed by administrators only (the routes are wrapped in
// requireAdmin). They are independent of users, reserved for the future API
// and not used for anything yet: the Web login is username + password only.

// handleListTokens lists every token.
func (c *core) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := models.ListTokens(c.db)
	if err != nil {
		writeInternalError(w, "failed to list tokens", err)
		return
	}
	out := make([]TokenDTO, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, toTokenDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// handleCreateToken issues a token; the value is returned only here.
func (c *core) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
		Expires    string `json:"expires"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		body.Name = body.Identifier
	}
	expiresAt, err := modules.ParseExpiry(body.Expires)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok, err := modules.CreateToken(c.db, body.Name, body.Identifier, expiresAt, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// CreateToken fills only the id; re-read the row for created_at.
	if stored, err := models.GetTokenByIdentifier(c.db, tok.Identifier); err == nil {
		tok = stored
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": toTokenDTO(tok), "value": tok.Token})
}

// handleDeleteToken deletes a token by identifier. Deleting the default
// token never promotes another one (see modules.EnsureDefaultToken). The
// first token created afterwards becomes the default (CreateToken).
func (c *core) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	identifier := r.PathValue("identifier")
	if _, err := models.GetTokenByIdentifier(c.db, identifier); err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "token not found")
		return
	} else if err != nil {
		writeInternalError(w, "failed to load token", err)
		return
	}
	if err := models.DeleteToken(c.db, identifier); err != nil {
		writeInternalError(w, "failed to delete token", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
