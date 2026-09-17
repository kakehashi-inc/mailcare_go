package workers

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// handleHealth is the liveness endpoint; it also exposes the build version.
func (c *core) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": modules.AppVersion})
}

// handleSetupStatus tells the SPA whether the first administrator must be
// created.
func (c *core) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := models.CountUsers(c.db)
	if err != nil {
		writeInternalError(w, "failed to count users", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": n == 0})
}

// setSessionCookie issues a fresh session cookie with a sliding expiry.
func (c *core) setSessionCookie(w http.ResponseWriter, u *models.User) {
	expiry := time.Now().Add(time.Duration(c.cookieTTLHours) * time.Hour)
	val, err := modules.IssueSessionCookie(c.key, u, expiry)
	if err != nil {
		log.Printf("failed to issue a session cookie: %v", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: modules.CookieName, Value: val, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: c.cookieTTLHours * 3600,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: modules.CookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
}

// handleSetup creates the first administrator (only while no user exists)
// and signs them in.
func (c *core) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := models.CountUsers(c.db)
	if err != nil {
		writeInternalError(w, "failed to count users", err)
		return
	}
	if n > 0 {
		writeError(w, http.StatusForbidden, "setup is already done")
		return
	}
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	u, err := modules.CreateUser(c.db, body.Username, body.DisplayName, body.Password, modules.RoleAdmin)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = models.TouchUserLogin(c.db, u.ID)
	c.setSessionCookie(w, u)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "user": toUserDTO(u)})
}

// handleLogin signs in with username + password or with a login token.
func (c *core) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if !decodeJSON(w, r, &body) {
			return
		}
	} else {
		body.Username, body.Password, body.Token = r.FormValue("username"), r.FormValue("password"), r.FormValue("token")
	}
	var (
		u   *models.User
		err error
	)
	if tokenValue := strings.TrimSpace(body.Token); tokenValue != "" {
		var tok *models.Token
		u, tok, err = modules.AuthenticateToken(c.db, tokenValue)
		if err == nil {
			_ = models.TouchTokenUse(c.db, tok.ID)
		}
	} else {
		if body.Username == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "username and password, or token, are required")
			return
		}
		u, err = modules.AuthenticateUser(c.db, body.Username, body.Password)
	}
	if errors.Is(err, modules.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeInternalError(w, "login failed", err)
		return
	}
	_ = models.TouchUserLogin(c.db, u.ID)
	c.setSessionCookie(w, u)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": toUserDTO(u)})
}

// handleLogout clears the session cookie.
func (c *core) handleLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMe returns the signed-in user.
func (c *core) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(userFrom(r)), "version": modules.AppVersion})
}

// handleChangeMyPassword changes the caller's own password and re-issues the
// session cookie (the old sessions become invalid).
func (c *core) handleChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !modules.VerifyPassword(u, body.CurrentPassword) {
		writeError(w, http.StatusBadRequest, "current password is wrong")
		return
	}
	if err := modules.ChangePassword(c.db, u.ID, body.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fresh, err := models.GetUserByID(c.db, u.ID)
	if err != nil && err != sql.ErrNoRows {
		writeInternalError(w, "failed to reload user", err)
		return
	}
	if fresh != nil {
		c.setSessionCookie(w, fresh)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
