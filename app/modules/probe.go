package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
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
//   - While a mail is missing, the next checks run every DelayMinutes
//     after the latest send up to FailureMinutes after it, then every
//     FailureMinutes (NextProbeCheckAt), or earlier when a missing mail
//     reaches its next level before (probeLevelCheckAt).
//   - The next send replaces the pending check: its first check runs
//     DelayMinutes after it and covers the older mails as well, so the
//     number of checks never grows.
//   - A mail that arrives after it was marked (or that arrived later than
//     the delay threshold) is "recovered" and keeps the level it reached;
//     a user can close a missing mail by hand with a reason ("resolved").
//   - A mail is mailed to the notification recipients once when it becomes
//     delayed and once when it becomes a failure (or when the SMTP server
//     refused it); one that went straight to failure only as a failure
//     (notifyProbeAlerts).
//   - Every check lists the probe mails of the probe mailbox once (the
//     subjects that carry ProbeSubjectMark, every identifier, the whole
//     folder; mailengine.CheckProbeServer) and decides both the arrivals
//     and the deletions from that one listing (sortProbeMessages).
//   - A verified probe mail (the subject and the probe header carry the
//     same token, the token is recorded and the sender is the recorded
//     address) is an arrival when its record is waiting, and is deleted
//     from the probe server at once, whatever its record (the probe mailbox
//     would fill up otherwise); one left there (the deletion failed, it
//     arrived after it was resolved) goes with the next check.
//   - Any other probe mail (no record, or a closed record with another
//     sender) is deleted when it carries the current identifier, arrived
//     ProbeLeftoverMinAge ago or earlier and its token is no open record.
//     A probe mail of another identifier without a record, and any other
//     mail of the probe mailbox, is never touched.
//
// The probe settings live in the settings table (SettingProbe*), the SMTP
// connection of a mailbox in its detail_info (smtp_host and the rest), the
// probe mails in the probe_mails table of the master database.

// Mail templates of the probe (templates/mail/<kind>_<language>.txt): the
// body of a probe mail, the alert mail (delays and failures) and the mail
// about an unreadable probe server to the notification recipients.
const (
	probeTemplateKind           = "probe"
	probeAlertTemplateKind      = "probe_alert"
	probeUnreadableTemplateKind = "probe_unreadable"
)

// errProbeAddressInvalid is returned for a probe address that is not a mail
// address.
var errProbeAddressInvalid = wording.New("validation.common.emailFormat", "probe_address is not a valid mail address")

// ErrProbeIMAPPasswordRequired is returned when the probe server connection
// changes while a password is stored and no password is given.
var ErrProbeIMAPPasswordRequired = wording.New("validation.connection.passwordRequiredOnChange", "probe_imap_password is required when the connection settings change")

