package modules

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

// Notification mails (the system design document (Documents) 7.4).
//
// The settings live in the settings table (SMTP connection, recipients,
// time, interval, public URL). A notification collects, from the index of
// every mailbox, the actionable open groups that have a completed agent
// report and sends one text/plain mail (rendered from
// templates/mail/notification_ja.txt) to every selected user that has a
// notification address. The scheduler queues a notify job when the notify
// time arrives and the interval since the last mail has elapsed.

const (
	// notifyTemplateFile / testTemplateFile / categoryLabelsFile are the
	// mail templates inside TemplatesFS. All user-facing text of the
	// mails lives there, never in Go source.
	notifyTemplateFile  = "templates/mail/notification_ja.txt"
	testTemplateFile    = "templates/mail/test_ja.txt"
	categoryLabelsFile  = "templates/mail/categories_ja.txt"
	notifyTestPrefix    = "test:" // notify job target for a test mail
	notifyMaxRecipients = 100
)

// NotificationSettings is the resolved notification configuration.
type NotificationSettings struct {
	SMTP            SMTPConfig // Password is filled only when resolved with the master key
	SMTPPasswordSet bool
	PublicBaseURL   string
	Enabled         bool
	Time            string // HH:MM local
	IntervalDays    int
	UserIDs         []int64
	LastSentAt      sql.NullTime
}

// ResolveNotificationSettings reads the notification settings (saved >
// default). key decrypts the SMTP password; nil leaves it empty.
func ResolveNotificationSettings(db *sql.DB, key []byte) (*NotificationSettings, error) {
	s := &NotificationSettings{
		SMTP: SMTPConfig{
			Host:     models.GetSetting(db, SettingSMTPHost),
			Port:     resolveInt(db, SettingSMTPPort, 0, DefaultSMTPPort),
			Security: resolveStr(db, SettingSMTPSecurity, "", DefaultSMTPSecurity),
			Username: models.GetSetting(db, SettingSMTPUsername),
			From:     models.GetSetting(db, SettingSMTPFrom),
		},
		PublicBaseURL: models.GetSetting(db, SettingPublicBaseURL),
		Enabled:       models.GetSetting(db, SettingNotifyEnabled) == "1",
		Time:          resolveStr(db, SettingNotifyTime, "", DefaultNotifyTime),
		IntervalDays:  resolveInt(db, SettingNotifyInterval, 0, DefaultNotifyIntervalDays),
		UserIDs:       ParseNotifyUserIDs(models.GetSetting(db, SettingNotifyUserIDs)),
	}
	if ValidateSMTPSecurity(s.SMTP.Security) != nil {
		s.SMTP.Security = DefaultSMTPSecurity
	}
	if _, err := ValidateNotifyTime(s.Time); err != nil {
		s.Time = DefaultNotifyTime
	}
	if ValidateNotifyInterval(s.IntervalDays) != nil {
		s.IntervalDays = DefaultNotifyIntervalDays
	}
	if enc := models.GetSetting(db, SettingSMTPPasswordEnc); enc != "" {
		s.SMTPPasswordSet = true
		if key != nil {
			password, err := DecryptSecret(key, enc)
			if err != nil {
				return nil, fmt.Errorf("SMTP password: %w", err)
			}
			s.SMTP.Password = password
		}
	}
	if raw := models.GetSetting(db, SettingNotifyLastSent); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			s.LastSentAt = sql.NullTime{Time: t, Valid: true}
		}
	}
	return s, nil
}

