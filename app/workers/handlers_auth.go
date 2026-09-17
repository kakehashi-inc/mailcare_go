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

// setSessionCookie issues a fresh session cookie. remember = true gives a
// persistent cookie (Max-Age = cookie_ttl_hours) with a sliding expiry;
// remember = false gives a browser-session cookie (no Max-Age) that expires
// after SessionCookieTTLHours and is never refreshed.
func (c *core) setSessionCookie(w http.ResponseWriter, u *models.User, remember bool) {
	ttlHours := modules.SessionCookieTTLHours
	if remember {
		ttlHours = c.cookieTTLHours
	}
	expiry := time.Now().Add(time.Duration(ttlHours) * time.Hour)
	val, err := modules.IssueSessionCookie(c.key, u, expiry, remember)
	if err != nil {
		log.Printf("failed to issue a session cookie: %v", err)
		return
	}
	ck := &http.Cookie{Name: modules.CookieName, Value: val, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if remember {
		ck.MaxAge = c.cookieTTLHours * 3600
	}
	http.SetCookie(w, ck)
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
		Email       string `json:"email"`
		Language    string `json:"language"`
		Timezone    string `json:"timezone"`
		Theme       string `json:"theme"`
		Password    string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	u, err := modules.CreateUserFrom(c.db, modules.NewUser{Username: body.Username, DisplayName: body.DisplayName, Email: body.Email,
		Language: body.Language, Timezone: body.Timezone, Theme: body.Theme, Password: body.Password, Role: modules.RoleAdmin})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = models.TouchUserLogin(c.db, u.ID)
	c.setSessionCookie(w, u, false)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "user": toUserDTO(u)})
}

// handleLogin signs in with username + password. remember = true opens a
// persistent session (see setSessionCookie). Tokens are not accepted here:
// they are reserved for the future API.
func (c *core) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if !decodeJSON(w, r, &body) {
			return
		}
	} else {
		body.Username, body.Password = r.FormValue("username"), r.FormValue("password")
		body.Remember, _ = modules.ParseBoolSetting(r.FormValue("remember"))
	}
	if body.Username == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	u, err := modules.AuthenticateUser(c.db, body.Username, body.Password)
	if errors.Is(err, modules.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		writeInternalError(w, "login failed", err)
		return
	}
	_ = models.TouchUserLogin(c.db, u.ID)
	c.setSessionCookie(w, u, body.Remember)
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
	// Read the session mode before the change invalidates the cookie.
	remember := c.sessionRemember(r)
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
		c.setSessionCookie(w, fresh, remember)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sessionRemember reports whether the caller's current session is a
// remembered one (true when the cookie cannot be read).
func (c *core) sessionRemember(r *http.Request) bool {
	sess, err := modules.ValidateSessionCookie(c.db, c.key, cookieValue(r))
	if err != nil {
		return true
	}
	return sess.Remember
}

// handleChangeMyProfile changes the caller's own profile: display name,
// notification address, language, timezone and theme. Absent fields are
// left unchanged; an empty email clears the address; an empty preference
// restores its default.
func (c *core) handleChangeMyProfile(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var body struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Language    *string `json:"language"`
		Timezone    *string `json:"timezone"`
		Theme       *string `json:"theme"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.DisplayName != nil && *body.DisplayName == "" {
		body.DisplayName = nil
	}
	in := modules.ProfileInput{DisplayName: body.DisplayName, Email: body.Email, Language: body.Language, Timezone: body.Timezone, Theme: body.Theme}
	if err := modules.UpdateProfile(c.db, u, in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fresh, err := models.GetUserByID(c.db, u.ID)
	if err != nil {
		writeInternalError(w, "failed to reload user", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(fresh)})
}
