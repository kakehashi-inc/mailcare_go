package workers

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules"
)

// Delivery probe (the system design document (Documents) 7.6, 9.2). The
// probe settings and the send / check actions are for administrators; the
// state of the probe mails (the summary and the list) is shown to every
// user, without the connection of the probe server, and every user can
// resolve a probe mail by hand, like the state of an alert.

const (
	defaultProbePage = 50
	maxProbePage     = 200
	// probeRecentDays is how far back the counts of recovered and resolved
	// probe mails reach.
	probeRecentDays = 7
)

// ProbeSettingsDTO is the payload of GET/PUT /api/v1/settings/probe.
type ProbeSettingsDTO struct {
	Enabled         bool     `json:"enabled"`
	Address         string   `json:"address"`
	IMAPHost        string   `json:"imap_host"`
	IMAPPort        int      `json:"imap_port"`
	IMAPSecurity    string   `json:"imap_security"`
	IMAPUsername    string   `json:"imap_username"`
	IMAPPasswordSet bool     `json:"imap_password_set"`
	IMAPFolder      string   `json:"imap_folder"`
	SendTimes       []string `json:"send_times"`
	DelayMinutes    int      `json:"delay_minutes"`
	FailureMinutes  int      `json:"failure_minutes"`
	// Mailboxes is the number of enabled mailboxes with an SMTP
	// server (the ones that send probe mails).
	Mailboxes int `json:"mailboxes"`
}

// probeSettingsInput mirrors modules.ProbeInput (absent fields are left
// unchanged; an empty password is kept).
type probeSettingsInput struct {
	Enabled        *bool     `json:"enabled"`
	Address        *string   `json:"address"`
	IMAPHost       *string   `json:"imap_host"`
	IMAPPort       *int      `json:"imap_port"`
	IMAPSecurity   *string   `json:"imap_security"`
	IMAPUsername   *string   `json:"imap_username"`
	IMAPPassword   *string   `json:"imap_password"`
	IMAPFolder     *string   `json:"imap_folder"`
	SendTimes      *[]string `json:"send_times"`
	DelayMinutes   *int      `json:"delay_minutes"`
	FailureMinutes *int      `json:"failure_minutes"`
}

// ProbeMailDTO is one probe mail.
type ProbeMailDTO struct {
	ID            int64   `json:"id"`
	MailboxID     int64   `json:"mailbox_id"`
	Address       string  `json:"address"`
	Token         string  `json:"token"`
	Slot          string  `json:"slot"`
	Manual        bool    `json:"manual"`
	Subject       string  `json:"subject"`
	SentAt        string  `json:"sent_at"`
	Status        string  `json:"status"`
	Level         string  `json:"level"`
	DelaySeconds  int64   `json:"delay_seconds"`
	ArrivedAt     *string `json:"arrived_at"`
	LastCheckedAt *string `json:"last_checked_at"`
	ErrorMessage  string  `json:"error_message"`
	NotifiedAt    *string `json:"notified_at"`
	ResolvedAt    *string `json:"resolved_at"`
	ResolvedBy    string  `json:"resolved_by"`
	ResolveReason string  `json:"resolve_reason"`
	ResolveNote   string  `json:"resolve_note"`
}

func toProbeMailDTO(p *models.ProbeMail) ProbeMailDTO {
	return ProbeMailDTO{
		ID: p.ID, MailboxID: p.MailboxID, Address: p.Address, Token: p.Token, Slot: p.Slot, Manual: p.Manual,
		Subject: p.Subject,
		SentAt:  timeString(p.SentAt), Status: p.Status, Level: p.Level, DelaySeconds: p.DelaySeconds,
		ArrivedAt: nullTimeString(p.ArrivedAt), LastCheckedAt: nullTimeString(p.LastCheckedAt),
		ErrorMessage: p.ErrorMessage, NotifiedAt: nullTimeString(p.NotifiedAt), ResolvedAt: nullTimeString(p.ResolvedAt),
		ResolvedBy: p.ResolvedBy, ResolveReason: p.ResolveReason, ResolveNote: p.ResolveNote,
	}
}

func toProbeMailDTOs(list []*models.ProbeMail) []ProbeMailDTO {
	out := make([]ProbeMailDTO, 0, len(list))
	for _, p := range list {
		out = append(out, toProbeMailDTO(p))
	}
	return out
}

// ProbeMailboxDTO is the probe state of one mailbox: whether it sends probe
// mails and its newest probe mail.
type ProbeMailboxDTO struct {
	MailboxID   int64         `json:"mailbox_id"`
	Address     string        `json:"address"`
	DisplayName string        `json:"display_name"`
	Sending     bool          `json:"sending"`
	Latest      *ProbeMailDTO `json:"latest"`
}