// ParseNotifyUserIDs parses the comma-separated user id list (invalid
// entries are dropped, duplicates removed, ascending).
func ParseNotifyUserIDs(s string) []int64 {
	seen := map[int64]bool{}
	out := []int64{} // never nil so that JSON output shows [] rather than null
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FormatNotifyUserIDs joins user ids for storage.
func FormatNotifyUserIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

// ValidateNotifyTime checks one HH:MM time and returns it normalized.
func ValidateNotifyTime(s string) (string, error) {
	times, err := ParseCheckTimes([]string{s})
	if err != nil || len(times) != 1 {
		return "", fmt.Errorf("invalid notify_time %q (use one HH:MM time, 00:00-23:59)", strings.TrimSpace(s))
	}
	return times[0], nil
}

// ValidateNotifyInterval checks the interval in days.
func ValidateNotifyInterval(n int) error {
	if n < MinNotifyIntervalDays || n > MaxNotifyIntervalDays {
		return fmt.Errorf("notify_interval_days must be between %d and %d", MinNotifyIntervalDays, MaxNotifyIntervalDays)
	}
	return nil
}

// ValidateNotifyUserIDs checks that every id names an existing user and
// returns the deduplicated, sorted list.
func ValidateNotifyUserIDs(db *sql.DB, ids []int64) ([]int64, error) {
	if len(ids) > notifyMaxRecipients {
		return nil, fmt.Errorf("at most %d recipients can be selected", notifyMaxRecipients)
	}
	seen := map[int64]bool{}
	var out []int64
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid user id %d", id)
		}
		if seen[id] {
			continue
		}
		if _, err := models.GetUserByID(db, id); err == sql.ErrNoRows {
			return nil, fmt.Errorf("user %d not found", id)
		} else if err != nil {
			return nil, err
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// NormalizePublicBaseURL trims the URL and its trailing slash and checks
// that it is empty or an absolute http(s) URL.
func NormalizePublicBaseURL(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("public_base_url must be an absolute http or https URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("public_base_url must not carry a query or fragment")
	}
	return s, nil
}

// NotificationInput carries the fields of a settings update; nil fields are
// left unchanged. An empty SMTPPassword also leaves the password unchanged.
type NotificationInput struct {
	SMTPHost      *string
	SMTPPort      *int
	SMTPSecurity  *string
	SMTPUsername  *string
	SMTPPassword  *string
	SMTPFrom      *string
	PublicBaseURL *string
	Enabled       *bool
	Time          *string
	IntervalDays  *int
	UserIDs       *[]int64
}

// SaveNotificationSettings validates every given field and persists it
// (values equal to the code default delete the row). key is needed only
// when a password is given. While a password is stored, an update that
// changes the host, port, security mode or username must carry the password
// too (ErrSMTPPasswordRequired otherwise; nothing is saved): the stored
// password serves the server it was saved for and is never pointed at
// another one.
func SaveNotificationSettings(db *sql.DB, key []byte, in *NotificationInput) error {
	type write struct {
		key, value string
		isDefault  bool
	}
	var writes []write
	if in.SMTPHost != nil {
		host := strings.TrimSpace(*in.SMTPHost)
		if strings.ContainsAny(host, " /\\@") {
			return errors.New("smtp_host must be a host name or IP address")
		}
		writes = append(writes, write{SettingSMTPHost, host, host == ""})
	}
	if in.SMTPPort != nil {
		if *in.SMTPPort < 1 || *in.SMTPPort > 65535 {
			return errors.New("smtp_port must be between 1 and 65535")
		}
		writes = append(writes, write{SettingSMTPPort, strconv.Itoa(*in.SMTPPort), *in.SMTPPort == DefaultSMTPPort})
	}
	if in.SMTPSecurity != nil {
		sec := strings.ToLower(strings.TrimSpace(*in.SMTPSecurity))
		if sec == "" {
			sec = DefaultSMTPSecurity
		}
		if err := ValidateSMTPSecurity(sec); err != nil {
			return err
		}
		writes = append(writes, write{SettingSMTPSecurity, sec, sec == DefaultSMTPSecurity})
	}
	if in.SMTPUsername != nil {
		name := strings.TrimSpace(*in.SMTPUsername)
		writes = append(writes, write{SettingSMTPUsername, name, name == ""})
	}
	if in.SMTPPassword != nil && *in.SMTPPassword != "" {
		if key == nil {
			return errors.New("the master key is required to store the SMTP password")
		}
		enc, err := EncryptSecret(key, *in.SMTPPassword)
		if err != nil {
			return err
		}
		writes = append(writes, write{SettingSMTPPasswordEnc, enc, false})
	}
	if in.SMTPFrom != nil {
		from, err := NormalizeEmail(*in.SMTPFrom)
		if err != nil {
			return fmt.Errorf("smtp_from: %w", err)
		}
		writes = append(writes, write{SettingSMTPFrom, from, from == ""})
	}
	if in.PublicBaseURL != nil {
		base, err := NormalizePublicBaseURL(*in.PublicBaseURL)
		if err != nil {
			return err
		}
		writes = append(writes, write{SettingPublicBaseURL, base, base == ""})
	}
	if in.Enabled != nil {
		writes = append(writes, write{SettingNotifyEnabled, "1", !*in.Enabled})
	}
	if in.Time != nil {
		t := strings.TrimSpace(*in.Time)
		if t == "" {
			t = DefaultNotifyTime
		}
		norm, err := ValidateNotifyTime(t)
		if err != nil {
			return err
		}
		writes = append(writes, write{SettingNotifyTime, norm, norm == DefaultNotifyTime})
	}
	if in.IntervalDays != nil {
		if err := ValidateNotifyInterval(*in.IntervalDays); err != nil {
			return err
		}
		writes = append(writes, write{SettingNotifyInterval, strconv.Itoa(*in.IntervalDays), *in.IntervalDays == DefaultNotifyIntervalDays})
	}
	if in.UserIDs != nil {
		ids, err := ValidateNotifyUserIDs(db, *in.UserIDs)
		if err != nil {
			return err
		}
		value := FormatNotifyUserIDs(ids)
		writes = append(writes, write{SettingNotifyUserIDs, value, value == ""})
	}
	if in.SMTPPassword == nil || *in.SMTPPassword == "" {
		saved, err := ResolveNotificationSettings(db, nil)
		if err != nil {
			return err
		}
		if saved.SMTPPasswordSet && smtpConnectionChanged(saved, in) {
			return ErrSMTPPasswordRequired
		}
	}
	for _, w := range writes {
		if err := PersistSetting(db, w.key, w.value, w.isDefault); err != nil {
			return err
		}
	}
	return nil
}

// smtpConnectionChanged reports whether in changes the SMTP host, port,
// security mode or username away from the saved values (compared after the
// same normalization the update applies).
func smtpConnectionChanged(saved *NotificationSettings, in *NotificationInput) bool {
	if in.SMTPHost != nil && strings.TrimSpace(*in.SMTPHost) != saved.SMTP.Host {
		return true
	}
	if in.SMTPPort != nil && *in.SMTPPort != saved.SMTP.Port {
		return true
	}
	if in.SMTPSecurity != nil {
		sec := strings.ToLower(strings.TrimSpace(*in.SMTPSecurity))
		if sec == "" {
			sec = DefaultSMTPSecurity
		}
		if sec != saved.SMTP.Security {
			return true
		}
	}
	if in.SMTPUsername != nil && strings.TrimSpace(*in.SMTPUsername) != saved.SMTP.Username {
		return true
	}
	return false
}

// SetNotifyLastSent records the time of the last notification mail.
func SetNotifyLastSent(db *sql.DB, t time.Time) error {
	return models.SetSetting(db, SettingNotifyLastSent, t.UTC().Format(time.RFC3339))
}

// EffectiveBaseURL is the public URL used for links: public_base_url when
// set, otherwise http://<saved web_listen or localhost>:<saved web_port>.
// The running server uses BaseURLFor with its actual listen address.
func EffectiveBaseURL(db *sql.DB, s *NotificationSettings) string {
	return BaseURLFor(s, ResolveWebListen(db, ""), ResolveWebPort(db, 0))
}

// BaseURLFor is EffectiveBaseURL for a known listen address and port.
func BaseURLFor(s *NotificationSettings, webListen string, webPort int) string {
	if s != nil && s.PublicBaseURL != "" {
		return s.PublicBaseURL
	}
	host := webListen
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if webPort <= 0 {
		webPort = DefaultWebPort
	}
	return fmt.Sprintf("http://%s:%d", host, webPort)
}

// NotificationRecipients returns the selected users and the addresses of
// those among them that have one (sorted by username, deduplicated).
func NotificationRecipients(db *sql.DB, s *NotificationSettings) (selected []*models.User, addresses []string, err error) {
	selected, err = models.ListUsersByIDs(db, s.UserIDs)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, u := range selected {
		if u.Email == "" || seen[u.Email] {
			continue
		}
		seen[u.Email] = true
		addresses = append(addresses, u.Email)
	}
	return selected, addresses, nil
}

// --- Timing ---

// notifyTimeOfDay parses HH:MM.
func notifyTimeOfDay(s string) (h, m int, ok bool) {
	hh, mm, found := strings.Cut(s, ":")
	if !found {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return h, m, true
}

// localDateOf returns the local calendar day of t at 00:00.
func localDateOf(t time.Time) time.Time {
	t = t.In(time.Local)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

// daysBetween counts the calendar days from a to b (both local dates).
func daysBetween(a, b time.Time) int {
	return int(localDateOf(b).Sub(localDateOf(a)).Round(24*time.Hour).Hours() / 24)
}

// NotifyDue decides whether the scheduler should queue a notification at
// now: notifications are enabled, an occurrence of the notify time lies in
// (last, now] and was not fired yet (lastFired is the "YYYY-MM-DD HH:MM" of
// the previous firing, so the same minute never fires twice), and the
// calendar days since the last sent mail reach the interval (no last mail:
// the first occurrence fires). It returns the occurrence key to remember.
func NotifyDue(last, now time.Time, s *NotificationSettings, lastFired string) (string, bool) {
	if s == nil || !s.Enabled {
		return "", false
	}
	h, m, ok := notifyTimeOfDay(s.Time)
	if !ok {
		return "", false
	}
	now = now.In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
	for _, occ := range []time.Time{today.AddDate(0, 0, -1), today} {
		if !occ.After(last) || occ.After(now) {
			continue
		}
		key := occ.Format("2006-01-02 15:04")
		if key == lastFired {
			continue
		}
		if s.LastSentAt.Valid && daysBetween(s.LastSentAt.Time, occ) < s.IntervalDays {
			continue
		}
		return key, true
	}
	return "", false
}

// NextNotifyAt returns the next time a notification will be sent (false
// when notifications are disabled).
func NextNotifyAt(now time.Time, s *NotificationSettings) (time.Time, bool) {
	if s == nil || !s.Enabled {
		return time.Time{}, false
	}
	h, m, ok := notifyTimeOfDay(s.Time)
	if !ok {
		return time.Time{}, false
	}
	now = now.In(time.Local)
	cand := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
	if s.LastSentAt.Valid {
		d := localDateOf(s.LastSentAt.Time).AddDate(0, 0, s.IntervalDays)
		earliest := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, time.Local)
		if earliest.After(cand) {
			cand = earliest
		}
	}
	for !cand.After(now) {
		cand = cand.AddDate(0, 0, 1)
	}
	return cand, true
}

// --- Content ---

// NotificationGroup is one block of the mail.
type NotificationGroup struct {
	GroupKey       string
	Category       string
	CategoryLabel  string
	UnitValue      string
	Authority      string
	MessageCount   int
	RecipientCount int
	FirstSeen      string // local "2006-01-02 15:04" ("" when unknown)
	LastSeen       string
	Summary        string
	URL            string
}

// NotificationMailbox is the heading of one mail address.
type NotificationMailbox struct {
	ID          int64
	Address     string
	DisplayName string
	Groups      []NotificationGroup
}

// Notification is the rendered mail and the data it was built from.
type Notification struct {
	Date      string // YYYY-MM-DD (local)
	Total     int
	BaseURL   string
	Mailboxes []NotificationMailbox
	Subject   string
	Body      string
}

// BuildNotification collects, from the index of every mailbox, the
// actionable open groups that have a completed agent report and renders the
// mail. Mailboxes without an index are skipped; a mailbox whose index cannot
// be opened is logged and skipped. Total is 0 when there is nothing to send.
func BuildNotification(ctx context.Context, db *sql.DB, mailsRoot, baseURL string, now time.Time) (*Notification, error) {
	n := &Notification{Date: now.In(time.Local).Format("2006-01-02"), BaseURL: strings.TrimRight(baseURL, "/")}
	labels, err := loadCategoryLabels()
	if err != nil {
		return nil, err
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		return nil, err
	}
	sort.Slice(mailboxes, func(i, j int) bool { return mailboxes[i].Address < mailboxes[j].Address })
	for _, mb := range mailboxes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, err := os.Stat(mailengine.MailboxIndexPath(mailsRoot, mb.Address)); err != nil {
			continue
		}
		groups, err := collectNotificationGroups(ctx, mailsRoot, mb, n.BaseURL, labels)
		if err != nil {
			log.Printf("notification: %s skipped: %v", mb.Address, err)
			continue
		}
		if len(groups) == 0 {
			continue
		}
		n.Mailboxes = append(n.Mailboxes, NotificationMailbox{ID: mb.ID, Address: mb.Address, DisplayName: mb.DisplayName, Groups: groups})
		n.Total += len(groups)
	}
	if err := renderNotification(n); err != nil {
		return nil, err
	}
	return n, nil
}

// collectNotificationGroups reads the open actionable groups with a
// completed report from the index of one mailbox.
func collectNotificationGroups(ctx context.Context, mailsRoot string, mb *models.Mailbox, baseURL string, labels map[string]string) ([]NotificationGroup, error) {
	idx, err := mailengine.OpenIndex(ctx, mailsRoot, mb.Address, nil)
	if err != nil {
		return nil, err
	}
	defer idx.Close()
	actionable := true
	groups, err := models.ListGroups(idx, models.GroupFilter{State: GroupStateOpen, Actionable: &actionable})
	if err != nil {
		return nil, err
	}
	reports, err := models.LatestCompletedAgentReports(idx)
	if err != nil {
		return nil, err
	}
	var out []NotificationGroup
	for _, g := range groups {
		report := reports[g.GroupKey]
		if report == nil {
			continue
		}
		summary := strings.TrimSpace(report.Summary)
		if summary == "" {
			summary = firstParagraph(report.ReportMarkdown)
		}
		label := labels[g.Category]
		if label == "" {
			label = g.Category
		}
		out = append(out, NotificationGroup{
			GroupKey: g.GroupKey, Category: g.Category, CategoryLabel: label, UnitValue: g.UnitValue, Authority: g.Authority,
			MessageCount: g.MessageCount, RecipientCount: g.RecipientCount,
			FirstSeen: localStamp(g.FirstSeen), LastSeen: localStamp(g.LastSeen), Summary: summary,
			URL: GroupURL(baseURL, mb.ID, g.GroupKey),
		})
	}
	return out, nil
}

// GroupURL is the link to a group in the Web UI.
func GroupURL(baseURL string, mailboxID int64, groupKey string) string {
	return fmt.Sprintf("%s/alerts/%d/groups/%s", strings.TrimRight(baseURL, "/"), mailboxID, url.PathEscape(groupKey))
}

func localStamp(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.In(time.Local).Format("2006-01-02 15:04")
}

// firstParagraph returns the first paragraph of a Markdown report that is
// not a heading, joined into one line.
func firstParagraph(markdown string) string {
	var lines []string
	for _, raw := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, " ")
}

// mailTemplate parses one template file of TemplatesFS.
func mailTemplate(name string) (*template.Template, error) {
	if TemplatesFS == nil {
		return nil, errors.New("mail templates are not available")
	}
	tpl, err := template.ParseFS(TemplatesFS, name)
	if err != nil {
		return nil, fmt.Errorf("mail template %s: %w", name, err)
	}
	return tpl, nil
}

// renderSubjectAndBody executes the "subject" and "body" templates of a file.
func renderSubjectAndBody(name string, data any) (subject, body string, err error) {
	tpl, err := mailTemplate(name)
	if err != nil {
		return "", "", err
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "subject", data); err != nil {
		return "", "", fmt.Errorf("mail template %s (subject): %w", name, err)
	}
	subject = strings.Join(strings.Fields(buf.String()), " ")
	buf.Reset()
	if err := tpl.ExecuteTemplate(&buf, "body", data); err != nil {
		return "", "", fmt.Errorf("mail template %s (body): %w", name, err)
	}
	body = strings.TrimLeft(buf.String(), "\n") + "\n"
	return subject, body, nil
}

// renderNotification fills Subject and Body from the notification template.
func renderNotification(n *Notification) error {
	subject, body, err := renderSubjectAndBody(notifyTemplateFile, n)
	if err != nil {
		return err
	}
	n.Subject, n.Body = subject, body
	return nil
}

// loadCategoryLabels reads the category label file (key<TAB>label per line).
func loadCategoryLabels() (map[string]string, error) {
	if TemplatesFS == nil {
		return nil, errors.New("mail templates are not available")
	}
	data, err := fs.ReadFile(TemplatesFS, categoryLabelsFile)
	if err != nil {
		return nil, fmt.Errorf("category labels: %w", err)
	}
	labels := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, label, ok := strings.Cut(line, "\t")
		if !ok {
			key, label, ok = strings.Cut(line, " ")
		}
		if ok && strings.TrimSpace(key) != "" {
			labels[strings.TrimSpace(key)] = strings.TrimSpace(label)
		}
	}
	return labels, sc.Err()
}

