package workers

import (
	"database/sql"
	"net/http"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// userFromPath loads the user named by {id}, answering 404 when absent.
func (c *core) userFromPath(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return nil, false
	}
	u, err := models.GetUserByID(c.db, id)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "user not found")
		return nil, false
	}
	if err != nil {
		writeInternalError(w, "failed to load user", err)
		return nil, false
	}
	return u, true
}

// isLastAdmin reports whether u is the only administrator.
func (c *core) isLastAdmin(u *models.User) (bool, error) {
	if u.Role != modules.RoleAdmin {
		return false, nil
	}
	n, err := models.CountAdmins(c.db)
	if err != nil {
		return false, err
	}
	return n <= 1, nil
}

func (c *core) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := models.ListUsers(c.db)
	if err != nil {
		writeInternalError(w, "failed to list users", err)
		return
	}
	out := make([]UserDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (c *core) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	u, err := modules.CreateUser(c.db, body.Username, body.DisplayName, body.Password, body.Role)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": toUserDTO(u)})
}

func (c *core) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	u, ok := c.userFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Role == "" {
		body.Role = u.Role
	}
	if err := modules.ValidateRole(body.Role); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.DisplayName == "" {
		body.DisplayName = u.DisplayName
	}
	if len(body.DisplayName) > 128 {
		writeError(w, http.StatusBadRequest, "display_name must be 128 characters or fewer")
		return
	}
	if body.Role != modules.RoleAdmin {
		last, err := c.isLastAdmin(u)
		if err != nil {
			writeInternalError(w, "failed to count administrators", err)
			return
		}
		if last {
			writeError(w, http.StatusBadRequest, "cannot demote the last administrator")
			return
		}
	}
	if err := models.UpdateUser(c.db, u.ID, body.DisplayName, body.Role); err != nil {
		writeInternalError(w, "failed to update user", err)
		return
	}
	fresh, err := models.GetUserByID(c.db, u.ID)
	if err != nil {
		writeInternalError(w, "failed to reload user", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserDTO(fresh)})
}

func (c *core) handleSetUserPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := c.userFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := modules.ChangePassword(c.db, u.ID, body.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if u.ID == userFrom(r).ID {
		if fresh, err := models.GetUserByID(c.db, u.ID); err == nil {
			c.setSessionCookie(w, fresh)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (c *core) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, ok := c.userFromPath(w, r)
	if !ok {
		return
	}
	if u.ID == userFrom(r).ID {
		writeError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	last, err := c.isLastAdmin(u)
	if err != nil {
		writeInternalError(w, "failed to count administrators", err)
		return
	}
	if last {
		writeError(w, http.StatusBadRequest, "cannot delete the last administrator")
		return
	}
	if err := models.DeleteUser(c.db, u.ID); err != nil {
		writeInternalError(w, "failed to delete user", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
