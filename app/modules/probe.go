package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/wording"
)

// Delivery probe (the system design document (Documents) 7.6).
//
// At every probe send time each enabled mailbox with an SMTP connection (its
// smtp_host and the rest of the connection) sends one probe mail to the
// probe address, and the probe server (the mailbox of that address, over
// IMAP) is checked for them:
//
//   - The first arrival check runs DelayMinutes after the latest send and
//     looks for every probe mail that has not arrived yet, of every send
//     (one check, however many sends are waiting).
//   - A mail found records its arrival time and its delay. A mail not found
//     is delayed once DelayMinutes passed since it was sent and a failure
//     once FailureMinutes passed; its delay is updated at every check.
//   - While a mail is missing, the next check runs FailureMinutes after the
//     latest send and then every FailureMinutes.
//   - The next send replaces the pending check: its first check runs
//     DelayMinutes after it and covers the older mails as well, so the
//     number of checks never grows.
//   - A mail that arrives after it was marked (or that arrived later than
//     the delay threshold) is "recovered" and keeps the level it reached;
//     a user can close a missing mail by hand with a reason ("resolved").
//   - A mail that becomes a failure (or that the SMTP server refused) is
//     mailed once to the notification recipients.
//   - A probe mail whose arrival is recorded is deleted from the probe
//     server at once (the probe mailbox would fill up otherwise); one left
//     there (the deletion failed, it arrived after its record was removed)
//     is deleted once it is ProbeLeftoverAge old, only by the first arrival
//     check after a send (the check that covers all addresses of that send
//     at once; the re-checks while mails are missing leave it, and no
//     separate run reads the server for it). Other mail of the probe
//     mailbox is never touched (mailengine.CheckProbeServer).
//
// The probe settings live in the settings table (SettingProbe*), the SMTP
// connection of a mailbox in its detail_info (smtp_host and the rest), the
// probe mails in the probe_mails table of the master database.

// Mail templates of the probe (templates/mail/<kind>_<language>.txt): the
// body of a probe mail and the failure mail to the notification recipients.
const (
	probeTemplateKind        = "probe"
	probeFailureTemplateKind = "probe_failure"
)

// errProbeAddressInvalid is returned for a probe address that is not a mail
// address.
var errProbeAddressInvalid = wording.New("validation.common.emailFormat", "probe_address is not a valid mail address")

// ErrProbeIMAPPasswordRequired is returned when the probe server connection
// changes while a password is stored and no password is given.
var ErrProbeIMAPPasswordRequired = wording.New("validation.connection.passwordRequiredOnChange", "probe_imap_password is required when the connection settings change")

// ErrProbeNotConfigured is returned when a probe action needs the probe
// server and it is not set up.
var ErrProbeNotConfigured = wording.New("result.probe.notConfigured", "the probe server (address, IMAP host, username and password) is not set")

// ProbeSettings is the resolved probe configuration.
type ProbeSettings struct {
	Enabled bool
	Address string
	// Conn carries the IMAP connection of the probe server in the fields
	// of a mailbox row (ImapHost, ImapPort, ImapSecurity, ImapUsername,
	// Folder), so the IMAP code of mailengine serves it unchanged.
	Conn           models.Mailbox
	PasswordSet    bool
	Password       string // filled only when resolved with the master key
	SendTimes      []string
	DelayMinutes   int
	FailureMinutes int
	LastSendAt     sql.NullTime
	NextCheckAt    sql.NullTime
	LastCheckAt    sql.NullTime
	LastCheckError string
}

// Configured reports whether the probe address and the probe server are
// set (the mails can be sent and looked for).
func (s *ProbeSettings) Configured() bool {
	return s.Address != "" && s.Conn.ImapHost != "" && s.Conn.ImapUsername != "" && s.PasswordSet
}

// Delay and Failure return the two thresholds as durations.
func (s *ProbeSettings) Delay() time.Duration {
	return time.Duration(s.DelayMinutes) * time.Minute
}

func (s *ProbeSettings) Failure() time.Duration {
	return time.Duration(s.FailureMinutes) * time.Minute
}