// ProbeSummaryDTO is the state of the delivery probe.
type ProbeSummaryDTO struct {
	Enabled        bool               `json:"enabled"`
	Configured     bool               `json:"configured"`
	SendTimes      []string           `json:"send_times"`
	DelayMinutes   int                `json:"delay_minutes"`
	FailureMinutes int                `json:"failure_minutes"`
	NextSendAt     *string            `json:"next_send_at"`
	NextCheckAt    *string            `json:"next_check_at"`
	LastSendAt     *string            `json:"last_send_at"`
	LastCheckAt    *string            `json:"last_check_at"`
	LastCheckError string             `json:"last_check_error"`
	Counts         models.ProbeCounts `json:"counts"`
	RecentDays     int                `json:"recent_days"`
	Mailboxes      []ProbeMailboxDTO  `json:"mailboxes"`
}

// probeSummary builds the probe state. The error of the latest check names
// the probe server and is given to administrators only.
func (c *core) probeSummary(u *models.User) (ProbeSummaryDTO, error) {
	s, err := modules.ResolveProbeSettings(c.db, nil)
	if err != nil {
		return ProbeSummaryDTO{}, err
	}
	dto := ProbeSummaryDTO{
		Enabled: s.Enabled, Configured: s.Configured(), SendTimes: s.SendTimes, DelayMinutes: s.DelayMinutes,
		FailureMinutes: s.FailureMinutes, LastSendAt: nullTimeString(s.LastSendAt), LastCheckAt: nullTimeString(s.LastCheckAt),
		RecentDays: probeRecentDays, Mailboxes: []ProbeMailboxDTO{},
	}
	if u != nil && u.Role == modules.RoleAdmin {
		dto.LastCheckError = s.LastCheckError
	} else if s.LastCheckError != "" {
		dto.LastCheckError = "error"
	}
	if s.Enabled && s.Configured() {
		if next, ok := modules.NextCheckAt(time.Now(), s.SendTimes); ok {
			str := timeString(next)
			dto.NextSendAt = &str
		}
		dto.NextCheckAt = nullTimeString(s.NextCheckAt)
	}
	if dto.Counts, err = models.CountProbeMails(c.db, 0, time.Now().AddDate(0, 0, -probeRecentDays)); err != nil {
		return dto, err
	}
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		return dto, err
	}
	latest, err := models.LatestProbeMails(c.db)
	if err != nil {
		return dto, err
	}
	for _, mb := range mailboxes {
		sending := modules.ProbeParticipates(mb)
		p := latest[mb.ID]
		if !sending && p == nil {
			continue
		}
		row := ProbeMailboxDTO{MailboxID: mb.ID, Address: mb.Address, DisplayName: mb.DisplayName, Sending: sending}
		if p != nil {
			d := toProbeMailDTO(p)
			row.Latest = &d
		}
		dto.Mailboxes = append(dto.Mailboxes, row)
	}
	return dto, nil
}

// probeSettingsDTO builds the DTO from the saved settings.
func (c *core) probeSettingsDTO() (*ProbeSettingsDTO, error) {
	s, err := modules.ResolveProbeSettings(c.db, nil)
	if err != nil {
		return nil, err
	}
	mailboxes, err := models.ListMailboxes(c.db)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, mb := range mailboxes {
		if modules.ProbeParticipates(mb) {
			n++
		}
	}
	return &ProbeSettingsDTO{
		Enabled: s.Enabled, Address: s.Address, IMAPHost: s.Conn.ImapHost, IMAPPort: s.Conn.ImapPort,
		IMAPSecurity: s.Conn.ImapSecurity, IMAPUsername: s.Conn.ImapUsername, IMAPPasswordSet: s.PasswordSet,
		IMAPFolder: s.Conn.Folder, SendTimes: s.SendTimes, DelayMinutes: s.DelayMinutes, FailureMinutes: s.FailureMinutes,
		Mailboxes: n,
	}, nil
}