// --- Sending ---

// ErrSMTPPasswordRequired is returned by SaveNotificationSettings and
// SMTPConfigForTest when a password is stored, the request changes the
// connection settings (host, port, security, username) and gives no
// password of its own.
var ErrSMTPPasswordRequired = errors.New("smtp_password is required when the connection settings change")

// SMTPTestInput carries the connection values a test mail may try instead of
// the saved ones; nil fields (and an empty password) mean the saved values.
type SMTPTestInput struct {
	Host     *string
	Port     *int
	Security *string
	Username *string
	Password *string
	From     *string
}

// SMTPConfigForTest builds the SMTP configuration of a test mail from the
// saved settings and the values given in the request. The stored password is
// used only when the host, port, security mode and username are the saved
// ones; as soon as one of them differs the request must carry its own
// password (ErrSMTPPasswordRequired otherwise), so that the stored password
// can never be sent to a server other than the one it was saved for.
func SMTPConfigForTest(db *sql.DB, key []byte, in *SMTPTestInput) (SMTPConfig, error) {
	saved, err := ResolveNotificationSettings(db, key)
	if err != nil {
		return SMTPConfig{}, err
	}
	cfg := saved.SMTP
	cfg.Password = ""
	if in == nil {
		in = &SMTPTestInput{}
	}
	if in.Host != nil {
		cfg.Host = strings.TrimSpace(*in.Host)
	}
	if in.Port != nil {
		if *in.Port < 1 || *in.Port > 65535 {
			return SMTPConfig{}, errors.New("smtp_port must be between 1 and 65535")
		}
		cfg.Port = *in.Port
	}
	if in.Security != nil {
		sec := strings.ToLower(strings.TrimSpace(*in.Security))
		if sec == "" {
			sec = DefaultSMTPSecurity
		}
		if err := ValidateSMTPSecurity(sec); err != nil {
			return SMTPConfig{}, err
		}
		cfg.Security = sec
	}
	if in.Username != nil {
		cfg.Username = strings.TrimSpace(*in.Username)
	}
	if in.From != nil {
		from, err := NormalizeEmail(*in.From)
		if err != nil {
			return SMTPConfig{}, fmt.Errorf("smtp_from: %w", err)
		}
		cfg.From = from
	}
	if in.Password != nil && *in.Password != "" {
		cfg.Password = *in.Password
		return cfg, nil
	}
	if !saved.SMTPPasswordSet {
		return cfg, nil
	}
	sameServer := cfg.Host == saved.SMTP.Host && cfg.Port == saved.SMTP.Port &&
		cfg.Security == saved.SMTP.Security && cfg.Username == saved.SMTP.Username
	if !sameServer {
		return SMTPConfig{}, ErrSMTPPasswordRequired
	}
	cfg.Password = saved.SMTP.Password
	return cfg, nil
}