// ResolveProbeSettings reads the probe settings (saved > default). key
// decrypts the IMAP password; nil leaves it empty.
func ResolveProbeSettings(db *sql.DB, key []byte) (*ProbeSettings, error) {
	s := &ProbeSettings{
		Enabled: models.GetSetting(db, SettingProbeEnabled) == "1",
		Address: models.GetSetting(db, SettingProbeAddress),
		Conn: models.Mailbox{
			Address:      models.GetSetting(db, SettingProbeAddress),
			ImapHost:     models.GetSetting(db, SettingProbeIMAPHost),
			ImapPort:     resolveInt(db, SettingProbeIMAPPort, 0, DefaultIMAPPort),
			ImapSecurity: resolveStr(db, SettingProbeIMAPSecurity, "", IMAPSecuritySSL),
			ImapUsername: models.GetSetting(db, SettingProbeIMAPUsername),
			Folder:       resolveStr(db, SettingProbeIMAPFolder, "", "INBOX"),
		},
		SendTimes:      ResolveProbeSendTimes(db),
		DelayMinutes:   resolveInt(db, SettingProbeDelayMinutes, 0, DefaultProbeDelayMinutes),
		FailureMinutes: resolveInt(db, SettingProbeFailureMinutes, 0, DefaultProbeFailureMinutes),
		LastCheckError: models.GetSetting(db, SettingProbeLastCheckError),
	}
	if ValidateSMTPSecurity(s.Conn.ImapSecurity) != nil {
		s.Conn.ImapSecurity = IMAPSecuritySSL
	}
	if validateProbeMinutes(s.DelayMinutes) != nil {
		s.DelayMinutes = DefaultProbeDelayMinutes
	}
	if validateProbeMinutes(s.FailureMinutes) != nil || s.FailureMinutes <= s.DelayMinutes {
		s.FailureMinutes = max(DefaultProbeFailureMinutes, s.DelayMinutes+1)
	}
	if enc := models.GetSetting(db, SettingProbeIMAPPassword); enc != "" {
		s.PasswordSet = true
		if key != nil {
			password, err := DecryptSecret(key, enc)
			if err != nil {
				return nil, fmt.Errorf("probe IMAP password: %w", err)
			}
			s.Password = password
		}
	}
	s.LastSendAt = settingTime(db, SettingProbeLastSendAt)
	s.NextCheckAt = settingTime(db, SettingProbeNextCheckAt)
	s.LastCheckAt = settingTime(db, SettingProbeLastCheckAt)
	return s, nil
}

// ResolveProbeSendTimes returns the probe send times (saved > default). An
// explicitly empty list means no scheduled send.
func ResolveProbeSendTimes(db *sql.DB) []string {
	saved, found, err := models.GetSettingStrict(db, SettingProbeSendTimes)
	if err != nil || !found {
		times, _ := ParseCheckTimes([]string{DefaultProbeSendTimes})
		return times
	}
	times, err := ParseCheckTimes([]string{saved})
	if err != nil {
		log.Printf("ignoring invalid saved probe_send_times %q: %v", saved, err)
		times, _ = ParseCheckTimes([]string{DefaultProbeSendTimes})
	}
	return times
}