func (c *core) handleGetProbeSettings(w http.ResponseWriter, r *http.Request) {
	dto, err := c.probeSettingsDTO()
	if err != nil {
		writeInternalError(w, "failed to load the probe settings", err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (c *core) handleUpdateProbeSettings(w http.ResponseWriter, r *http.Request) {
	var body probeSettingsInput
	if !decodeJSON(w, r, &body) {
		return
	}
	in := &modules.ProbeInput{
		Enabled: body.Enabled, Address: body.Address, IMAPHost: body.IMAPHost, IMAPPort: body.IMAPPort,
		IMAPSecurity: body.IMAPSecurity, IMAPUsername: body.IMAPUsername, IMAPPassword: body.IMAPPassword,
		IMAPFolder: body.IMAPFolder, SendTimes: body.SendTimes, DelayMinutes: body.DelayMinutes,
		FailureMinutes: body.FailureMinutes,
	}
	if err := modules.SaveProbeSettings(c.db, c.key, in); err != nil {
		writeErrorMessage(w, r, err)
		return
	}
	dto, err := c.probeSettingsDTO()
	if err != nil {
		writeInternalError(w, "failed to load the probe settings", err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleTestProbeSettings tries the connection to the probe server with the
// saved settings and the values of the request (the stored password only
// for the saved connection).
func (c *core) handleTestProbeSettings(w http.ResponseWriter, r *http.Request) {
	var body probeSettingsInput
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	err := modules.TestProbeServer(r.Context(), c.db, c.key, &modules.ProbeTestInput{
		IMAPHost: body.IMAPHost, IMAPPort: body.IMAPPort, IMAPSecurity: body.IMAPSecurity,
		IMAPUsername: body.IMAPUsername, IMAPPassword: body.IMAPPassword, IMAPFolder: body.IMAPFolder,
	})
	if err != nil {
		writeErrorMessage(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTestMailboxProbe sends a test mail through the SMTP
// connection of a mailbox input to the probe address.
func (c *core) handleTestMailboxProbe(w http.ResponseWriter, r *http.Request) {
	var in modules.MailboxInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := modules.TestProbeSMTP(r.Context(), c.db, c.key, &in); err != nil {
		writeErrorMessage(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleListProbes answers the probe state and one page of the probe mails
// of a filter (open: still missing or not sent; issues: every mail that was
// delayed, failed or not sent; all), newest first, optionally of one
// mailbox (mailbox_id).
func (c *core) handleListProbes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := q.Get("filter")
	switch filter {
	case "":
		filter = models.ProbeFilterOpen
	case models.ProbeFilterOpen, models.ProbeFilterIssues, models.ProbeFilterAll:
	default:
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	var mailboxID int64
	if s := q.Get("mailbox_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "system.invalidRequest")
			return
		}
		mailboxID = id
	}
	page, perPage := 1, defaultProbePage
	for _, p := range []struct {
		name string
		dst  *int
		max  int
	}{{"page", &page, maxJobPage}, {"per_page", &perPage, maxProbePage}} {
		s := q.Get(p.name)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > p.max {
			writeError(w, http.StatusBadRequest, "system.invalidRequest")
			return
		}
		*p.dst = n
	}
	list, total, err := models.ListProbeMails(c.db, filter, mailboxID, (page-1)*perPage, perPage)
	if err != nil {
		writeInternalError(w, "failed to list the probe mails", err)
		return
	}
	summary, err := c.probeSummary(userFrom(r))
	if err != nil {
		writeInternalError(w, "failed to load the probe state", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": summary, "probes": toProbeMailDTOs(list), "total": total, "page": page, "per_page": perPage,
		"filter": filter, "reasons": models.ProbeResolveReasons,
	})
}

// probeResolveInput is the body of PUT /api/v1/probes/{id}/resolve.
type probeResolveInput struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// handleResolveProbe closes an open probe mail by hand with a reason (every
// user, like the state of an alert).
func (c *core) handleResolveProbe(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "system.invalidRequest")
		return
	}
	var body probeResolveInput
	if !decodeJSON(w, r, &body) {
		return
	}
	by := ""
	if u := userFrom(r); u != nil {
		by = u.Username
	}
	p, err := modules.ResolveProbe(c.db, id, by, body.Reason, body.Note)
	if err != nil {
		writeErrorMessage(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"probe": toProbeMailDTO(p)})
}

// handleSendProbes queues a probe send now (administrators only); it is
// recorded without a send time.
func (c *core) handleSendProbes(w http.ResponseWriter, r *http.Request) {
	c.enqueueAndRespond(w, r, modules.JobKindProbeSend, 0, "")
}

// handleCheckProbes queues an arrival check now (administrators only).
func (c *core) handleCheckProbes(w http.ResponseWriter, r *http.Request) {
	c.enqueueAndRespond(w, r, modules.JobKindProbeCheck, 0, "")
}

// dashboardProbe returns the probe state for the dashboard; a failure is
// logged and yields the empty state so the dashboard still answers.
func (c *core) dashboardProbe(u *models.User) ProbeSummaryDTO {
	dto, err := c.probeSummary(u)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("failed to load the probe state: %v", err)
	}
	if dto.Mailboxes == nil {
		dto.Mailboxes = []ProbeMailboxDTO{}
	}
	if dto.SendTimes == nil {
		dto.SendTimes = []string{}
	}
	return dto
}