// TestSMTP sends a short mail that only proves the SMTP settings work.
func TestSMTP(ctx context.Context, cfg SMTPConfig, to string) error {
	to, err := NormalizeEmail(to)
	if err != nil {
		return err
	}
	if to == "" {
		return errors.New("recipient address is required")
	}
	if !cfg.Configured() {
		return ErrSMTPNotConfigured
	}
	port := cfg.Port
	if port <= 0 {
		port = DefaultSMTPPort
	}
	subject, body, err := renderSubjectAndBody(testTemplateFile, map[string]any{
		"Date": time.Now().In(time.Local).Format("2006-01-02 15:04"), "Host": cfg.Host, "Port": port,
		"Security": cfg.Security, "From": cfg.From,
	})
	if err != nil {
		return err
	}
	return SendMail(ctx, cfg, cfg.From, []string{to}, subject, body)
}

// NotificationResult reports what SendNotification did.
type NotificationResult struct {
	Sent       bool
	Skipped    string // why nothing was sent ("" when Sent)
	Recipients []string
	Groups     int
	Subject    string
}

// Skipped reasons (stored in the job result as "skipped: <reason>").
const (
	SkipSMTPNotConfigured  = "SMTP host or sender address is not set"
	SkipNoRecipient        = "no recipient is selected"
	SkipRecipientsNoEmail  = "none of the selected recipients has an email address"
	SkipNoActionableGroups = "no open actionable group has a completed report"
)