// settingTime reads an RFC 3339 setting (invalid when absent or unreadable).
func settingTime(db *sql.DB, key string) sql.NullTime {
	raw := models.GetSetting(db, key)
	if raw == "" {
		return sql.NullTime{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// setSettingTime stores an RFC 3339 setting; the zero time removes it.
func setSettingTime(db *sql.DB, key string, t time.Time) error {
	if t.IsZero() {
		return models.DeleteSetting(db, key)
	}
	return models.SetSetting(db, key, t.UTC().Format(time.RFC3339))
}

func validateProbeMinutes(n int) error {
	if n < MinProbeMinutes || n > MaxProbeMinutes {
		return wording.New("validation.common.numberOutOfRange",
			fmt.Sprintf("the probe thresholds must be between %d and %d minutes", MinProbeMinutes, MaxProbeMinutes)).
			With("min", MinProbeMinutes).With("max", MaxProbeMinutes)
	}
	return nil
}

// ProbeInput carries the fields of a probe settings update; nil fields are
// left unchanged. An empty IMAPPassword also leaves the password unchanged.
type ProbeInput struct {
	Enabled        *bool
	Address        *string
	IMAPHost       *string
	IMAPPort       *int
	IMAPSecurity   *string
	IMAPUsername   *string
	IMAPPassword   *string
	IMAPFolder     *string
	SendTimes      *[]string
	DelayMinutes   *int
	FailureMinutes *int
}

// SaveProbeSettings validates every given field and persists them together
// (values equal to the code default delete the row). The failure threshold
// must be longer than the delay threshold. While a password is stored, an
// update that changes the host, port, security mode or username must carry
// the password too (ErrProbeIMAPPasswordRequired otherwise; nothing is
// saved). Enabling the probe needs the probe server to be set up.
func SaveProbeSettings(db *sql.DB, key []byte, in *ProbeInput) error {
	saved, err := ResolveProbeSettings(db, nil)
	if err != nil {
		return err
	}
	next := *saved
	var writes []settingWrite
	if in.Address != nil {
		address, err := NormalizeEmail(*in.Address)
		if err != nil {
			return errProbeAddressInvalid
		}
		next.Address = address
		writes = append(writes, settingWrite{SettingProbeAddress, address, address == ""})
	}
	if in.IMAPHost != nil {
		host := strings.TrimSpace(*in.IMAPHost)
		if host != "" && !validIMAPHost(host) {
			return wording.New("validation.connection.hostInvalid", "probe_imap_host is not a valid host name")
		}
		next.Conn.ImapHost = host
		writes = append(writes, settingWrite{SettingProbeIMAPHost, host, host == ""})
	}
	if in.IMAPPort != nil {
		if *in.IMAPPort < 1 || *in.IMAPPort > 65535 {
			return wording.New("validation.common.numberOutOfRange", "probe_imap_port must be between 1 and 65535").With("min", 1).With("max", 65535)
		}
		next.Conn.ImapPort = *in.IMAPPort
		writes = append(writes, settingWrite{SettingProbeIMAPPort, strconv.Itoa(*in.IMAPPort), *in.IMAPPort == DefaultIMAPPort})
	}
	if in.IMAPSecurity != nil {
		sec := strings.ToLower(strings.TrimSpace(*in.IMAPSecurity))
		if sec == "" {
			sec = IMAPSecuritySSL
		}
		if err := ValidateSMTPSecurity(sec); err != nil {
			return err
		}
		next.Conn.ImapSecurity = sec
		writes = append(writes, settingWrite{SettingProbeIMAPSecurity, sec, sec == IMAPSecuritySSL})
	}
	if in.IMAPUsername != nil {
		name := strings.TrimSpace(*in.IMAPUsername)
		next.Conn.ImapUsername = name
		writes = append(writes, settingWrite{SettingProbeIMAPUsername, name, name == ""})
	}
	if in.IMAPFolder != nil {
		folder := strings.TrimSpace(*in.IMAPFolder)
		if strings.ContainsAny(folder, "\r\n") {
			return wording.New("validation.mailbox.folderInvalid", "probe_imap_folder is not a valid folder name")
		}
		if folder == "" {
			folder = "INBOX"
		}
		writes = append(writes, settingWrite{SettingProbeIMAPFolder, folder, folder == "INBOX"})
	}
	passwordGiven := in.IMAPPassword != nil && *in.IMAPPassword != ""
	if passwordGiven {
		if key == nil {
			return errors.New("the master key is required to store the probe IMAP password")
		}
		enc, err := EncryptSecret(key, *in.IMAPPassword)
		if err != nil {
			return err
		}
		next.PasswordSet = true
		writes = append(writes, settingWrite{SettingProbeIMAPPassword, enc, false})
	}
	if in.SendTimes != nil {
		times, err := ParseCheckTimes(*in.SendTimes)
		if err != nil {
			return err
		}
		value := strings.Join(times, ",")
		// An empty list is stored as empty (no scheduled send), unlike an
		// absent row (the default times).
		writes = append(writes, settingWrite{SettingProbeSendTimes, value, value == DefaultProbeSendTimes})
	}
	if in.DelayMinutes != nil {
		if err := validateProbeMinutes(*in.DelayMinutes); err != nil {
			return err
		}
		next.DelayMinutes = *in.DelayMinutes
		writes = append(writes, settingWrite{SettingProbeDelayMinutes, strconv.Itoa(*in.DelayMinutes), *in.DelayMinutes == DefaultProbeDelayMinutes})
	}
	if in.FailureMinutes != nil {
		if err := validateProbeMinutes(*in.FailureMinutes); err != nil {
			return err
		}
		next.FailureMinutes = *in.FailureMinutes
		writes = append(writes, settingWrite{SettingProbeFailureMinutes, strconv.Itoa(*in.FailureMinutes), *in.FailureMinutes == DefaultProbeFailureMinutes})
	}
	if next.FailureMinutes <= next.DelayMinutes {
		return wording.New("validation.probe.failureNotAfterDelay", "probe_failure_minutes must be longer than probe_delay_minutes")
	}
	if !passwordGiven && saved.PasswordSet && probeConnectionChanged(&saved.Conn, &next.Conn) {
		return ErrProbeIMAPPasswordRequired
	}
	if in.Enabled != nil {
		if *in.Enabled && !next.Configured() {
			return ErrProbeNotConfigured
		}
		writes = append(writes, settingWrite{SettingProbeEnabled, "1", !*in.Enabled})
	}
	return persistSettings(db, writes)
}

// probeConnectionChanged reports whether the host, port, security mode or
// username of the probe server differ.
func probeConnectionChanged(a, b *models.Mailbox) bool {
	return !strings.EqualFold(a.ImapHost, b.ImapHost) || a.ImapPort != b.ImapPort ||
		a.ImapSecurity != b.ImapSecurity || a.ImapUsername != b.ImapUsername
}

// ProbeTestInput carries the connection values a test of the probe server
// may try instead of the saved ones; nil fields (and an empty password) mean
// the saved values.
type ProbeTestInput struct {
	IMAPHost     *string
	IMAPPort     *int
	IMAPSecurity *string
	IMAPUsername *string
	IMAPPassword *string
	IMAPFolder   *string
}

// TestProbeServer connects to the probe server with the saved settings and
// the values of the request, logs in and selects the folder. The stored
// password is used only for the saved connection (same host, port, security
// and username), as for the mailboxes.
func TestProbeServer(ctx context.Context, db *sql.DB, key []byte, in *ProbeTestInput) error {
	saved, err := ResolveProbeSettings(db, key)
	if err != nil {
		return err
	}
	conn := saved.Conn
	if in == nil {
		in = &ProbeTestInput{}
	}
	if in.IMAPHost != nil {
		conn.ImapHost = strings.TrimSpace(*in.IMAPHost)
	}
	if in.IMAPPort != nil {
		conn.ImapPort = *in.IMAPPort
	}
	if in.IMAPSecurity != nil {
		conn.ImapSecurity = strings.ToLower(strings.TrimSpace(*in.IMAPSecurity))
		if conn.ImapSecurity == "" {
			conn.ImapSecurity = IMAPSecuritySSL
		}
	}
	if in.IMAPUsername != nil {
		conn.ImapUsername = strings.TrimSpace(*in.IMAPUsername)
	}
	if in.IMAPFolder != nil {
		conn.Folder = strings.TrimSpace(*in.IMAPFolder)
	}
	if conn.ImapHost == "" || !validIMAPHost(conn.ImapHost) {
		return wording.New("validation.connection.hostRequired", "probe_imap_host is required")
	}
	if conn.ImapPort < 1 || conn.ImapPort > 65535 {
		return wording.New("validation.common.numberOutOfRange", "probe_imap_port must be between 1 and 65535").With("min", 1).With("max", 65535)
	}
	if err := ValidateSMTPSecurity(conn.ImapSecurity); err != nil {
		return err
	}
	if conn.ImapUsername == "" {
		return wording.New("validation.connection.usernameRequired", "probe_imap_username is required")
	}
	password := ""
	switch {
	case in.IMAPPassword != nil && *in.IMAPPassword != "":
		password = *in.IMAPPassword
	case !saved.PasswordSet:
		return errIMAPPasswordRequired
	case probeConnectionChanged(&saved.Conn, &conn):
		return ErrProbeIMAPPasswordRequired
	default:
		password = saved.Password
	}
	ctx, cancel := context.WithTimeout(ctx, IMAPTimeout)
	defer cancel()
	if err := mailengine.TestConnection(ctx, &conn, password); err != nil {
		return wording.New("result.mailbox.testFailed", err.Error()).With("detail", err.Error())
	}
	return nil
}

// --- Timing ---

// ProbeSendDue returns the occurrence ("YYYY-MM-DD HH:MM") of the latest
// probe send time in (last, now] that was not fired yet (fired maps a time
// to the occurrence it last fired for), or false. Several send times
// passed in one tick send once, for the latest of them.
func ProbeSendDue(last, now time.Time, times []string, fired map[string]string) (dueTime, bool) {
	due := dueCheckTimes(last, now, times, fired)
	if len(due) == 0 {
		return dueTime{}, false
	}
	latest := due[0]
	for _, d := range due[1:] {
		if d.minute > latest.minute {
			latest = d
		}
	}
	return latest, true
}

// NextProbeCheckAt returns when the arrival check after one that ran at
// checkedAt is due while probe mails are still missing: DelayMinutes after
// the latest send while that is still ahead (a check run by hand earlier
// keeps it), then FailureMinutes after the latest send, then every
// FailureMinutes. Without a recorded send it is FailureMinutes after the
// check.
func NextProbeCheckAt(lastSend, checkedAt time.Time, delay, failure time.Duration) time.Time {
	if failure <= 0 {
		failure = time.Duration(DefaultProbeFailureMinutes) * time.Minute
	}
	if lastSend.IsZero() {
		return checkedAt.Add(failure)
	}
	if first := lastSend.Add(delay); first.After(checkedAt) {
		return first
	}
	next := lastSend.Add(failure)
	if !next.After(checkedAt) {
		// Whole failure periods since the latest send, then one more.
		n := checkedAt.Sub(lastSend)/failure + 1
		next = lastSend.Add(n * failure)
	}
	return next
}

// ProbeLevelFor returns the level of a probe mail that has been missing (or
// took) elapsed: failure from the failure threshold, delayed from the delay
// threshold.
func ProbeLevelFor(elapsed, delay, failure time.Duration) string {
	switch {
	case elapsed >= failure:
		return models.ProbeLevelFailure
	case elapsed >= delay:
		return models.ProbeLevelDelayed
	}
	return models.ProbeLevelNone
}

// worseProbeLevel returns the worse of two levels.
func worseProbeLevel(a, b string) string {
	rank := func(l string) int {
		switch l {
		case models.ProbeLevelFailure:
			return 2
		case models.ProbeLevelDelayed:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// ApplyProbeCheck updates one waiting probe mail with what a check at now
// found: arrivedAt is its arrival time (zero when it was not found). It
// returns the level the mail newly reached ("" when unchanged).
func ApplyProbeCheck(p *models.ProbeMail, arrivedAt, now time.Time, delay, failure time.Duration) string {
	before := p.Level
	p.LastCheckedAt = sql.NullTime{Time: now, Valid: true}
	if !arrivedAt.IsZero() {
		took := max(arrivedAt.Sub(p.SentAt), 0)
		p.DelaySeconds = int64(took / time.Second)
		p.ArrivedAt = sql.NullTime{Time: arrivedAt, Valid: true}
		p.Level = worseProbeLevel(p.Level, ProbeLevelFor(took, delay, failure))
		if p.Level == models.ProbeLevelNone {
			p.Status = models.ProbeStatusArrived
		} else {
			p.Status = models.ProbeStatusRecovered
		}
		return ""
	}
	elapsed := max(now.Sub(p.SentAt), 0)
	p.DelaySeconds = int64(elapsed / time.Second)
	p.Level = worseProbeLevel(p.Level, ProbeLevelFor(elapsed, delay, failure))
	switch p.Level {
	case models.ProbeLevelFailure:
		p.Status = models.ProbeStatusFailure
	case models.ProbeLevelDelayed:
		p.Status = models.ProbeStatusDelayed
	default:
		p.Status = models.ProbeStatusPending
	}
	if p.Level != before {
		return p.Level
	}
	return ""
}

// --- Probe mails ---

// ProbeParticipates reports whether a mailbox sends probe mails: it is
// enabled, its send check is on and it has an SMTP host (smtp_host).
func ProbeParticipates(mb *models.Mailbox) bool {
	return mb.Enabled && mb.ProbeEnabled && strings.TrimSpace(mb.SmtpHost) != ""
}

// probeSMTPConfig builds the SMTP configuration of the probe mails of a
// mailbox from its SMTP connection (password decrypted with key; a port or
// security saved as 0 / "" takes the default).
func probeSMTPConfig(key []byte, mb *models.Mailbox) (SMTPConfig, error) {
	cfg := SMTPConfig{Host: mb.SmtpHost, Port: mb.SmtpPort, Security: mb.SmtpSecurity,
		Username: mb.SmtpUsername, From: mb.Address}
	if cfg.Security == "" {
		cfg.Security = DefaultSMTPSecurity
	}
	if mb.SmtpUsername != "" && mb.SmtpPasswordEnc != "" {
		password, err := DecryptSecret(key, mb.SmtpPasswordEnc)
		if err != nil {
			return SMTPConfig{}, fmt.Errorf("SMTP password: %w", err)
		}
		cfg.Password = password
	}
	return cfg, nil
}

// newProbeToken returns a fresh token (ProbeSubjectMarker and 16 hex digits).
func newProbeToken() (string, error) {
	tok, err := RandomHex(8)
	if err != nil {
		return "", err
	}
	return ProbeSubjectMarker + tok, nil
}

// probeSubject builds the subject of a probe mail: ASCII only, so no server
// has to decode it to find the token, and it names the date and time of the
// send (the slot, else the time of sending, server local time) and the
// sending address, so a missing mail can be traced by its subject alone.
func probeSubject(slotLabel, address, token string) string {
	return fmt.Sprintf("%sProbe %s %s %s", NotifyMailSubjectPrefix, slotLabel, address, token)
}

// probeMailContent renders the probe mail of a mailbox (the body in the
// server's default language; the subject is fixed, see probeSubject).
func probeMailContent(mb *models.Mailbox, slotLabel, token string, now time.Time, test bool) (OutgoingMail, error) {
	loc := DefaultMailLocale()
	_, body, err := renderSubjectAndBody(mailTemplatePath(probeTemplateKind, loc.Language), map[string]any{
		"Address": mb.Address, "Slot": slotLabel, "SentAt": now.In(loc.Location).Format("2006-01-02 15:04:05 -0700"),
		"Token": token, "Test": test,
	})
	if err != nil {
		return OutgoingMail{}, err
	}
	subject := probeSubject(slotLabel, mb.Address, token)
	if test {
		subject = NotifyMailSubjectPrefix + "Probe test " + slotLabel + " " + mb.Address
	}
	fromName := mb.DisplayName
	if fromName == "" {
		fromName = SMTPFromName
	}
	mail := OutgoingMail{FromName: fromName, Subject: subject, Body: body}
	if !test {
		mail.Headers = []MailHeader{{Name: ProbeHeader, Value: token}}
	}
	return mail, nil
}

// probeSlotLabel returns the "YYYY-MM-DD HH:MM" of a send: the slot, else
// the time of sending (server local time).
func probeSlotLabel(slot string, now time.Time) string {
	if slot != "" {
		return slot
	}
	return now.In(time.Local).Format("2006-01-02 15:04")
}

// TestProbeSMTP sends a test mail through the SMTP connection of a mailbox
// input to the probe address (no probe mail is recorded: the
// subject carries no token). An empty password uses the stored one when the
// input names a stored mailbox and the same connection.
func TestProbeSMTP(ctx context.Context, db *sql.DB, key []byte, in *MailboxInput) error {
	// The test tries the connection of the send check, so it is validated
	// as switched on (host and port required) whatever the input says.
	on := true
	in.ProbeEnabled = &on
	if err := ValidateMailboxInput(in); err != nil {
		return err
	}
	settings, err := ResolveProbeSettings(db, nil)
	if err != nil {
		return err
	}
	if settings.Address == "" {
		return wording.New("result.probe.addressNotSet", "the probe address is not set (Settings - Delivery probe)")
	}
	mb := &models.Mailbox{}
	if err := applyMailboxInput(key, mb, in); err != nil {
		return err
	}
	if in.SmtpPassword == "" && in.SmtpUsername != "" {
		if in.ID == 0 {
			return errIMAPPasswordRequired
		}
		stored, err := models.GetMailboxByID(db, in.ID)
		if err == sql.ErrNoRows {
			return wording.New("system.notFound", "mailbox not found")
		}
		if err != nil {
			return err
		}
		if stored.SmtpPasswordEnc == "" {
			return errIMAPPasswordRequired
		}
		if !sameSMTPConnection(stored, in) {
			return ErrSMTPConnectionPasswordRequired
		}
		mb.SmtpPasswordEnc = stored.SmtpPasswordEnc
	}
	cfg, err := probeSMTPConfig(key, mb)
	if err != nil {
		return err
	}
	now := time.Now()
	mail, err := probeMailContent(mb, probeSlotLabel("", now), "", now, true)
	if err != nil {
		return err
	}
	if err := SendMessage(ctx, cfg, mb.Address, []string{settings.Address}, mail); err != nil {
		return wording.New("result.notification.testFailed", err.Error()).With("detail", err.Error())
	}
	return nil
}

// ProbeSendResult reports what SendProbes did.
type ProbeSendResult struct {
	Skipped string // why nothing was sent ("" when the mailboxes were tried)
	Sent    int
	Failed  int
}

// Skipped reasons of the probe jobs (stored in the job result as
// "skipped: <reason>").
const (
	SkipProbeNotConfigured = "the probe server is not set"
	SkipProbeNoMailbox     = "no enabled mail address has an SMTP server (smtp_host)"
	SkipProbeNothingToSee  = "no probe mail is waiting"
)

// SendProbes sends one probe mail from every participating mailbox to the
// probe address and records them (slot: "YYYY-MM-DD HH:MM" of the send
// time, "" when sent by hand). Afterwards the arrival check is scheduled
// DelayMinutes after the end of the sending, replacing a pending check (it
// covers every missing mail). A mail the SMTP server refused is recorded as
// send_failed and mailed to the notification recipients.
func SendProbes(ctx context.Context, db *sql.DB, key []byte, baseURL, slot string, progress func(string)) (*ProbeSendResult, error) {
	settings, err := ResolveProbeSettings(db, nil)
	if err != nil {
		return nil, err
	}
	res := &ProbeSendResult{}
	if !settings.Configured() {
		res.Skipped = SkipProbeNotConfigured
		return res, nil
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		return nil, err
	}
	var targets []*models.Mailbox
	for _, mb := range mailboxes {
		if ProbeParticipates(mb) {
			targets = append(targets, mb)
		}
	}
	if len(targets) == 0 {
		res.Skipped = SkipProbeNoMailbox
		return res, nil
	}
	for _, mb := range targets {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := sendOneProbe(ctx, db, key, mb, settings.Address, slot); err != nil {
			res.Failed++
			progress(fmt.Sprintf("%s: %v", mb.Address, err))
			continue
		}
		res.Sent++
		progress(fmt.Sprintf("%s: sent", mb.Address))
	}
	end := time.Now()
	if err := setSettingTime(db, SettingProbeLastSendAt, end); err != nil {
		return res, err
	}
	if err := scheduleProbeCheck(db, end.Add(settings.Delay())); err != nil {
		return res, err
	}
	if res.Failed > 0 {
		notifyProbeFailures(ctx, db, key, baseURL, progress)
	}
	return res, nil
}

// sendOneProbe sends and records the probe mail of one mailbox. A mail that
// could not be sent is recorded too (send_failed) and its error returned.
func sendOneProbe(ctx context.Context, db *sql.DB, key []byte, mb *models.Mailbox, to, slot string) error {
	token, err := newProbeToken()
	if err != nil {
		return err
	}
	now := time.Now()
	label := probeSlotLabel(slot, now)
	p := &models.ProbeMail{MailboxID: mb.ID, Address: mb.Address, Token: token, Slot: label, Manual: slot == "",
		Subject: probeSubject(label, mb.Address, token), Status: models.ProbeStatusPending}
	sendErr := func() error {
		cfg, err := probeSMTPConfig(key, mb)
		if err != nil {
			return err
		}
		mail, err := probeMailContent(mb, label, token, now, false)
		if err != nil {
			return err
		}
		return SendMessage(ctx, cfg, mb.Address, []string{to}, mail)
	}()
	p.SentAt = time.Now()
	if sendErr != nil {
		p.Status, p.Level, p.ErrorMessage = models.ProbeStatusSendFailed, models.ProbeLevelFailure, sendErr.Error()
	}
	if err := models.InsertProbeMail(db, p); err != nil {
		return errors.Join(sendErr, fmt.Errorf("record the probe mail: %w", err))
	}
	return sendErr
}

// scheduleProbeCheck records when the next arrival check is due (the zero
// time: none).
func scheduleProbeCheck(db *sql.DB, at time.Time) error {
	return setSettingTime(db, SettingProbeNextCheckAt, at)
}

// ProbeCheckResult reports what CheckProbes did.
type ProbeCheckResult struct {
	Skipped   string
	Checked   int // waiting mails looked for
	Arrived   int // arrived within the delay threshold
	Recovered int // arrived late
	Delayed   int // newly delayed
	Failed    int // newly failed
	Missing   int // still missing after the check
	Deleted   int // confirmed probe mails deleted from the probe server
	// DeleteError is why the confirmed probe mails could not be deleted
	// from the probe server ("" when they were, or none had to be).
	DeleteError string
	NextAt      time.Time
}

// CheckProbes looks for every waiting probe mail on the probe server and
// updates them (ApplyProbeCheck), deletes the probe mails from the probe
// server (sortProbeMessages), mails the new failures and schedules the next check
// (NextProbeCheckAt; none when nothing is missing). When the probe server
// cannot be read the check is tried again ProbeCheckRetryInterval later.
func CheckProbes(ctx context.Context, db *sql.DB, key []byte, baseURL string, progress func(string)) (res *ProbeCheckResult, err error) {
	res = &ProbeCheckResult{}
	now := time.Now()
	lastCheck := settingTime(db, SettingProbeLastCheckAt)
	defer func() {
		if err != nil {
			res.NextAt = now.Add(ProbeCheckRetryInterval)
		}
		if serr := scheduleProbeCheck(db, res.NextAt); serr != nil {
			log.Printf("probe: failed to schedule the next check: %v", serr)
		}
		if serr := setSettingTime(db, SettingProbeLastCheckAt, now); serr != nil {
			log.Printf("probe: failed to record the check time: %v", serr)
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if serr := persistSettings(db, []settingWrite{{SettingProbeLastCheckError, msg, msg == ""}}); serr != nil {
			log.Printf("probe: failed to record the check error: %v", serr)
		}
	}()
	settings, err := ResolveProbeSettings(db, key)
	if err != nil {
		return res, err
	}
	if !settings.Configured() {
		res.Skipped = SkipProbeNotConfigured
		return res, nil
	}
	waiting, err := models.ListWaitingProbeMails(db)
	if err != nil {
		return res, err
	}
	if len(waiting) == 0 {
		res.Skipped = SkipProbeNothingToSee
		return res, nil
	}
	progress(fmt.Sprintf("looking for %d probe mails on %s", len(waiting), settings.Conn.ImapHost))
	res.Checked = len(waiting)
	// The probe mails left on the server are deleted only by the first
	// check after a send, not by the re-checks while mails are missing.
	firstAfterSend := settings.LastSendAt.Valid && (!lastCheck.Valid || lastCheck.Time.Before(settings.LastSendAt.Time))
	// The arrivals are recorded before anything is deleted from the server:
	// a message is removed only once its arrival is in the database.
	record := func(found []mailengine.ProbeMessage) (map[uint32]bool, error) {
		now = time.Now()
		tokens := make([]string, 0, len(found))
		for _, m := range found {
			tokens = append(tokens, m.Token)
		}
		recorded, err := models.ProbeAddressesByToken(db, tokens)
		if err != nil {
			return nil, err
		}
		arrivals, remove := sortProbeMessages(found, recorded, now, firstAfterSend)
		if err := recordProbeArrivals(db, waiting, arrivals, now, settings, res, progress); err != nil {
			return nil, err
		}
		return remove, nil
	}
	ictx, cancel := context.WithTimeout(ctx, IMAPTimeout)
	deleted, err := mailengine.CheckProbeServer(ictx, &settings.Conn, settings.Password, ProbeSubjectMarker, ProbeHeader, record)
	cancel()
	res.Deleted = deleted
	switch {
	case errors.Is(err, mailengine.ErrProbeDelete):
		// The check itself succeeded; the next one deletes them.
		res.DeleteError = err.Error()
		progress(err.Error())
	case err != nil:
		return res, fmt.Errorf("probe server %s: %w", settings.Conn.ImapHost, err)
	}
	if deleted > 0 {
		progress(fmt.Sprintf("deleted %d confirmed probe mail(s) from the probe server", deleted))
	}
	if res.Missing > 0 {
		res.NextAt = NextProbeCheckAt(settings.LastSendAt.Time, now, settings.Delay(), settings.Failure())
	}
	notifyProbeFailures(ctx, db, key, baseURL, progress)
	return res, nil
}

// recordProbeArrivals applies what the probe server showed (arrivals:
// token -> arrival time) to the waiting probe mails (ApplyProbeCheck) in one
// transaction and counts the outcome in res.
func recordProbeArrivals(db *sql.DB, waiting []*models.ProbeMail, arrivals map[string]time.Time, now time.Time,
	settings *ProbeSettings, res *ProbeCheckResult, progress func(string)) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range waiting {
		newLevel := ApplyProbeCheck(p, arrivals[p.Token], now, settings.Delay(), settings.Failure())
		switch {
		case p.Status == models.ProbeStatusArrived:
			res.Arrived++
		case p.Status == models.ProbeStatusRecovered:
			res.Recovered++
			progress(fmt.Sprintf("%s (%s): arrived after %s", p.Address, p.Slot, formatProbeDelay(p.DelaySeconds)))
		default:
			res.Missing++
			if newLevel == models.ProbeLevelFailure {
				res.Failed++
			} else if newLevel == models.ProbeLevelDelayed {
				res.Delayed++
			}
			if newLevel != "" {
				progress(fmt.Sprintf("%s (%s): %s, missing for %s", p.Address, p.Slot, newLevel, formatProbeDelay(p.DelaySeconds)))
			}
		}
		if err := models.UpdateProbeMailCheck(tx, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// sortProbeMessages decides what the probe mails found on the probe server
// mean (found: the messages whose subject and probe header carry the same
// token; recorded: token -> sending address of the recorded probe mails).
// A message is one of our probe mails when its token is recorded and its
// sender is the recorded address: it is an arrival (the earliest one per
// token) and it is deleted at once. Any other probe mail (a deletion that
// failed, an arrival after the record was removed) is deleted only when
// leftovers is set (the first check after a send) and it is
// ProbeLeftoverAge old; otherwise it is left alone.
func sortProbeMessages(found []mailengine.ProbeMessage, recorded map[string]string, now time.Time, leftovers bool) (map[string]time.Time, map[uint32]bool) {
	arrivals := map[string]time.Time{}
	remove := map[uint32]bool{}
	for _, m := range found {
		address, ok := recorded[m.Token]
		if ok && strings.EqualFold(address, m.From) {
			if prev, seen := arrivals[m.Token]; !seen || (!m.ReceivedAt.IsZero() && m.ReceivedAt.Before(prev)) {
				arrivals[m.Token] = m.ReceivedAt
			}
			remove[m.UID] = true
			continue
		}
		if leftovers && !m.ReceivedAt.IsZero() && now.Sub(m.ReceivedAt) >= ProbeLeftoverAge {
			remove[m.UID] = true
		}
	}
	return arrivals, remove
}

// formatProbeDelay renders seconds as "1h05m" / "12m" (job progress and
// results; the Web UI and the mails format it themselves).
func formatProbeDelay(seconds int64) string {
	m := seconds / 60
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}

// ResolveProbe closes an open probe mail by hand (by: the username) with a
// reason (models.ProbeResolveReasons) and optional details. A mail that is
// not open any more is refused (result.probe.notOpen).
func ResolveProbe(db *sql.DB, id int64, by, reason, note string) (*models.ProbeMail, error) {
	p, err := models.GetProbeMail(db, id)
	if err == sql.ErrNoRows {
		return nil, wording.New("system.notFound", "probe mail not found")
	}
	if err != nil {
		return nil, err
	}
	known := false
	for _, r := range models.ProbeResolveReasons {
		if r == reason {
			known = true
		}
	}
	if !known {
		return nil, wording.New("validation.probe.reasonRequired", "choose why the probe mail is resolved")
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > MaxProbeNoteLength {
		return nil, wording.New("validation.group.stateNoteTooLong", fmt.Sprintf("note must be %d characters or fewer", MaxProbeNoteLength)).
			With("max", MaxProbeNoteLength)
	}
	ok, err := models.ResolveProbeMail(db, p.ID, by, reason, note, time.Now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, wording.New("result.probe.notOpen", "the probe mail is not open any more (it arrived or was resolved)")
	}
	return models.GetProbeMail(db, id)
}

// --- Failure mail ---

// ProbeFailureEntry is one failed probe mail in the failure mail.
type ProbeFailureEntry struct {
	Address    string
	Slot       string // server time, as in the subject
	Manual     bool
	SentAt     string // recipient's time zone
	SendFailed bool
	Error      string
	Hours      int64 // time without arrival
	Minutes    int64
}

// notifyProbeFailures mails the failures not mailed yet (ProbeStatusFailure
// and ProbeStatusSendFailed) to the notification recipients, in each
// recipient's language and time zone, and marks them mailed when at least
// one recipient got the mail. notify_enabled does not apply (it schedules
// the alert summary); without an SMTP server or recipients nothing is sent
// and the failures stay unmailed. Errors are reported as progress lines
// only: the check itself succeeded.
func notifyProbeFailures(ctx context.Context, db *sql.DB, key []byte, baseURL string, progress func(string)) {
	failures, err := models.ListUnnotifiedProbeFailures(db)
	if err != nil {
		progress(fmt.Sprintf("failure mail: %v", err))
		return
	}
	if len(failures) == 0 {
		return
	}
	s, err := ResolveNotificationSettings(db, key)
	if err != nil {
		progress(fmt.Sprintf("failure mail: %v", err))
		return
	}
	if !s.SMTP.Configured() {
		progress("failure mail: skipped: " + SkipSMTPNotConfigured)
		return
	}
	selected, _, err := NotificationRecipients(db, s)
	if err != nil {
		progress(fmt.Sprintf("failure mail: %v", err))
		return
	}
	recipients := notificationTargets(selected)
	if len(recipients) == 0 {
		progress("failure mail: skipped: " + SkipRecipientsNoEmail)
		return
	}
	if baseURL == "" {
		baseURL = EffectiveBaseURL(db, s)
	}
	delivered := 0
	for _, u := range recipients {
		loc := MailLocaleFor(u)
		subject, body, err := renderProbeFailureMail(failures, baseURL, loc)
		if err != nil {
			progress(fmt.Sprintf("failure mail: %v", err))
			return
		}
		if err := SendMail(ctx, s.SMTP, s.SMTP.From, []string{u.Email}, subject, body); err != nil {
			progress(fmt.Sprintf("failure mail to %s: %v", u.Email, err))
			continue
		}
		delivered++
	}
	if delivered == 0 {
		return
	}
	ids := make([]int64, 0, len(failures))
	for _, p := range failures {
		ids = append(ids, p.ID)
	}
	if err := models.MarkProbeMailsNotified(db, ids, time.Now()); err != nil {
		progress(fmt.Sprintf("failure mail: %v", err))
		return
	}
	progress(fmt.Sprintf("failure mail sent to %d recipients (%d probe mails)", delivered, len(failures)))
}

// renderProbeFailureMail renders the failure mail for one locale.
func renderProbeFailureMail(failures []*models.ProbeMail, baseURL string, loc MailLocale) (string, string, error) {
	entries := make([]ProbeFailureEntry, 0, len(failures))
	for _, p := range failures {
		e := ProbeFailureEntry{Address: p.Address, Slot: p.Slot, Manual: p.Manual, SentAt: p.SentAt.In(loc.Location).Format("2006-01-02 15:04"),
			SendFailed: p.Status == models.ProbeStatusSendFailed, Error: p.ErrorMessage}
		e.Hours, e.Minutes = p.DelaySeconds/3600, p.DelaySeconds%3600/60
		entries = append(entries, e)
	}
	return renderSubjectAndBody(mailTemplatePath(probeFailureTemplateKind, loc.Language), map[string]any{
		"Date": time.Now().In(loc.Location).Format("2006-01-02 15:04"), "Total": len(entries), "Entries": entries,
		"URL": strings.TrimRight(baseURL, "/") + "/alerts/probes", "BaseURL": baseURL,
	})
}
