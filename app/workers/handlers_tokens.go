package workers

import (
	"database/sql"
	"net/http"
	"strconv"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// usernames maps user ids to usernames for token DTOs.
func (c *core) usernames() (map[int64]string, error) {
	users, err := models.ListUsers(c.db)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(users))
	for _, u := range users {
		out[u.ID] = u.Username
	}
	return out, nil
}

// handleListTokens lists tokens: administrators see every token (optionally
// filtered by ?user_id=), other users only their own.
func (c *core) handleListTokens(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r)
	userID := me.ID
	if q := r.URL.Query().Get("user_id"); q != "" {
		id, err := strconv.ParseInt(q, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		if id != me.ID && me.Role != modules.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		userID = id
	} else if me.Role == modules.RoleAdmin {
		userID = 0
	}
	var (
		tokens []*models.Token
		err    error
	)
	if userID == 0 {
		tokens, err = models.ListTokens(c.db)
	} else {
		tokens, err = models.ListTokensByUser(c.db, userID)
	}
	if err != nil {
		writeInternalError(w, "failed to list tokens", err)
		return
	}
	names, err := c.usernames()
	if err != nil {
		writeInternalError(w, "failed to list users", err)
		return
	}
	out := make([]TokenDTO, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, toTokenDTO(t, names[t.UserID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// handleCreateToken issues a token; the value is returned only here.
func (c *core) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r)
	var body struct {
		UserID  int64  `json:"user_id"`
		Name    string `json:"name"`
		Expires string `json:"expires"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	userID := me.ID
	if body.UserID != 0 {
		if body.UserID != me.ID && me.Role != modules.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		userID = body.UserID
	}
	owner, err := models.GetUserByID(c.db, userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusBadRequest, "user not found")
		return
	}
	if err != nil {
		writeInternalError(w, "failed to load user", err)
		return
	}
	expiresAt, err := modules.ParseExpiry(body.Expires)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok, err := modules.CreateLoginToken(c.db, owner.ID, body.Name, "", expiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": toTokenDTO(tok, owner.Username), "value": tok.Token})
}

// handleDeleteToken deletes a token; non-administrators may delete only
// their own.
func (c *core) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	me := userFrom(r)
	identifier := r.PathValue("identifier")
	tok, err := models.GetTokenByIdentifier(c.db, identifier)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}
	if err != nil {
		writeInternalError(w, "failed to load token", err)
		return
	}
	if tok.UserID != me.ID && me.Role != modules.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := models.DeleteToken(c.db, identifier); err != nil {
		writeInternalError(w, "failed to delete token", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