// errSMTPPasswordRequired is returned by the test of the send check when a
// username is given and no password is given or stored.
var errSMTPPasswordRequired = wording.New("validation.connection.passwordRequired", "smtp_password is required")

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
	// LastDeleteError is why the verified probe mails could not be deleted
	// at the latest check that read the server ("" when they were).
	LastDeleteError string
	// CheckFailures counts the checks that failed in a row since
	// FailingSince; UnreadableNotifiedAt is when the one mail about it went
	// out (see CheckProbes).
	CheckFailures        int
	FailingSince         sql.NullTime
	UnreadableNotifiedAt sql.NullTime
	// SubjectTag is the identifier at the start of the subject of the probe
	// mails ("[<identifier>:<token>]").
	SubjectTag string
	// passwordEnc is the stored (encrypted) password, to tell a changed
	// password apart (probeSettingsMoved).
	passwordEnc string
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
	s.passwordEnc = models.GetSetting(db, SettingProbeIMAPPassword)
	if enc := s.passwordEnc; enc != "" {
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
	s.LastDeleteError = models.GetSetting(db, SettingProbeLastDeleteError)
	s.CheckFailures = resolveInt(db, SettingProbeCheckFailures, 0, 0)
	s.FailingSince = settingTime(db, SettingProbeCheckFailingSince)
	s.UnreadableNotifiedAt = settingTime(db, SettingProbeUnreadableNotifiedAt)
	s.SubjectTag = resolveStr(db, SettingProbeSubjectTag, "", DefaultProbeSubjectTag)
	if validateProbeSubjectTag(s.SubjectTag) != nil {
		s.SubjectTag = DefaultProbeSubjectTag
	}
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

// setTime sets an RFC 3339 setting (the probe bookkeeping, read back by
// settingTime); the zero time removes it.
func (u *SettingsUpdate) setTime(key string, t time.Time) {
	if t.IsZero() {
		u.set(key, "", true)
		return
	}
	u.set(key, t.UTC().Format(time.RFC3339), false)
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
	// ClearIMAPPassword removes the stored password (CLI settings set
	// probe_imap_password ""); refused while the probe is on.
	ClearIMAPPassword bool
	// SubjectTag is the identifier at the start of the subject.
	SubjectTag *string
}

// probeSubjectTagRE is an identifier: 1 to 16 letters, digits, dots,
// underscores or hyphens (no brackets, colon or space: it is written
// "[<identifier>:<token>]").
var probeSubjectTagRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,16}$`)

// validateProbeSubjectTag refuses an identifier probeSubjectTagRE does not
// accept.
func validateProbeSubjectTag(tag string) error {
	if !probeSubjectTagRE.MatchString(tag) {
		return wording.New("validation.probe.subjectTagInvalid",
			"probe_subject_tag must be 1 to 16 letters, digits, dots, underscores or hyphens")
	}
	return nil
}

// errProbeOffFirst refuses to remove the probe address, the host, the
// username or the password of the probe server while the probe is on: it
// stays on only while it is fully set up.
var errProbeOffFirst = wording.New("validation.probe.offFirst", "switch the delivery probe off first (probe_enabled 0; its open probe mails are closed)")

// SaveProbeSettings validates every given field and persists them together
// (values equal to the code default delete the row), under the settings
// lock (UpdateSettings, SettingsUpdate.Probe). Switching the probe off
// closes the open probe mails (closeOpenProbes).
func SaveProbeSettings(db *sql.DB, key []byte, in *ProbeInput) error {
	if err := UpdateSettings(db, func(u *SettingsUpdate) error { return u.Probe(db, key, in) }); err != nil {
		return err
	}
	if in.Enabled != nil && !*in.Enabled {
		closeOpenProbes(db, 0)
	}
	return nil
}

// closeOpenProbes resolves the open probe mails of a mailbox (0: of every
// mailbox) as the system (models.ProbeReasonProbeDisabled) once the probe,
// the send check of the mailbox or the mailbox is switched off: no check
// looks for them any more. A failure is logged only (the change itself was
// saved).
func closeOpenProbes(db *sql.DB, mailboxID int64) {
	if _, err := models.CloseOpenProbeMails(db, mailboxID, models.ProbeReasonProbeDisabled, time.Now()); err != nil {
		log.Printf("probe: failed to close the open probe mails: %v", err)
	}
}

// closeStaleOpenProbes closes the open probe mails no check looks for any
// more: all of them while the probe is off, else those of the mailboxes
// that no longer send (ProbeParticipates). It covers a switch-off that
// raced a send (CheckProbes starts and SendProbes ends with it).
func closeStaleOpenProbes(db *sql.DB) {
	if s, err := ResolveProbeSettings(db, nil); err == nil && !s.Enabled {
		closeOpenProbes(db, 0)
		return
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		log.Printf("probe: failed to list the mailboxes: %v", err)
		return
	}
	for _, mb := range mailboxes {
		if !ProbeParticipates(mb) {
			closeOpenProbes(db, mb.ID)
		}
	}
}

// Probe sets the given probe settings, checked against the saved ones. The
// failure threshold must be longer than the delay threshold. While a
// password is stored, an update that changes the host, port, security mode
// or username must carry the password too (ErrProbeIMAPPasswordRequired
// otherwise). Enabling the probe needs the probe server to be set up.
func (u *SettingsUpdate) Probe(db *sql.DB, key []byte, in *ProbeInput) error {
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
		folder, err := normalizeProbeFolder(*in.IMAPFolder)
		if err != nil {
			return err
		}
		writes = append(writes, settingWrite{SettingProbeIMAPFolder, folder, folder == "INBOX"})
	}
	passwordGiven := in.IMAPPassword != nil && *in.IMAPPassword != ""
	if in.ClearIMAPPassword && !passwordGiven {
		next.PasswordSet = false
		writes = append(writes, settingWrite{SettingProbeIMAPPassword, "", true})
	}
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
	if in.SubjectTag != nil {
		// Not part of the connection: changing it does not end a run of
		// failed checks, and the mails sent before stay traceable (the
		// identifier is not compared for them).
		tag := strings.TrimSpace(*in.SubjectTag)
		if err := validateProbeSubjectTag(tag); err != nil {
			return err
		}
		writes = append(writes, settingWrite{SettingProbeSubjectTag, tag, tag == DefaultProbeSubjectTag})
	}
	if in.SendTimes != nil {
		times, err := ParseCheckTimes(*in.SendTimes)
		if err != nil {
			return err
		}
		if err := validateProbeSendTimes(times); err != nil {
			return err
		}
		next.SendTimes = times
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
	if in.SendTimes != nil || in.DelayMinutes != nil {
		// The first check of a send runs the delay threshold after it; the
		// next send would replace it before it ran.
		if gap, ok := minProbeSendGap(next.SendTimes); ok && next.DelayMinutes >= gap {
			return wording.New("validation.probe.delayNotBeforeNextSend",
				fmt.Sprintf("probe_delay_minutes must be shorter than the shortest gap between the send times (%d minutes)", gap)).
				With("max", gap-1)
		}
	}
	if !passwordGiven && saved.PasswordSet && probeConnectionChanged(&saved.Conn, &next.Conn) {
		return ErrProbeIMAPPasswordRequired
	}
	enabled := saved.Enabled
	if in.Enabled != nil {
		if *in.Enabled && !next.Configured() {
			return ErrProbeNotConfigured
		}
		enabled = *in.Enabled
		writes = append(writes, settingWrite{SettingProbeEnabled, "1", !*in.Enabled})
	}
	if enabled && in.Enabled == nil && !next.Configured() {
		return errProbeOffFirst
	}
	// A run of failed checks belongs to the server it was measured on and
	// to the probe being on: switching the probe on or off, or another
	// connection, starts afresh.
	folderChanged := false
	if in.IMAPFolder != nil {
		folder, _ := normalizeProbeFolder(*in.IMAPFolder) // validated above
		folderChanged = folder != saved.Conn.Folder
	}
	if enabled != saved.Enabled || probeConnectionChanged(&saved.Conn, &next.Conn) || passwordGiven || in.ClearIMAPPassword || folderChanged {
		writes = append(writes, probeFailureRunReset()...)
	}
	u.writes = append(u.writes, writes...)
	return nil
}

// probeFailureRunReset are the writes that forget a run of failed arrival
// checks (and its error and mail).
func probeFailureRunReset() []settingWrite {
	return []settingWrite{
		{SettingProbeCheckFailures, "", true}, {SettingProbeCheckFailingSince, "", true},
		{SettingProbeUnreadableNotifiedAt, "", true}, {SettingProbeLastCheckError, "", true},
	}
}

// normalizeProbeFolder trims the folder of the probe server ("" = INBOX) and
// refuses a value with a line break (SaveProbeSettings, TestProbeServer).
func normalizeProbeFolder(folder string) (string, error) {
	folder = strings.TrimSpace(folder)
	if strings.ContainsAny(folder, "\r\n") {
		return "", wording.New("validation.mailbox.folderInvalid", "probe_imap_folder is not a valid folder name")
	}
	if folder == "" {
		folder = "INBOX"
	}
	return folder, nil
}

// validateProbeSendTimes bounds the send times (already normalized and
// sorted, ParseCheckTimes): at most MaxProbeSendTimes, at least
// MinProbeSendGapMinutes apart (across midnight too).
func validateProbeSendTimes(times []string) error {
	if len(times) > MaxProbeSendTimes {
		return wording.New("validation.probe.sendTimesTooMany",
			fmt.Sprintf("at most %d probe send times", MaxProbeSendTimes)).With("max", MaxProbeSendTimes)
	}
	if gap, ok := minProbeSendGap(times); ok && gap < MinProbeSendGapMinutes {
		return wording.New("validation.probe.sendTimesTooClose",
			fmt.Sprintf("the probe send times must be at least %d minutes apart", MinProbeSendGapMinutes)).
			With("min", MinProbeSendGapMinutes)
	}
	return nil
}

// minProbeSendGap returns the shortest gap in minutes between two send times
// that follow each other (HH:MM, sorted), the wrap past midnight included
// (24 hours for a single time); false without a send time.
func minProbeSendGap(times []string) (int, bool) {
	if len(times) == 0 {
		return 0, false
	}
	mins := make([]int, 0, len(times))
	for _, t := range times {
		var h, m int
		if _, err := fmt.Sscanf(t, "%d:%d", &h, &m); err != nil {
			continue
		}
		mins = append(mins, h*60+m)
	}
	if len(mins) == 0 {
		return 0, false
	}
	slices.Sort(mins)
	gap := 24*60 - mins[len(mins)-1] + mins[0]
	for i := 1; i < len(mins); i++ {
		gap = min(gap, mins[i]-mins[i-1])
	}
	return gap, true
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
		folder, err := normalizeProbeFolder(*in.IMAPFolder)
		if err != nil {
			return err
		}
		conn.Folder = folder
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
// checkedAt is due while probe mails are still missing: every DelayMinutes
// after the latest send up to FailureMinutes after it (15, 30, 45 and 60
// minutes with the defaults), then every FailureMinutes (120, 180, ...).
// The steps are counted from the latest send, so a check run by hand in
// between keeps them. Without a recorded send it is FailureMinutes after
// the check.
func NextProbeCheckAt(lastSend, checkedAt time.Time, delay, failure time.Duration) time.Time {
	if failure <= 0 {
		failure = time.Duration(DefaultProbeFailureMinutes) * time.Minute
	}
	if lastSend.IsZero() {
		return checkedAt.Add(failure)
	}
	elapsed := max(checkedAt.Sub(lastSend), 0)
	if delay > 0 && delay < failure {
		// The next whole delay step, while it comes before the failure
		// threshold.
		if step := (elapsed/delay + 1) * delay; step < failure {
			return lastSend.Add(step)
		}
	}
	if elapsed < failure {
		return lastSend.Add(failure)
	}
	// Whole failure periods since the latest send, then one more.
	return lastSend.Add((elapsed/failure + 1) * failure)
}

// probeLevelCheckAt returns when the check after one at now runs, given the
// regular next check (next, NextProbeCheckAt) and the moments the missing
// probe mails reach their next level (moments; past ones are ignored): the
// first such moment when it comes before next. The moments (and next)
// within ProbeCheckCoalesce of it are checked together, a little later, so
// the mails of one send (sent over some seconds) do not get one check each.
func probeLevelCheckAt(next time.Time, moments []time.Time, now time.Time) time.Time {
	var first time.Time
	for _, m := range moments {
		if m.After(now) && (first.IsZero() || m.Before(first)) {
			first = m
		}
	}
	if first.IsZero() || !first.Before(next) {
		return next
	}
	at := first
	for _, m := range moments {
		if m.After(at) && m.Sub(first) <= ProbeCheckCoalesce {
			at = m
		}
	}
	if next.After(at) && next.Sub(first) <= ProbeCheckCoalesce {
		at = next
	}
	return at
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

// newProbeToken returns a fresh token: 16 lower-case hex digits (8 random
// bytes).
func newProbeToken() (string, error) {
	return RandomHex(8)
}

// probeSubject builds the subject of a probe mail:
// "[<identifier>:<token>] Probe <slot> <address>" (ASCII only, so no server
// has to decode it to find the token; it names the date and time of the
// send (the slot, else the time of sending, server local time) and the
// sending address, so a missing mail can be traced by its subject alone).
func probeSubject(tag, slotLabel, address, token string) string {
	return fmt.Sprintf("[%s:%s] Probe %s %s", tag, token, slotLabel, address)
}

// probeMailContent renders the probe mail of a mailbox (the body in the
// server's default language; the subject is fixed, see probeSubject). A test
// mail carries "[<identifier>] Probe test" and no token.
func probeMailContent(mb *models.Mailbox, tag, slotLabel, token string, now time.Time, test bool) (OutgoingMail, error) {
	loc := DefaultMailLocale()
	_, body, err := renderSubjectAndBody(mailTemplatePath(probeTemplateKind, loc.Language), map[string]any{
		"Address": mb.Address, "Slot": slotLabel, "SentAt": now.In(loc.Location).Format("2006-01-02 15:04:05 -0700"),
		"Token": token, "Test": test,
	})
	if err != nil {
		return OutgoingMail{}, err
	}
	subject := probeSubject(tag, slotLabel, mb.Address, token)
	if test {
		subject = fmt.Sprintf("[%s] Probe test %s %s", tag, slotLabel, mb.Address)
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
// input names a stored mailbox, its address and the same connection.
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
			return errSMTPPasswordRequired
		}
		stored, err := models.GetMailboxByID(db, in.ID)
		if err == sql.ErrNoRows {
			return wording.New("system.notFound", "mailbox not found")
		}
		if err != nil {
			return err
		}
		if stored.SmtpPasswordEnc == "" {
			return errSMTPPasswordRequired
		}
		// The stored password serves only the stored connection of the
		// stored address.
		if !sameSMTPConnection(stored, in) || !strings.EqualFold(stored.Address, in.Address) {
			return ErrSMTPConnectionPasswordRequired
		}
		mb.SmtpPasswordEnc = stored.SmtpPasswordEnc
	}
	cfg, err := probeSMTPConfig(key, mb)
	if err != nil {
		return err
	}
	now := time.Now()
	mail, err := probeMailContent(mb, settings.SubjectTag, probeSlotLabel("", now), "", now, true)
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
	SkipProbeDisabled      = "the delivery probe is off"
	SkipProbeNoMailbox     = "no enabled mailbox has the send check on"
	SkipProbeNothingToSee  = "no probe mail is waiting"
	SkipProbeCheckNotDue   = "the arrival check is not due"
)

// ErrProbeDisabled refuses a send by hand while the probe is switched off.
var ErrProbeDisabled = wording.New("result.probe.disabled", "the delivery probe is off")

// ErrProbeNoMailbox refuses a send by hand while no mailbox sends probe
// mails, and ErrProbeNothingOpen a check by hand while no probe mail is
// open (either job would do nothing).
var (
	ErrProbeNoMailbox   = wording.New("result.probe.noMailbox", SkipProbeNoMailbox)
	ErrProbeNothingOpen = wording.New("result.probe.nothingOpen", SkipProbeNothingToSee)
)

// ProbeParticipantCount counts the mailboxes that send probe mails
// (ProbeParticipates).
func ProbeParticipantCount(db *sql.DB) (int, error) {
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, mb := range mailboxes {
		if ProbeParticipates(mb) {
			n++
		}
	}
	return n, nil
}

// ProbeJobRefusal returns why a probe_send or probe_check asked for now
// would do nothing (nil when it would not, and for every other kind): the
// probe is off or not set up, no mailbox sends probe mails (probe_send) or
// no probe mail is open (probe_check).
func ProbeJobRefusal(db *sql.DB, kind string) error {
	if kind != JobKindProbeSend && kind != JobKindProbeCheck {
		return nil
	}
	s, err := ResolveProbeSettings(db, nil)
	if err != nil {
		return err
	}
	switch {
	case !s.Enabled:
		return ErrProbeDisabled
	case !s.Configured():
		return ErrProbeNotConfigured
	}
	if kind == JobKindProbeSend {
		n, err := ProbeParticipantCount(db)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrProbeNoMailbox
		}
		return nil
	}
	n, err := models.CountOpenProbeMails(db)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrProbeNothingOpen
	}
	return nil
}

// ClearProbeCheck does what an arrival check with nothing open does
// (recordProbeCheck): it removes the next check and ends a run of failed
// checks. The scheduler calls it instead of queueing such a check.
func ClearProbeCheck(db *sql.DB) error {
	return UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeNextCheckAt, time.Time{})
		u.writes = append(u.writes, probeFailureRunReset()...)
		return nil
	})
}

// SendProbes sends one probe mail from every participating mailbox to the
// probe address and records them (slot: "YYYY-MM-DD HH:MM" of the send
// time, "" when sent by hand). Nothing is sent while the probe is switched
// off. The arrival check is scheduled DelayMinutes after the start of the
// sending before the first mail goes out (so no earlier check runs in
// between), and DelayMinutes after its end once the sending is over; this
// replaces a pending check (it covers every missing mail). A mail the SMTP
// server refused is recorded as send_failed. The probe and each mailbox are
// read again right before its mail goes out (a switch-off during the send
// skips the rest), and the run ends by closing the open probe mails of the
// mailboxes that no longer send (closeStaleOpenProbes) and with the alert
// mail of the delays and failures not mailed yet (notifyProbeAlerts).
func SendProbes(ctx context.Context, db *sql.DB, key []byte, baseURL, slot string, progress func(string)) (*ProbeSendResult, error) {
	settings, err := ResolveProbeSettings(db, nil)
	if err != nil {
		return nil, err
	}
	res := &ProbeSendResult{}
	switch {
	case !settings.Configured():
		res.Skipped = SkipProbeNotConfigured
		return res, nil
	case !settings.Enabled:
		res.Skipped = SkipProbeDisabled
		return res, nil
	}
	defer notifyProbeAlerts(ctx, db, key, baseURL, progress)
	defer closeStaleOpenProbes(db)
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
	start := time.Now()
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeNextCheckAt, start.Add(settings.Delay()))
		return nil
	}); err != nil {
		return res, err
	}
	for _, mb := range targets {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		err := sendOneProbe(ctx, db, key, mb, settings.Address, slot)
		if errors.Is(err, errProbeSendSkipped) {
			progress(fmt.Sprintf("%s: skipped: %v", mb.Address, err))
			continue
		}
		if err != nil {
			res.Failed++
			progress(fmt.Sprintf("%s: %v", mb.Address, err))
			continue
		}
		res.Sent++
		progress(fmt.Sprintf("%s: sent", mb.Address))
	}
	end := time.Now()
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeLastSendAt, end)
		u.setTime(SettingProbeNextCheckAt, end.Add(settings.Delay()))
		return nil
	}); err != nil {
		return res, err
	}
	return res, nil
}

// errProbeSendSkipped is returned by sendOneProbe when the probe or the
// mailbox was switched off since the send started (nothing is recorded).
var errProbeSendSkipped = errors.New("the probe or the send check of the mailbox was switched off")

// sendOneProbe sends and records the probe mail of one mailbox. The probe
// and the mailbox are read again first: switched off meanwhile, nothing is
// sent (errProbeSendSkipped). A mail that could not be sent is recorded too
// (send_failed) and its error returned.
func sendOneProbe(ctx context.Context, db *sql.DB, key []byte, mb *models.Mailbox, to, slot string) error {
	settings, err := ResolveProbeSettings(db, nil)
	if err != nil {
		return err
	}
	if !settings.Enabled || !settings.Configured() {
		return errProbeSendSkipped
	}
	current, err := models.GetMailboxByID(db, mb.ID)
	if err == sql.ErrNoRows {
		return errProbeSendSkipped
	}
	if err != nil {
		return err
	}
	if !ProbeParticipates(current) {
		return errProbeSendSkipped
	}
	mb = current
	token, err := newProbeToken()
	if err != nil {
		return err
	}
	now := time.Now()
	label := probeSlotLabel(slot, now)
	p := &models.ProbeMail{MailboxID: mb.ID, Address: mb.Address, Token: token, Slot: label, Manual: slot == "",
		Subject: probeSubject(settings.SubjectTag, label, mb.Address, token), Status: models.ProbeStatusPending}
	sendErr := func() error {
		cfg, err := probeSMTPConfig(key, mb)
		if err != nil {
			return err
		}
		mail, err := probeMailContent(mb, settings.SubjectTag, label, token, now, false)
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

// ProbeCheckResult reports what CheckProbes did.
type ProbeCheckResult struct {
	Skipped   string
	Checked   int // waiting mails looked for (a mail resolved during the check is not counted)
	Arrived   int // arrived within the delay threshold
	Recovered int // arrived late (also a send_failed mail that arrived after all)
	Delayed   int // newly delayed
	Failed    int // newly failed
	Missing   int // still missing after the check
	Deleted   int // probe mails deleted from the probe server (verified and unrecorded)
	// Mismatched counts the probe mails whose token is recorded but whose
	// sender is not the recorded address (left on the server).
	Mismatched int
	// Unrecorded counts the probe mails deleted under the rule for the
	// unrecorded ones (current identifier, ProbeLeftoverMinAge old, no open
	// record).
	Unrecorded int
	// DeleteError is why the verified probe mails could not be deleted
	// from the probe server ("" when they were, or none had to be).
	DeleteError string
	NextAt      time.Time
	// levelAts are the moments the mails still missing reach their next
	// level (the delay threshold, the failure threshold; probeLevelCheckAt).
	levelAts []time.Time
}

// probeRetryAfter returns when an arrival check is tried again after the
// failures-th failed check in a row: ProbeCheckRetryInterval, doubled for
// every further failure, at most the failure threshold, but never less than
// ProbeCheckRetryInterval (also when the failure threshold is shorter).
func probeRetryAfter(failures int, failure time.Duration) time.Duration {
	limit := max(failure, ProbeCheckRetryInterval)
	d := ProbeCheckRetryInterval
	for i := 1; i < failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// errProbeRecord marks an error of recording what a check found (the
// database), not of the probe server.
var errProbeRecord = errors.New("recording the arrival check")

// CheckProbes looks for every waiting probe mail on the probe server and
// updates them (ApplyProbeCheck), deletes the verified probe mails from the
// probe server (sortProbeMessages), mails the new delays and failures
// (notifyProbeAlerts) and schedules the next check (NextProbeCheckAt, or
// earlier when a missing mail reaches its next level before:
// probeLevelCheckAt; none when nothing is missing). It starts by
// closing the open probe mails no check looks for any more
// (closeStaleOpenProbes). When the probe server cannot be read the check is
// tried again after probeRetryAfter, and once it stayed unreadable for the
// failure threshold the notification recipients get one mail about it
// (notifyProbeUnreadable); a check that reads the server, a check with
// nothing to look for, switching the probe on or off and another connection
// end that run. Errors that are not the probe server's (the database, the
// settings, a canceled job) are returned as well but do not count in the
// run. With no open probe mail no check reads the server, but the alert
// mails not sent yet are tried again. With only send_failed mails open the
// server is read (a late arrival recovers them) but no further check is
// scheduled: Missing and NextAt count the waiting mails only, so such mails
// are looked for once after every send.
func CheckProbes(ctx context.Context, db *sql.DB, key []byte, baseURL string, progress func(string)) (res *ProbeCheckResult, err error) {
	res = &ProbeCheckResult{}
	now := time.Now()
	var settings *ProbeSettings
	serverErr := false
	defer func() {
		if settings != nil && settings.Configured() {
			notifyProbeAlerts(ctx, db, key, baseURL, progress)
		}
		recordProbeCheck(ctx, db, key, baseURL, settings, res, err, serverErr, now, progress)
	}()
	closeStaleOpenProbes(db)
	settings, err = ResolveProbeSettings(db, key)
	if err != nil {
		return res, err
	}
	if !settings.Configured() {
		res.Skipped = SkipProbeNotConfigured
		return res, nil
	}
	open, err := models.ListOpenProbeMails(db)
	if err != nil {
		return res, err
	}
	if len(open) == 0 {
		res.Skipped = SkipProbeNothingToSee
		return res, nil
	}
	// The waiting mails are judged (missing, delayed, failed); a
	// send_failed one is only looked for, in case it arrived after all.
	waiting, err := models.ListWaitingProbeMails(db)
	if err != nil {
		return res, err
	}
	progress(fmt.Sprintf("looking for %d probe mails on %s", len(open), settings.Conn.ImapHost))
	res.Checked = len(waiting)
	var sorted probeSort
	// The arrivals are recorded before anything is deleted from the server:
	// a message is removed only once its arrival is in the database.
	record := func(found []mailengine.ProbeMessage) (map[uint32]bool, error) {
		now = time.Now()
		tokens := make([]string, 0, len(found))
		for _, m := range found {
			tokens = append(tokens, m.Token)
		}
		recorded, err := models.ProbeMailsByToken(db, tokens)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errProbeRecord, err)
		}
		sorted = sortProbeMessages(found, recorded, settings.SubjectTag, now)
		if err := recordProbeArrivals(db, waiting, recorded, sorted, now, settings, res, progress); err != nil {
			return nil, fmt.Errorf("%w: %w", errProbeRecord, err)
		}
		return sorted.remove, nil
	}
	// One listing of the probe mails decides the arrivals and the deletions.
	deleted, err := mailengine.CheckProbeServer(ctx, &settings.Conn, settings.Password, ProbeHeader, ProbeSubjectMark, record)
	// What was deleted counts, also the batches deleted before a failed
	// one; the records it empties are marked deleted.
	gone, unrecorded := sorted.deletedOf(deleted)
	res.Deleted, res.Unrecorded = len(deleted), unrecorded
	if len(gone) > 0 {
		if merr := models.MarkProbeMailsDeleted(db, gone, time.Now()); merr != nil {
			progress(fmt.Sprintf("could not record the deletion: %v", merr))
		}
	}
	switch {
	case errors.Is(err, mailengine.ErrProbeDelete):
		// The check itself succeeded; the next one deletes them.
		res.DeleteError = err.Error()
		progress(err.Error())
		err = nil
	case err != nil:
		serverErr = !errors.Is(err, errProbeRecord) && ctx.Err() == nil
		return res, fmt.Errorf("probe server %s: %w", settings.Conn.ImapHost, err)
	}
	if res.Deleted > 0 {
		progress(fmt.Sprintf("deleted %d probe mail(s) from the probe server (%d unrecorded)", res.Deleted, res.Unrecorded))
	}
	if res.Missing > 0 {
		res.NextAt = NextProbeCheckAt(settings.LastSendAt.Time, now, settings.Delay(), settings.Failure())
		res.NextAt = probeLevelCheckAt(res.NextAt, res.levelAts, now)
	}
	return res, nil
}

// recordProbeCheck saves the outcome of an arrival check in one settings
// write. A check with nothing to look for (skipped) clears the next check
// and ends a run of failed checks. A check that read the probe server
// records its time and ends the run, and keeps the deletion error (also
// when the settings changed during the check). A check the probe server failed
// (serverErr) records its time and error, counts the run (probeRetryAfter
// decides the next check; notifyProbeUnreadable mails once about a server
// unreadable for the failure threshold). Any other error (the database, the
// settings) records the error and retries after ProbeCheckRetryInterval
// without touching the run; a canceled job only keeps a check pending.
func recordProbeCheck(ctx context.Context, db *sql.DB, key []byte, baseURL string, settings *ProbeSettings,
	res *ProbeCheckResult, checkErr error, serverErr bool, now time.Time, progress func(string)) {
	canceled := checkErr != nil && (errors.Is(checkErr, context.Canceled) || ctx.Err() != nil)
	failures, failingSince, notified := 1, now, false
	if settings != nil {
		failures = settings.CheckFailures + 1
		if settings.FailingSince.Valid {
			failingSince = settings.FailingSince.Time
		}
		notified = settings.UnreadableNotifiedAt.Valid
	}
	switch {
	case canceled:
		res.NextAt = now.Add(ProbeCheckRetryInterval)
	case serverErr:
		res.NextAt = now.Add(probeRetryAfter(failures, settings.Failure()))
		if settings.Configured() && !notified && now.Sub(failingSince) >= settings.Failure() {
			notified = notifyProbeUnreadable(ctx, db, key, baseURL, settings, failingSince, checkErr, progress)
		}
	case checkErr != nil:
		res.NextAt = now.Add(ProbeCheckRetryInterval)
	}
	if err := UpdateSettings(db, func(u *SettingsUpdate) error {
		u.setTime(SettingProbeNextCheckAt, res.NextAt)
		if checkErr == nil && res.Skipped == "" {
			// What the deletion on the server read did (the next check
			// retries a failed one). The time of the daily sweep of older
			// versions goes.
			u.set(SettingProbeLastDeleteError, res.DeleteError, res.DeleteError == "")
			u.set(SettingProbeWideSweepAt, "", true)
		}
		// The settings may have changed while the check ran (the probe
		// switched, another connection, another check's run): the run of
		// failed checks then belongs to them, not to this check.
		if settings != nil && (serverErr || checkErr == nil) {
			if cur, err := ResolveProbeSettings(db, nil); err != nil || probeSettingsMoved(settings, cur) {
				return nil
			}
		}
		switch {
		case canceled:
		case serverErr:
			u.setTime(SettingProbeLastCheckAt, now)
			u.set(SettingProbeLastCheckError, checkErr.Error(), false)
			u.set(SettingProbeCheckFailures, strconv.Itoa(failures), false)
			u.setTime(SettingProbeCheckFailingSince, failingSince)
			if notified && !settings.UnreadableNotifiedAt.Valid {
				u.setTime(SettingProbeUnreadableNotifiedAt, now)
			}
		case checkErr != nil:
			u.set(SettingProbeLastCheckError, checkErr.Error(), false)
		case res.Skipped != "":
			u.writes = append(u.writes, probeFailureRunReset()...)
		default:
			u.setTime(SettingProbeLastCheckAt, now)
			u.writes = append(u.writes, probeFailureRunReset()...)
		}
		return nil
	}); err != nil {
		log.Printf("probe: failed to record the check: %v", err)
	}
}

// probeSettingsMoved reports whether the probe settings changed in a way
// that ends a run of failed checks (switched on or off, another connection
// or password, or another check counted meanwhile) between before and now.
func probeSettingsMoved(before, now *ProbeSettings) bool {
	return before.Enabled != now.Enabled || probeConnectionChanged(&before.Conn, &now.Conn) ||
		before.Conn.Folder != now.Conn.Folder || before.passwordEnc != now.passwordEnc || before.CheckFailures != now.CheckFailures
}

// probeSenderMismatch is the error text of a waiting probe mail whose token
// was found with another sender (a relay that rewrites From, for example):
// shown to administrators on the probe row and in the job progress.
func probeSenderMismatch(recorded, found string) string {
	if found == "" {
		found = "(none)"
	}
	return fmt.Sprintf("token matched, sender differs (%s vs %s)", recorded, found)
}

// recordProbeArrivals applies what the probe server showed to the waiting
// probe mails (ApplyProbeCheck) and to the send_failed mails that arrived
// after all, in one transaction, and counts the outcome in res. A row
// resolved while the check ran is left as it is and not counted (the update
// is guarded by its status, models.UpdateProbeMailCheck).
func recordProbeArrivals(db *sql.DB, waiting []*models.ProbeMail, recorded map[string]*models.ProbeMail, sorted probeSort,
	now time.Time, settings *ProbeSettings, res *ProbeCheckResult, progress func(string)) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for token, found := range sorted.mismatched {
		// Only the open rows matter: a leftover of a closed one is no news.
		if p := recorded[token]; p != nil && models.ProbeStatusOpen(p.Status) {
			res.Mismatched++
			progress(fmt.Sprintf("%s (%s): %s", p.Address, p.Slot, probeSenderMismatch(p.Address, found)))
		}
	}
	for _, p := range waiting {
		arrivedAt := sorted.arrivals[p.Token]
		switch found, ok := sorted.mismatched[p.Token]; {
		case !arrivedAt.IsZero():
			p.ErrorMessage = ""
		case ok:
			p.ErrorMessage = probeSenderMismatch(p.Address, found)
		}
		newLevel := ApplyProbeCheck(p, arrivedAt, now, settings.Delay(), settings.Failure())
		updated, err := models.UpdateProbeMailCheck(tx, p)
		if err != nil {
			return err
		}
		if !updated {
			res.Checked--
			continue
		}
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
			switch p.Level {
			case models.ProbeLevelNone:
				res.levelAts = append(res.levelAts, p.SentAt.Add(settings.Delay()))
			case models.ProbeLevelDelayed:
				res.levelAts = append(res.levelAts, p.SentAt.Add(settings.Failure()))
			}
		}
	}
	// A mail the SMTP server seemed to refuse (send_failed) that arrives
	// after all is recovered; its level (failure) and its error stay.
	for token, arrivedAt := range sorted.arrivals {
		p := recorded[token]
		if p == nil || p.Status != models.ProbeStatusSendFailed {
			continue
		}
		ApplyProbeCheck(p, arrivedAt, now, settings.Delay(), settings.Failure())
		updated, err := models.UpdateProbeMailCheck(tx, p)
		if err != nil {
			return err
		}
		if updated {
			res.Recovered++
			progress(fmt.Sprintf("%s (%s): not sent according to the SMTP server, but arrived", p.Address, p.Slot))
		}
	}
	return tx.Commit()
}

// probeSort is what sortProbeMessages made of the probe mails found:
// arrivals (token -> arrival time), the UIDs to delete, of them the
// verified probe mails (UID -> token; the record is marked once they are
// deleted) and the ones deleted under the rule for the unrecorded ones, and
// the tokens found with another sender than the recorded one (token ->
// sender found).
type probeSort struct {
	arrivals   map[string]time.Time
	remove     map[uint32]bool
	verified   map[uint32]string
	unrecorded map[uint32]bool
	mismatched map[string]string
}

// sortProbeMessages decides what the probe mails found on the probe server
// mean (found: the messages whose subject carries "[<identifier>:<token>]"
// and whose probe header carries that token alone; recorded: the recorded
// probe mails by token, whatever their status; tag: the current
// identifier). A verified probe mail (its token is recorded and its sender
// is the recorded address) is an arrival (the earliest one per token, now
// when the server gave no arrival time; only a waiting or send_failed mail
// takes it) and is deleted at once, also when it is left over from an
// earlier check. Any other probe mail is deleted only when it carries the
// current identifier, arrived ProbeLeftoverMinAge ago or earlier (another
// MailCare sharing the probe server may still be looking for it) and its
// token is no open recorded probe mail (no sender check: an unrecorded mail
// has none to compare). A recorded token with another sender is
// reported (mismatched) when no verified copy of it was found. Every other
// message stays on the server.
func sortProbeMessages(found []mailengine.ProbeMessage, recorded map[string]*models.ProbeMail, tag string, now time.Time) probeSort {
	out := probeSort{arrivals: map[string]time.Time{}, remove: map[uint32]bool{}, verified: map[uint32]string{},
		unrecorded: map[uint32]bool{}, mismatched: map[string]string{}}
	for _, m := range found {
		p, ok := recorded[m.Token]
		if ok && strings.EqualFold(p.Address, m.From) {
			at := m.ReceivedAt
			if at.IsZero() {
				at = now
			}
			if prev, seen := out.arrivals[m.Token]; !seen || at.Before(prev) {
				out.arrivals[m.Token] = at
			}
			out.remove[m.UID] = true
			out.verified[m.UID] = m.Token
			continue
		}
		if ok {
			out.mismatched[m.Token] = m.From
		}
		old := !m.ReceivedAt.IsZero() && now.Sub(m.ReceivedAt) >= ProbeLeftoverMinAge
		if m.Identifier == tag && old && (!ok || !models.ProbeStatusOpen(p.Status)) {
			out.remove[m.UID] = true
			out.unrecorded[m.UID] = true
		}
	}
	for token := range out.arrivals {
		delete(out.mismatched, token)
	}
	return out
}

// deletedOf returns, of the probe mails deleted from the probe server, the
// tokens of the records whose every verified copy found is gone (a partly
// failed deletion leaves the others to be looked for) and how many were
// deleted under the rule for the unrecorded ones.
func (s probeSort) deletedOf(deleted []mailengine.ProbeMessage) (gone []string, unrecorded int) {
	left := map[string]int{}
	for _, token := range s.verified {
		left[token]++
	}
	for _, m := range deleted {
		if s.unrecorded[m.UID] {
			unrecorded++
		}
		if token, ok := s.verified[m.UID]; ok {
			if left[token]--; left[token] == 0 {
				gone = append(gone, token)
			}
		}
	}
	return gone, unrecorded
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

// ResolveProbe closes a delayed, failed or unsent probe mail by hand (by:
// the username) with a reason (models.ProbeResolveReasons) and optional
// details. Any other mail (pending, arrived, resolved) is refused
// (result.probe.notOpen).
func ResolveProbe(db *sql.DB, id int64, by, reason, note string) (*models.ProbeMail, error) {
	p, err := models.GetProbeMail(db, id)
	if err == sql.ErrNoRows {
		return nil, wording.New("system.notFound", "probe mail not found")
	}
	if err != nil {
		return nil, err
	}
	if !slices.Contains(models.ProbeResolveReasons, reason) {
		return nil, wording.New("validation.probe.reasonRequired", "choose why the probe mail is resolved")
	}
	note = strings.TrimSpace(note)
	ok, err := models.ResolveProbeMail(db, p.ID, by, reason, note, time.Now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, wording.New("result.probe.notOpen", "only a delayed, failed or unsent probe mail can be resolved")
	}
	return models.GetProbeMail(db, id)
}

// --- Alert mail ---

// ProbeAlertEntry is one delayed, failed or unsent probe mail in the alert
// mail.
type ProbeAlertEntry struct {
	Address    string
	Slot       string // server time, as in the subject
	Manual     bool
	SentAt     string // recipient's time zone
	SendFailed bool
	Error      string // why it could not be sent ("" for recipients other than administrators)
	Hours      int64  // time without arrival
	Minutes    int64
}

// mailProbeRecipients sends one mail rendered by render (per recipient: the
// recipient's locale and whether the recipient is an administrator, who
// alone gets the error texts) to the notification recipients through the
// SMTP server of the notification settings, and returns how many got it.
// notify_enabled does not apply (it schedules the alert summary); without an
// SMTP server or recipients nothing is sent. Problems are progress lines
// only (what (the kind of mail) starts them).
func mailProbeRecipients(ctx context.Context, db *sql.DB, key []byte, baseURL, what string, progress func(string),
	render func(baseURL string, loc MailLocale, admin bool) (string, string, error)) int {
	s, err := ResolveNotificationSettings(db, key)
	if err != nil {
		progress(fmt.Sprintf("%s: %v", what, err))
		return 0
	}
	if !s.SMTP.Configured() {
		progress(what + ": skipped: " + SkipSMTPNotConfigured)
		return 0
	}
	selected, _, err := NotificationRecipients(db, s)
	if err != nil {
		progress(fmt.Sprintf("%s: %v", what, err))
		return 0
	}
	recipients := notificationTargets(selected)
	if len(recipients) == 0 {
		progress(what + ": skipped: " + SkipRecipientsNoEmail)
		return 0
	}
	if baseURL == "" {
		baseURL = EffectiveBaseURL(db, s)
	}
	delivered := 0
	for _, u := range recipients {
		subject, body, err := render(baseURL, MailLocaleFor(u), u.Role == RoleAdmin)
		if err != nil {
			progress(fmt.Sprintf("%s: %v", what, err))
			return delivered
		}
		if err := SendMail(ctx, s.SMTP, s.SMTP.From, []string{u.Email}, subject, body); err != nil {
			progress(fmt.Sprintf("%s to %s: %v", what, u.Email, err))
			continue
		}
		delivered++
	}
	return delivered
}

// notifyProbeAlerts mails the probe mails not mailed yet at their level to
// the notification recipients (mailProbeRecipients) in one mail: the delays
// (ProbeStatusDelayed, delay not mailed) and the failures
// (ProbeStatusFailure and ProbeStatusSendFailed, failure not mailed). So
// every probe mail is mailed once when it becomes delayed and once when it
// becomes a failure; one that went straight to failure is mailed as a
// failure only, and one that arrived or was resolved before is not mailed.
// They are marked mailed when at least one recipient got the mail;
// otherwise they stay unmailed and the next send or check tries again.
// Errors are progress lines only: the job itself succeeded.
func notifyProbeAlerts(ctx context.Context, db *sql.DB, key []byte, baseURL string, progress func(string)) {
	delays, failures, err := models.ListUnnotifiedProbeAlerts(db)
	if err != nil {
		progress(fmt.Sprintf("alert mail: %v", err))
		return
	}
	if len(delays) == 0 && len(failures) == 0 {
		return
	}
	delivered := mailProbeRecipients(ctx, db, key, baseURL, "alert mail", progress,
		func(baseURL string, loc MailLocale, admin bool) (string, string, error) {
			return renderProbeAlertMail(delays, failures, baseURL, loc, admin)
		})
	if delivered == 0 {
		return
	}
	ids := func(list []*models.ProbeMail) []int64 {
		out := make([]int64, 0, len(list))
		for _, p := range list {
			out = append(out, p.ID)
		}
		return out
	}
	if err := models.MarkProbeMailsNotified(db, ids(delays), ids(failures), time.Now()); err != nil {
		progress(fmt.Sprintf("alert mail: %v", err))
		return
	}
	progress(fmt.Sprintf("alert mail sent to %d recipients (%d failed, %d delayed probe mails)", delivered, len(failures), len(delays)))
}

// renderProbeAlertMail renders the alert mail for one locale. The error
// texts name the SMTP server of the mailbox and go to administrators only
// (admin), as on the screens; the other recipients read that the details
// are shown to administrators when a mail could not be sent. The times are
// in the recipient's time zone, as the date of the mail.
func renderProbeAlertMail(delays, failures []*models.ProbeMail, baseURL string, loc MailLocale, admin bool) (string, string, error) {
	sendFailures := false
	entries := func(list []*models.ProbeMail) []ProbeAlertEntry {
		out := make([]ProbeAlertEntry, 0, len(list))
		for _, p := range list {
			e := ProbeAlertEntry{Address: p.Address, Slot: p.Slot, Manual: p.Manual, SentAt: p.SentAt.In(loc.Location).Format("2006-01-02 15:04"),
				SendFailed: p.Status == models.ProbeStatusSendFailed}
			sendFailures = sendFailures || e.SendFailed
			if admin {
				e.Error = p.ErrorMessage
			}
			e.Hours, e.Minutes = p.DelaySeconds/3600, p.DelaySeconds%3600/60
			out = append(out, e)
		}
		return out
	}
	return renderSubjectAndBody(mailTemplatePath(probeAlertTemplateKind, loc.Language), map[string]any{
		"Date": time.Now().In(loc.Location).Format("2006-01-02 15:04"), "Failures": entries(failures), "Delays": entries(delays),
		"URL": strings.TrimRight(baseURL, "/") + "/alerts/probes", "BaseURL": baseURL, "Admin": admin,
		"SendFailures": sendFailures,
	})
}

// notifyProbeUnreadable mails the notification recipients once that the
// probe server could not be read since failingSince (for at least the
// failure threshold), and reports whether at least one recipient got it.
// The error names the probe server and goes to administrators only.
func notifyProbeUnreadable(ctx context.Context, db *sql.DB, key []byte, baseURL string, settings *ProbeSettings,
	failingSince time.Time, checkErr error, progress func(string)) bool {
	delivered := mailProbeRecipients(ctx, db, key, baseURL, "probe server mail", progress,
		func(baseURL string, loc MailLocale, admin bool) (string, string, error) {
			detail := ""
			if admin {
				detail = checkErr.Error()
			}
			return renderSubjectAndBody(mailTemplatePath(probeUnreadableTemplateKind, loc.Language), map[string]any{
				"Date": time.Now().In(loc.Location).Format("2006-01-02 15:04"), "Since": failingSince.In(loc.Location).Format("2006-01-02 15:04"),
				"Error": detail, "Admin": admin, "URL": strings.TrimRight(baseURL, "/") + "/alerts/probes", "BaseURL": baseURL,
			})
		})
	if delivered > 0 {
		progress(fmt.Sprintf("probe server mail sent to %d recipients", delivered))
	}
	return delivered > 0
}
