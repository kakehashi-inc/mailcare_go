package workers

import (
	"database/sql"
	"time"

	"mailcare/app/models"
)

// Response DTOs (see Documents/システム設計書.md section 9.3). Times are
// RFC 3339 in UTC; nullable times are null.

func timeString(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func nullTimeString(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := timeString(t.Time)
	return &s
}

// UserDTO is a user as shown to the Web UI.
type UserDTO struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt *string `json:"last_login_at"`
}

func toUserDTO(u *models.User) UserDTO {
	return UserDTO{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role,
		CreatedAt: timeString(u.CreatedAt), LastLoginAt: nullTimeString(u.LastLoginAt)}
}

// TokenDTO is a login token without its secret value.
type TokenDTO struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Username   string  `json:"username"`
	Identifier string  `json:"identifier"`
	Name       string  `json:"name"`
	ExpiresAt  *string `json:"expires_at"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

func toTokenDTO(t *models.Token, username string) TokenDTO {
	return TokenDTO{ID: t.ID, UserID: t.UserID, Username: username, Identifier: t.Identifier, Name: t.Name,
		ExpiresAt: nullTimeString(t.ExpiresAt), CreatedAt: timeString(t.CreatedAt), LastUsedAt: nullTimeString(t.LastUsedAt)}
}

// MailboxStatsDTO summarizes the index of a mailbox.
type MailboxStatsDTO struct {
	Messages int                `json:"messages"`
	Bounces  int                `json:"bounces"`
	Groups   models.GroupCounts `json:"groups"`
}

// MailboxDTO is a mailbox without its password.
type MailboxDTO struct {
	ID              int64            `json:"id"`
	Address         string           `json:"address"`
	DisplayName     string           `json:"display_name"`
	ImapHost        string           `json:"imap_host"`
	ImapPort        int              `json:"imap_port"`
	ImapSecurity    string           `json:"imap_security"`
	ImapUsername    string           `json:"imap_username"`
	Folder          string           `json:"folder"`
	Enabled         bool             `json:"enabled"`
	InitialDays     int              `json:"initial_days"`
	RecentDays      int              `json:"recent_days"`
	LastCheckedAt   *string          `json:"last_checked_at"`
	LastCheckStatus string           `json:"last_check_status"`
	LastCheckError  string           `json:"last_check_error"`
	CreatedAt       string           `json:"created_at"`
	UpdatedAt       string           `json:"updated_at"`
	Stats           *MailboxStatsDTO `json:"stats,omitempty"`
}

func toMailboxDTO(mb *models.Mailbox) MailboxDTO {
	return MailboxDTO{
		ID: mb.ID, Address: mb.Address, DisplayName: mb.DisplayName, ImapHost: mb.ImapHost, ImapPort: mb.ImapPort,
		ImapSecurity: mb.ImapSecurity, ImapUsername: mb.ImapUsername, Folder: mb.Folder, Enabled: mb.Enabled,
		InitialDays: mb.InitialDays, RecentDays: mb.RecentDays, LastCheckedAt: nullTimeString(mb.LastCheckedAt),
		LastCheckStatus: mb.LastCheckStatus, LastCheckError: mb.LastCheckError,
		CreatedAt: timeString(mb.CreatedAt), UpdatedAt: timeString(mb.UpdatedAt),
	}
}

// GroupDTO is a bounce group with the headline of its latest report.
type GroupDTO struct {
	GroupKey           string  `json:"group_key"`
	Title              string  `json:"title"`
	BounceKind         string  `json:"bounce_kind"`
	RecipientDomain    string  `json:"recipient_domain"`
	StatusCode         string  `json:"status_code"`
	SMTPCode           string  `json:"smtp_code"`
	DiagnosticTemplate string  `json:"diagnostic_template"`
	Responsible        string  `json:"responsible"`
	MessageCount       int     `json:"message_count"`
	RecipientCount     int     `json:"recipient_count"`
	RemoteIPCount      int     `json:"remote_ip_count"`
	FirstSeen          *string `json:"first_seen"`
	LastSeen           *string `json:"last_seen"`
	State              string  `json:"state"`
	StateUpdatedAt     *string `json:"state_updated_at"`
	NeedsAnalysis      bool    `json:"needs_analysis"`
	ReportSummary      string  `json:"report_summary"`
	ReportSeverity     string  `json:"report_severity"`
	ReportStatus       string  `json:"report_status"`
}

// toGroupDTO converts a group. completed is the newest completed report (may
// be nil); latest is the newest report of any status (may be nil).
func toGroupDTO(g *models.BounceGroup, completed, latest *models.AgentReport) GroupDTO {
	dto := GroupDTO{
		GroupKey: g.GroupKey, Title: g.Title, BounceKind: g.BounceKind, RecipientDomain: g.RecipientDomain,
		StatusCode: g.StatusCode, SMTPCode: g.SMTPCode, DiagnosticTemplate: g.DiagnosticTemplate,
		Responsible: g.Responsible, MessageCount: g.MessageCount, RecipientCount: g.RecipientCount,
		RemoteIPCount: g.RemoteIPCount, FirstSeen: nullTimeString(g.FirstSeen), LastSeen: nullTimeString(g.LastSeen),
		State: g.State, StateUpdatedAt: nullTimeString(g.StateUpdatedAt), NeedsAnalysis: g.NeedsAnalysis,
	}
	if completed != nil {
		dto.ReportSummary = completed.Summary
		dto.ReportSeverity = completed.Severity
	}
	if latest != nil {
		dto.ReportStatus = latest.Status
	} else if completed != nil {
		dto.ReportStatus = completed.Status
	}
	return dto
}

// DashboardGroupDTO is a group annotated with its mailbox.
type DashboardGroupDTO struct {
	GroupDTO
	MailboxID      int64  `json:"mailbox_id"`
	MailboxAddress string `json:"mailbox_address"`
}

// ReportDTO is an agent report.
type ReportDTO struct {
	ID             int64   `json:"id"`
	GroupKey       string  `json:"group_key"`
	Provider       string  `json:"provider"`
	Status         string  `json:"status"`
	Summary        string  `json:"summary"`
	Responsible    string  `json:"responsible"`
	Severity       string  `json:"severity"`
	ReportMarkdown string  `json:"report_markdown"`
	ErrorMessage   string  `json:"error_message"`
	MessageCount   int     `json:"message_count"`
	StartedAt      *string `json:"started_at"`
	FinishedAt     *string `json:"finished_at"`
	CreatedAt      string  `json:"created_at"`
}

func toReportDTO(r *models.AgentReport) ReportDTO {
	return ReportDTO{
		ID: r.ID, GroupKey: r.GroupKey, Provider: r.Provider, Status: r.Status, Summary: r.Summary,
		Responsible: r.Responsible, Severity: r.Severity, ReportMarkdown: r.ReportMarkdown, ErrorMessage: r.ErrorMessage,
		MessageCount: r.MessageCount, StartedAt: nullTimeString(r.StartedAt), FinishedAt: nullTimeString(r.FinishedAt),
		CreatedAt: timeString(r.CreatedAt),
	}
}

// MessageDTO is an indexed message.
type MessageDTO struct {
	ID             int64   `json:"id"`
	MessageKey     string  `json:"message_key"`
	UID            uint32  `json:"uid"`
	Folder         string  `json:"folder"`
	MessageID      string  `json:"message_id"`
	Subject        string  `json:"subject"`
	FromAddress    string  `json:"from_address"`
	FromName       string  `json:"from_name"`
	ToAddress      string  `json:"to_address"`
	Date           *string `json:"date"`
	ReceivedAt     *string `json:"received_at"`
	Size           int64   `json:"size"`
	HasText        bool    `json:"has_text"`
	HasHTML        bool    `json:"has_html"`
	IsBounce       bool    `json:"is_bounce"`
	BounceKind     string  `json:"bounce_kind"`
	ClassifyReason string  `json:"classify_reason"`
	GroupKey       string  `json:"group_key"`
	FetchedAt      string  `json:"fetched_at"`
}

func toMessageDTO(m *models.Message) MessageDTO {
	return MessageDTO{
		ID: m.ID, MessageKey: m.MessageKey, UID: m.UID, Folder: m.Folder, MessageID: m.MessageID, Subject: m.Subject,
		FromAddress: m.FromAddress, FromName: m.FromName, ToAddress: m.ToAddress, Date: nullTimeString(m.Date),
		ReceivedAt: nullTimeString(m.ReceivedAt), Size: m.Size, HasText: m.HasText, HasHTML: m.HasHTML,
		IsBounce: m.IsBounce, BounceKind: m.BounceKind, ClassifyReason: m.ClassifyReason, GroupKey: m.GroupKey,
		FetchedAt: timeString(m.FetchedAt),
	}
}

func toMessageDTOs(list []*models.Message) []MessageDTO {
	out := make([]MessageDTO, 0, len(list))
	for _, m := range list {
		out = append(out, toMessageDTO(m))
	}
	return out
}

// BounceDTO is the extracted detail of a bounce message.
type BounceDTO struct {
	OriginalRecipient  string  `json:"original_recipient"`
	RecipientDomain    string  `json:"recipient_domain"`
	Action             string  `json:"action"`
	StatusCode         string  `json:"status_code"`
	SMTPCode           string  `json:"smtp_code"`
	Diagnostic         string  `json:"diagnostic"`
	DiagnosticTemplate string  `json:"diagnostic_template"`
	RemoteMTA          string  `json:"remote_mta"`
	RemoteIP           string  `json:"remote_ip"`
	ReportingMTA       string  `json:"reporting_mta"`
	OriginalMessageID  string  `json:"original_message_id"`
	OriginalSubject    string  `json:"original_subject"`
	OriginalFrom       string  `json:"original_from"`
	OriginalDate       *string `json:"original_date"`
	Responsible        string  `json:"responsible"`
}

func toBounceDTO(b *models.Bounce) *BounceDTO {
	if b == nil {
		return nil
	}
	return &BounceDTO{
		OriginalRecipient: b.OriginalRecipient, RecipientDomain: b.RecipientDomain, Action: b.Action,
		StatusCode: b.StatusCode, SMTPCode: b.SMTPCode, Diagnostic: b.Diagnostic, DiagnosticTemplate: b.DiagnosticTemplate,
		RemoteMTA: b.RemoteMTA, RemoteIP: b.RemoteIP, ReportingMTA: b.ReportingMTA, OriginalMessageID: b.OriginalMessageID,
		OriginalSubject: b.OriginalSubject, OriginalFrom: b.OriginalFrom, OriginalDate: nullTimeString(b.OriginalDate),
		Responsible: b.Responsible,
	}
}

// JobDTO is a job with its mailbox address resolved.
type JobDTO struct {
	ID             int64   `json:"id"`
	Kind           string  `json:"kind"`
	MailboxID      *int64  `json:"mailbox_id"`
	MailboxAddress string  `json:"mailbox_address"`
	Target         string  `json:"target"`
	Status         string  `json:"status"`
	Progress       string  `json:"progress"`
	Result         string  `json:"result"`
	ErrorMessage   string  `json:"error_message"`
	RequestedBy    string  `json:"requested_by"`
	CreatedAt      string  `json:"created_at"`
	StartedAt      *string `json:"started_at"`
	FinishedAt     *string `json:"finished_at"`
}

func toJobDTO(j *models.Job, addresses map[int64]string) JobDTO {
	dto := JobDTO{
		ID: j.ID, Kind: j.Kind, Target: j.Target, Status: j.Status, Progress: j.Progress, Result: j.Result,
		ErrorMessage: j.ErrorMessage, RequestedBy: j.RequestedBy, CreatedAt: timeString(j.CreatedAt),
		StartedAt: nullTimeString(j.StartedAt), FinishedAt: nullTimeString(j.FinishedAt),
	}
	if j.MailboxID.Valid {
		id := j.MailboxID.Int64
		dto.MailboxID = &id
		dto.MailboxAddress = addresses[id]
	}
	return dto
}

func toJobDTOs(list []*models.Job, addresses map[int64]string) []JobDTO {
	out := make([]JobDTO, 0, len(list))
	for _, j := range list {
		out = append(out, toJobDTO(j, addresses))
	}
	return out
}

// DashboardDTO aggregates the state of every mailbox.
type DashboardDTO struct {
	Mailboxes    []MailboxDTO        `json:"mailboxes"`
	Totals       DashboardTotalsDTO  `json:"totals"`
	RecentGroups []DashboardGroupDTO `json:"recent_groups"`
	ActiveJobs   []JobDTO            `json:"active_jobs"`
	RecentJobs   []JobDTO            `json:"recent_jobs"`
	NextCheckAt  *string             `json:"next_check_at"`
	CheckTimes   []string            `json:"check_times"`
	Agent        DashboardAgentDTO   `json:"agent"`
}

// DashboardTotalsDTO sums the per-mailbox counters.
type DashboardTotalsDTO struct {
	Mailboxes  int `json:"mailboxes"`
	OpenGroups int `json:"open_groups"`
	Bounces    int `json:"bounces"`
	Messages   int `json:"messages"`
}

// DashboardAgentDTO describes the configured agent.
type DashboardAgentDTO struct {
	Provider  string `json:"provider"`
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
}