// SendNotification applies the send conditions of the design (SMTP
// configured, at least one recipient with an address, at least one group),
// sends the mail and records the time. notify_enabled is not checked here:
// it only gates the scheduled sending (NotifyDue), so "send now" works while
// the schedule is off. A failed condition is not an error: the result says
// why nothing was sent.
func SendNotification(ctx context.Context, db *sql.DB, key []byte, mailsRoot string, now time.Time) (*NotificationResult, error) {
	s, err := ResolveNotificationSettings(db, key)
	if err != nil {
		return nil, err
	}
	res := &NotificationResult{}
	if !s.SMTP.Configured() {
		res.Skipped = SkipSMTPNotConfigured
		return res, nil
	}
	selected, addresses, err := NotificationRecipients(db, s)
	if err != nil {
		return nil, err
	}
	switch {
	case len(selected) == 0:
		res.Skipped = SkipNoRecipient
		return res, nil
	case len(addresses) == 0:
		res.Skipped = SkipRecipientsNoEmail
		return res, nil
	}
	res.Recipients = addresses
	n, err := BuildNotification(ctx, db, mailsRoot, EffectiveBaseURL(db, s), now)
	if err != nil {
		return nil, err
	}
	res.Groups, res.Subject = n.Total, n.Subject
	if n.Total == 0 {
		res.Skipped = SkipNoActionableGroups
		return res, nil
	}
	if err := SendMail(ctx, s.SMTP, s.SMTP.From, addresses, n.Subject, n.Body); err != nil {
		return nil, err
	}
	res.Sent = true
	if err := SetNotifyLastSent(db, now); err != nil {
		log.Printf("failed to record the notification time: %v", err)
	}
	return res, nil
}
