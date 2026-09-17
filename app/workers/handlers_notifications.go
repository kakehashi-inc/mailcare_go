package workers

import (
	"net/http"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// Notification settings and mails (the system design document (Documents)
// 7.4, 9.2). Administrators only. The SMTP password is never returned; the
// DTO says only whether one is stored.

// NotificationRecipientDTO is one user of the recipient picker.
type NotificationRecipientDTO struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

// NotificationSettingsDTO is the payload of GET/PUT /api/v1/settings/notifications.
type NotificationSettingsDTO struct {
	SMTPHost           string                     `json:"smtp_host"`
	SMTPPort           int                        `json:"smtp_port"`
	SMTPSecurity       string                     `json:"smtp_security"`
	SMTPUsername       string                     `json:"smtp_username"`
	SMTPPasswordSet    bool                       `json:"smtp_password_set"`
	SMTPFrom           string                     `json:"smtp_from"`
	PublicBaseURL      string                     `json:"public_base_url"`
	EffectiveBaseURL   string                     `json:"effective_base_url"`
	NotifyEnabled      bool                       `json:"notify_enabled"`
	NotifyTime         string                     `json:"notify_time"`
	NotifyIntervalDays int                        `json:"notify_interval_days"`
	NotifyUserIDs      []int64                    `json:"notify_user_ids"`
	LastSentAt         *string                    `json:"last_sent_at"`
	NextSendAt         *string                    `json:"next_send_at"`
	Recipients         []NotificationRecipientDTO `json:"recipients"`
}

// notificationSettingsInput mirrors NotificationSettingsInput of the design
// (absent fields are left unchanged; an empty password is kept).
type notificationSettingsInput struct {
	SMTPHost           *string  `json:"smtp_host"`
	SMTPPort           *int     `json:"smtp_port"`
	SMTPSecurity       *string  `json:"smtp_security"`
	SMTPUsername       *string  `json:"smtp_username"`
	SMTPPassword       *string  `json:"smtp_password"`
	SMTPFrom           *string  `json:"smtp_from"`
	PublicBaseURL      *string  `json:"public_base_url"`
	NotifyEnabled      *bool    `json:"notify_enabled"`
	NotifyTime         *string  `json:"notify_time"`
	NotifyIntervalDays *int     `json:"notify_interval_days"`
	NotifyUserIDs      *[]int64 `json:"notify_user_ids"`
}

// notificationSettingsDTO builds the DTO from the saved settings.
func (c *core) notificationSettingsDTO() (*NotificationSettingsDTO, error) {
	s, err := modules.ResolveNotificationSettings(c.db, nil)
	if err != nil {
		return nil, err
	}
	users, err := models.ListUsers(c.db)
	if err != nil {
		return nil, err
	}
	recipients := make([]NotificationRecipientDTO, 0, len(users))
	for _, u := range users {
		recipients = append(recipients, NotificationRecipientDTO{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email})
	}
	ids := s.UserIDs
	if ids == nil {
		ids = []int64{}
	}
	dto := &NotificationSettingsDTO{
		SMTPHost: s.SMTP.Host, SMTPPort: s.SMTP.Port, SMTPSecurity: s.SMTP.Security, SMTPUsername: s.SMTP.Username,
		SMTPPasswordSet: s.SMTPPasswordSet, SMTPFrom: s.SMTP.From, PublicBaseURL: s.PublicBaseURL,
		EffectiveBaseURL: modules.BaseURLFor(s, c.webListen, c.webPort), NotifyEnabled: s.Enabled, NotifyTime: s.Time,
		NotifyIntervalDays: s.IntervalDays, NotifyUserIDs: ids, LastSentAt: nullTimeString(s.LastSentAt), Recipients: recipients,
	}
	if next, ok := modules.NextNotifyAt(time.Now(), s); ok {
		str := timeString(next)
		dto.NextSendAt = &str
	}
	return dto, nil
}

func (c *core) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	dto, err := c.notificationSettingsDTO()
	if err != nil {
		writeInternalError(w, "failed to load the notification settings", err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (c *core) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var body notificationSettingsInput
	if !decodeJSON(w, r, &body) {
		return
	}
	in := &modules.NotificationInput{
		SMTPHost: body.SMTPHost, SMTPPort: body.SMTPPort, SMTPSecurity: body.SMTPSecurity, SMTPUsername: body.SMTPUsername,
		SMTPPassword: body.SMTPPassword, SMTPFrom: body.SMTPFrom, PublicBaseURL: body.PublicBaseURL, Enabled: body.NotifyEnabled,
		Time: body.NotifyTime, IntervalDays: body.NotifyIntervalDays, UserIDs: body.NotifyUserIDs,
	}
	if err := modules.SaveNotificationSettings(c.db, c.key, in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dto, err := c.notificationSettingsDTO()
	if err != nil {
		writeInternalError(w, "failed to load the notification settings", err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// notificationTestInput is the body of POST /api/v1/notifications/test: the
// recipient (default: the caller's own address) and, optionally, connection
// values to try instead of the saved ones.
type notificationTestInput struct {
	To           string  `json:"to"`
	SMTPHost     *string `json:"smtp_host"`
	SMTPPort     *int    `json:"smtp_port"`
	SMTPSecurity *string `json:"smtp_security"`
	SMTPUsername *string `json:"smtp_username"`
	SMTPPassword *string `json:"smtp_password"`
	SMTPFrom     *string `json:"smtp_from"`
}

// handleTestNotification sends the SMTP test mail to the given address
// (default: the caller's own address) and reports the SMTP error text. The
// stored password is used only when the host, port, security mode and
// username are the saved ones; a request that changes one of them must
// carry smtp_password (400 otherwise), so the stored password is never
// relayed to another server.
func (c *core) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var body notificationTestInput
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	to := body.To
	if to == "" {
		to = userFrom(r).Email
	}
	if to == "" {
		writeError(w, http.StatusBadRequest, "no recipient: set your notification address or pass \"to\"")
		return
	}
	cfg, err := modules.SMTPConfigForTest(c.db, c.key, &modules.SMTPTestInput{
		Host: body.SMTPHost, Port: body.SMTPPort, Security: body.SMTPSecurity, Username: body.SMTPUsername,
		Password: body.SMTPPassword, From: body.SMTPFrom,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := modules.TestSMTP(r.Context(), cfg, to); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSendNotification queues the notify job (the mail is sent by the job
// manager; its result records a skipped reason when a send condition fails).
func (c *core) handleSendNotification(w http.ResponseWriter, r *http.Request) {
	c.enqueueAndRespond(w, r, modules.JobKindNotify, 0, "")
}
