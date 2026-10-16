package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
	"mailcare/app/modules/wording"
)

// ErrMailboxBusy is returned when a mailbox cannot be renamed or deleted
// because one of its jobs is running (the HTTP layer answers 409).
var ErrMailboxBusy = wording.New("result.mailbox.busy", "a job is running for this mailbox")

// ErrPasswordRequired is returned when the connection settings of a mailbox
// (host, port, security, username) differ from the stored ones and no
// password was given: the stored password belongs to the stored connection
// and is never carried over to, or tried against, another one.
var ErrPasswordRequired = wording.New("validation.connection.passwordRequiredOnChange", "imap_password is required when the connection settings change")

// errIMAPPasswordRequired is returned when a password is needed and none is
// given or stored.
var errIMAPPasswordRequired = wording.New("validation.connection.passwordRequired", "imap_password is required")

// MailboxInput is the editable part of a mailbox as received from the Web UI
// or assembled by the CLI. Pointer / zero-valued optional fields fall back to
// the defaults documented on each field.
type MailboxInput struct {
	ID           int64  `json:"id,omitempty"`
	Address      string `json:"address"`
	DisplayName  string `json:"display_name"`
	ImapHost     string `json:"imap_host"`
	ImapPort     int    `json:"imap_port"`     // 0 = DefaultIMAPPort
	ImapSecurity string `json:"imap_security"` // "" = ssl
	ImapUsername string `json:"imap_username"`
	ImapPassword string `json:"imap_password"` // "" on update = keep the stored password (only while host / port / security / username stay the same)
	Folder       string `json:"folder"`        // "" = INBOX
	Enabled      *bool  `json:"enabled"`       // nil = true
	InitialDays  int    `json:"initial_days"`  // 0 = DefaultInitialDays
	RecentDays   int    `json:"recent_days"`   // 0 = DefaultRecentDays
	// ServerKeepDays is the server retention (0..MaxServerKeepDays, 0 =
	// never delete on the server, at most mail_keep_days: see
	// checkServerKeepDays). nil = DefaultServerKeepDays (or mail_keep_days
	// when shorter) on creation, the stored value on update.
	ServerKeepDays *int `json:"server_keep_days"`
	// SmtpHost is the host name of the SMTP server that sends the mail of
	// the address (normalizeSourceHost; "" = not set). nil = not set on
	// creation, the stored value on update. It is no IMAP connection
	// setting: changing it needs no IMAP password (the SMTP password of the
	// send check: see ProbeEnabled).
	SmtpHost *string `json:"smtp_host"`
	// ProbeEnabled is the "send check": the address sends probe mails
	// through SmtpHost with the fields below (probe.go). nil = off on
	// creation, the stored setting (with the stored port, security and
	// username) on update. On, the host and the port are required, the
	// security defaults to DefaultSMTPSecurity, an empty username means no
	// authentication; an empty password keeps the stored one, but only
	// while host, port, security and username stay the same
	// (ErrSMTPConnectionPasswordRequired otherwise). Off, SmtpHost only
	// serves the judgement of the sending sources and the fields below are
	// cleared.
	ProbeEnabled *bool  `json:"probe_enabled"`
	SmtpPort     int    `json:"smtp_port"`
	SmtpSecurity string `json:"smtp_security"`
	SmtpUsername string `json:"smtp_username"`
	SmtpPassword string `json:"smtp_password"`
}

// ErrSMTPConnectionPasswordRequired is returned when the SMTP connection of
// a mailbox changes while a password is stored for it and no password is
// given (the stored one is never pointed at another server).
var ErrSMTPConnectionPasswordRequired = wording.New("validation.connection.passwordRequiredOnChange", "smtp_password is required when the SMTP connection changes")

// MailboxInputFrom builds an input pre-filled from a stored mailbox (without
// the password) so a partial update can override single fields.
func MailboxInputFrom(mb *models.Mailbox) *MailboxInput {
	enabled := mb.Enabled
	serverKeep := mb.ServerKeepDays
	smtpHost := mb.SmtpHost
	probe := mb.ProbeEnabled
	return &MailboxInput{
		ID: mb.ID, Address: mb.Address, DisplayName: mb.DisplayName, ImapHost: mb.ImapHost,
		ImapPort: mb.ImapPort, ImapSecurity: mb.ImapSecurity, ImapUsername: mb.ImapUsername,
		Folder: mb.Folder, Enabled: &enabled, InitialDays: mb.InitialDays, RecentDays: mb.RecentDays,
		ServerKeepDays: &serverKeep, SmtpHost: &smtpHost, ProbeEnabled: &probe, SmtpPort: mb.SmtpPort, SmtpSecurity: mb.SmtpSecurity,
		SmtpUsername: mb.SmtpUsername,
	}
}

// ValidateMailboxInput normalizes the input in place (lower-cased address,
// trimmed strings, defaults applied) and reports the first validation error.
func ValidateMailboxInput(in *MailboxInput) error {
	in.Address = strings.ToLower(strings.TrimSpace(in.Address))
	if in.Address == "" {
		return wording.New("validation.mailbox.addressRequired", "address is required")
	}
	if len(in.Address) > MaxEmailLength || strings.ContainsAny(in.Address, " \t\r\n<>\"") {
		return errAddressInvalid
	}
	if a, err := mail.ParseAddress(in.Address); err != nil || a.Address != in.Address || !strings.Contains(in.Address, "@") {
		return errAddressInvalid
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(in.DisplayName) > MaxDisplayNameLength {
		return wording.New("validation.common.displayNameTooLong", fmt.Sprintf("display_name must be %d characters or fewer", MaxDisplayNameLength)).
			With("max", MaxDisplayNameLength)
	}
	in.ImapHost = strings.TrimSpace(in.ImapHost)
	if in.ImapHost == "" {
		return wording.New("validation.connection.hostRequired", "imap_host is required")
	}
	if !validIMAPHost(in.ImapHost) {
		return wording.New("validation.connection.hostInvalid", "imap_host is not a valid host name")
	}
	if in.ImapPort == 0 {
		in.ImapPort = DefaultIMAPPort
	}
	if in.ImapPort < 1 || in.ImapPort > 65535 {
		return wording.New("validation.common.numberOutOfRange", "imap_port must be between 1 and 65535").With("min", 1).With("max", 65535)
	}
	in.ImapSecurity = strings.ToLower(strings.TrimSpace(in.ImapSecurity))
	if in.ImapSecurity == "" {
		in.ImapSecurity = IMAPSecuritySSL
	}
	switch in.ImapSecurity {
	case IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone:
	default:
		return wording.New("system.invalidRequest", fmt.Sprintf("imap_security must be %s, %s or %s", IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone))
	}
	in.ImapUsername = strings.TrimSpace(in.ImapUsername)
	if in.ImapUsername == "" {
		return wording.New("validation.connection.usernameRequired", "imap_username is required")
	}
	in.Folder = strings.TrimSpace(in.Folder)
	if in.Folder == "" {
		in.Folder = "INBOX"
	}
	if strings.ContainsAny(in.Folder, "\r\n") {
		return wording.New("validation.mailbox.folderInvalid", "folder is not a valid folder name")
	}
	if in.Enabled == nil {
		t := true
		in.Enabled = &t
	}
	if in.InitialDays == 0 {
		in.InitialDays = DefaultInitialDays
	}
	if in.RecentDays == 0 {
		in.RecentDays = DefaultRecentDays
	}
	if in.InitialDays < 1 || in.InitialDays > 3650 {
		return wording.New("validation.common.numberOutOfRange", "initial_days must be between 1 and 3650").With("min", 1).With("max", 3650)
	}
	if in.RecentDays < 1 || in.RecentDays > 3650 {
		return wording.New("validation.common.numberOutOfRange", "recent_days must be between 1 and 3650").With("min", 1).With("max", 3650)
	}
	if in.ServerKeepDays != nil && (*in.ServerKeepDays < 0 || *in.ServerKeepDays > MaxServerKeepDays) {
		return wording.New("validation.common.numberOutOfRange",
			fmt.Sprintf("server_keep_days must be between 0 and %d (0 = keep the mails on the server)", MaxServerKeepDays)).
			With("min", 0).With("max", MaxServerKeepDays)
	}
	if in.SmtpHost != nil {
		host := strings.TrimSpace(*in.SmtpHost)
		if host != "" {
			norm, ok := normalizeSourceHost(host)
			if !ok {
				host = clipEntry(host)
				return wording.New("validation.mailbox.smtpHostInvalid",
					fmt.Sprintf("smtp_host %q is not a host name (letters, digits and hyphens; IP addresses and localhost are not accepted)", host)).
					With("value", host)
			}
			host = norm
		}
		in.SmtpHost = &host
	}
	return validateSMTPConnectionInput(in)
}

// Errors of the send check (validateSMTPConnectionInput).
var (
	errSMTPHostRequiredForProbe = wording.New("validation.mailbox.smtpHostRequiredForProbe", "smtp_host is required for the send check")
	errSMTPPortRequired         = wording.New("validation.mailbox.smtpPortRequired", "smtp_port is required for the send check")
)

// validateSMTPConnectionInput normalizes the send check of an input (the
// host is checked above): nil counts as off (UpdateMailbox fills in the
// stored setting first). Off clears the port, the security, the username
// and the password; on needs a host and a port, gives the security its
// default and drops the password when there is no username.
func validateSMTPConnectionInput(in *MailboxInput) error {
	if in.ProbeEnabled == nil || !*in.ProbeEnabled {
		off := false
		in.ProbeEnabled = &off
		in.SmtpPort, in.SmtpSecurity, in.SmtpUsername, in.SmtpPassword = 0, "", "", ""
		return nil
	}
	if in.SmtpHost == nil || *in.SmtpHost == "" {
		return errSMTPHostRequiredForProbe
	}
	if in.SmtpPort == 0 {
		return errSMTPPortRequired
	}
	if in.SmtpPort < 1 || in.SmtpPort > 65535 {
		return errSMTPPortInvalid
	}
	in.SmtpSecurity = strings.ToLower(strings.TrimSpace(in.SmtpSecurity))
	if in.SmtpSecurity == "" {
		in.SmtpSecurity = DefaultSMTPSecurity
	}
	if err := ValidateSMTPSecurity(in.SmtpSecurity); err != nil {
		return err
	}
	in.SmtpUsername = strings.TrimSpace(in.SmtpUsername)
	if in.SmtpUsername == "" {
		in.SmtpPassword = ""
	}
	return nil
}

// fillStoredSMTP completes an update input with the stored host and send
// check where it carries none (a partial update keeps them).
func fillStoredSMTP(stored *models.Mailbox, in *MailboxInput) {
	if in.SmtpHost == nil {
		host := stored.SmtpHost
		in.SmtpHost = &host
	}
	if in.ProbeEnabled == nil {
		probe := stored.ProbeEnabled
		in.ProbeEnabled = &probe
		in.SmtpPort, in.SmtpSecurity, in.SmtpUsername = stored.SmtpPort, stored.SmtpSecurity, stored.SmtpUsername
	}
}

// KeepAbsentSMTPFields completes an update received over HTTP with the
// stored SMTP connection where the request names no field (has): with the
// send check on (and on before), an absent port, security or username keeps
// the stored one, so a partial update never drops the stored password by
// clearing the username. An absent smtp_host or probe_enabled is handled by
// UpdateMailbox (fillStoredSMTP) and an absent or empty smtp_password keeps
// the stored one; a field sent empty keeps its meaning.
func KeepAbsentSMTPFields(stored *models.Mailbox, in *MailboxInput, has func(name string) bool) {
	if in.ProbeEnabled == nil || !*in.ProbeEnabled || !stored.ProbeEnabled {
		return
	}
	if !has("smtp_port") {
		in.SmtpPort = stored.SmtpPort
	}
	if !has("smtp_security") {
		in.SmtpSecurity = stored.SmtpSecurity
	}
	if !has("smtp_username") {
		in.SmtpUsername = stored.SmtpUsername
	}
}

// sameSMTPConnection reports whether a validated input names the SMTP
// connection stored for the mailbox (host, port, security, username).
func sameSMTPConnection(stored *models.Mailbox, in *MailboxInput) bool {
	return in.SmtpHost != nil && strings.EqualFold(stored.SmtpHost, *in.SmtpHost) && stored.SmtpPort == in.SmtpPort &&
		stored.SmtpSecurity == in.SmtpSecurity && stored.SmtpUsername == in.SmtpUsername
}

// smtpPasswordCheck refuses a validated input whose send check is on with a
// username but without a usable password (the same rule as the test send,
// TestProbeSMTP): no password is given and none is stored
// (errSMTPPasswordRequired), or one is stored for another SMTP connection
// (ErrSMTPConnectionPasswordRequired; the stored one is never pointed at
// another server). stored is nil on creation.
func smtpPasswordCheck(stored *models.Mailbox, in *MailboxInput) error {
	if in.ProbeEnabled == nil || !*in.ProbeEnabled || in.SmtpUsername == "" || in.SmtpPassword != "" {
		return nil
	}
	switch {
	case stored == nil || stored.SmtpPasswordEnc == "":
		return errSMTPPasswordRequired
	case !sameSMTPConnection(stored, in):
		return ErrSMTPConnectionPasswordRequired
	}
	return nil
}

var errAddressInvalid = wording.New("validation.common.emailFormat", "address is not a valid mail address")

// checkServerKeepDays bounds the server retention by the mail retention
// (mail_keep_days): a mail past the mail retention loses its index row, so
// a longer server retention could never be applied to it. On creation
// (stored nil) an absent value becomes DefaultServerKeepDays, or
// mail_keep_days when that is shorter. A value above mail_keep_days is
// refused, except on update when it is the stored value unchanged (the mail
// retention was shortened after it was saved; the cleanup then applies
// mail_keep_days, see effectiveServerKeepDays), so that editing another
// field of such a mailbox still works.
func checkServerKeepDays(db *sql.DB, in *MailboxInput, stored *int) error {
	mailKeep := ResolveMailKeepDays(db)
	if in.ServerKeepDays == nil {
		if stored == nil {
			days := min(DefaultServerKeepDays, mailKeep)
			in.ServerKeepDays = &days
		}
		return nil
	}
	if *in.ServerKeepDays <= mailKeep || (stored != nil && *in.ServerKeepDays == *stored) {
		return nil
	}
	return wording.New("validation.mailbox.serverKeepDaysOverMailKeep",
		fmt.Sprintf("server_keep_days must not exceed mail_keep_days (%d days)", mailKeep)).With("max", mailKeep)
}

// validIMAPHost accepts a host name or an IP literal (IPv4, or IPv6 such as
// "::1", as net.ParseIP reads it). A "host:port" value is refused: the port
// has a separate field.
func validIMAPHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return !strings.ContainsAny(host, " \t\r\n/\\:")
}

// applyMailboxInput copies a validated input onto a mailbox row, encrypting
// the password when one is given (otherwise ImapPasswordEnc is left empty so
// models.UpdateMailbox keeps the stored one).
func applyMailboxInput(key []byte, mb *models.Mailbox, in *MailboxInput) error {
	mb.Address = in.Address
	mb.DisplayName = in.DisplayName
	mb.ImapHost = in.ImapHost
	mb.ImapPort = in.ImapPort
	mb.ImapSecurity = in.ImapSecurity
	mb.ImapUsername = in.ImapUsername
	mb.Folder = in.Folder
	mb.Enabled = *in.Enabled
	mb.InitialDays = in.InitialDays
	mb.RecentDays = in.RecentDays
	if in.ServerKeepDays != nil {
		mb.ServerKeepDays = *in.ServerKeepDays
	}
	if in.SmtpHost != nil {
		mb.SmtpHost = *in.SmtpHost
	}
	if in.ProbeEnabled != nil {
		mb.ProbeEnabled = *in.ProbeEnabled
		mb.SmtpPort, mb.SmtpSecurity, mb.SmtpUsername = in.SmtpPort, in.SmtpSecurity, in.SmtpUsername
		// Empty: models.UpdateMailbox keeps the stored password while the
		// send check is on and a host and a username are set.
		mb.SmtpPasswordEnc = ""
		if in.SmtpPassword != "" && *in.ProbeEnabled {
			enc, err := EncryptSecret(key, in.SmtpPassword)
			if err != nil {
				return err
			}
			mb.SmtpPasswordEnc = enc
		}
	}
	mb.ImapPasswordEnc = ""
	if in.ImapPassword != "" {
		enc, err := EncryptSecret(key, in.ImapPassword)
		if err != nil {
			return err
		}
		mb.ImapPasswordEnc = enc
	}
	return nil
}

// addressTaken reports whether another mailbox (not excludeID) uses address.
func addressTaken(db *sql.DB, address string, excludeID int64) (bool, error) {
	other, err := models.GetMailboxByAddress(db, address)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return other.ID != excludeID, nil
}

// checkAddressAvailable refuses an address that another mailbox (not
// excludeID) already uses, and also one whose sanitized form (the name of
// the raw files directory, the index and the agent workspaces, 3.1) is the
// sanitized form of another mailbox's address: "a!b@example.com" and
// "a_b@example.com" would share "a_b@example.com" on disk.
func checkAddressAvailable(db *sql.DB, address string, excludeID int64) error {
	if taken, err := addressTaken(db, address, excludeID); err != nil {
		return err
	} else if taken {
		return wording.New("validation.mailbox.addressTaken", fmt.Sprintf("mailbox %q already exists", address))
	}
	mailboxes, err := models.ListMailboxes(db)
	if err != nil {
		return err
	}
	name := mailengine.SanitizeAddress(address)
	for _, other := range mailboxes {
		if other.ID == excludeID {
			continue
		}
		if mailengine.SanitizeAddress(other.Address) == name {
			return wording.New("validation.mailbox.addressConflicts",
				fmt.Sprintf("mailbox %q conflicts with %q (same data directory name)", address, other.Address)).
				With("other", other.Address)
		}
	}
	return nil
}

// CreateMailbox validates the input and inserts a mailbox. The password is
// required on creation. The address must be free, also in its sanitized form
// (checkAddressAvailable).
func CreateMailbox(db *sql.DB, key []byte, in *MailboxInput) (*models.Mailbox, error) {
	if err := ValidateMailboxInput(in); err != nil {
		return nil, err
	}
	if in.ImapPassword == "" {
		return nil, errIMAPPasswordRequired
	}
	if err := smtpPasswordCheck(nil, in); err != nil {
		return nil, err
	}
	if err := checkAddressAvailable(db, in.Address, 0); err != nil {
		return nil, err
	}
	if err := checkServerKeepDays(db, in, nil); err != nil {
		return nil, err
	}
	mb := &models.Mailbox{}
	if err := applyMailboxInput(key, mb, in); err != nil {
		return nil, err
	}
	if err := models.InsertMailbox(db, mb); err != nil {
		return nil, err
	}
	return mb, nil
}

// UpdateMailbox validates the input and updates the mailbox row in place. An
// empty password keeps the stored one, but only while the connection stays
// the same: when a password is stored and the host, port, security or
// username changes, the input must carry a password (ErrPasswordRequired,
// nothing is saved), the same rule as TestMailboxConnection. Renaming
// the address moves the raw files, the index and the agent workspaces along
// with it (files first, then the row; when the row cannot be written the
// files are moved back). A rename is refused with ErrMailboxBusy while a
// job of the mailbox is running, because that job works on the old address,
// and when the new address (or its sanitized form) belongs to another
// mailbox (checkAddressAvailable).
func UpdateMailbox(db *sql.DB, key []byte, mailsRoot, agentRoot string, mb *models.Mailbox, in *MailboxInput) error {
	fillStoredSMTP(mb, in)
	if err := ValidateMailboxInput(in); err != nil {
		return err
	}
	if in.ImapPassword == "" && mb.ImapPasswordEnc != "" && !sameConnection(mb, in) {
		return ErrPasswordRequired
	}
	if err := smtpPasswordCheck(mb, in); err != nil {
		return err
	}
	if err := checkAddressAvailable(db, in.Address, mb.ID); err != nil {
		return err
	}
	stored := mb.ServerKeepDays
	if err := checkServerKeepDays(db, in, &stored); err != nil {
		return err
	}
	oldAddress := mb.Address
	if oldAddress != in.Address {
		running, err := models.HasRunningJobForMailbox(db, mb.ID)
		if err != nil {
			return err
		}
		if running {
			return ErrMailboxBusy
		}
	}
	storedEnc := mb.ImapPasswordEnc
	storedSMTPEnc := mb.SmtpPasswordEnc
	if err := applyMailboxInput(key, mb, in); err != nil {
		return err
	}
	if oldAddress != mb.Address {
		if err := renameMailboxData(mailsRoot, agentRoot, oldAddress, mb.Address); err != nil {
			mb.Address = oldAddress
			return fmt.Errorf("failed to move the mail data to the new address: %w", err)
		}
	}
	if err := models.UpdateMailbox(db, mb); err != nil {
		if oldAddress != mb.Address {
			if back := renameMailboxData(mailsRoot, agentRoot, mb.Address, oldAddress); back != nil {
				err = errors.Join(err, fmt.Errorf("the mail data could not be moved back to %s: %w", oldAddress, back))
			}
			mb.Address = oldAddress
		}
		return err
	}
	if mb.ImapPasswordEnc == "" {
		mb.ImapPasswordEnc = storedEnc
	}
	if mb.SmtpPasswordEnc == "" && mb.ProbeEnabled && mb.SmtpHost != "" && mb.SmtpUsername != "" {
		mb.SmtpPasswordEnc = storedSMTPEnc
	}
	if !ProbeParticipates(mb) {
		// Its send check, or the mailbox, is switched off: no check looks
		// for its open probe mails any more.
		closeOpenProbes(db, mb.ID)
	}
	return nil
}

// AgentWorkspaceDir returns the directory holding the agent workspaces of a
// mailbox (<agentRoot>/<sanitized address>).
func AgentWorkspaceDir(agentRoot, address string) string {
	return filepath.Join(agentRoot, mailengine.SanitizeAddress(address))
}

// renameMailboxData moves the raw files directory, the index database (with
// its WAL side files) and the agent workspace directory of oldAddress to the
// names derived from newAddress. Missing sources are skipped (a mailbox that
// was never checked has no data).
func renameMailboxData(mailsRoot, agentRoot, oldAddress, newAddress string) error {
	if mailengine.SanitizeAddress(oldAddress) == mailengine.SanitizeAddress(newAddress) {
		return nil
	}
	oldIndex := mailengine.MailboxIndexPath(mailsRoot, oldAddress)
	newIndex := mailengine.MailboxIndexPath(mailsRoot, newAddress)
	pairs := [][2]string{
		{mailengine.MailboxDir(mailsRoot, oldAddress), mailengine.MailboxDir(mailsRoot, newAddress)},
		{oldIndex, newIndex},
		{oldIndex + "-wal", newIndex + "-wal"},
		{oldIndex + "-shm", newIndex + "-shm"},
		{AgentWorkspaceDir(agentRoot, oldAddress), AgentWorkspaceDir(agentRoot, newAddress)},
	}
	// Every target is checked before anything is moved so that a refused
	// rename leaves the data where it was.
	var moves [][2]string
	for _, p := range pairs {
		if _, err := os.Stat(p[0]); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := os.Stat(p[1]); err == nil {
			return wording.New("result.mailbox.dataExists", fmt.Sprintf("%s already exists", p[1]))
		}
		moves = append(moves, p)
	}
	for _, p := range moves {
		if err := os.Rename(p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// DeleteMailbox removes the mailbox row and, unless keepData, its raw files,
// its index (the agent reports live inside it) and its agent workspaces.
// The queued jobs of the mailbox are canceled in the same transaction as
// the row (a job that starts later would find no mailbox); while a job of
// the mailbox is running the deletion is refused with ErrMailboxBusy.
func DeleteMailbox(db *sql.DB, mailsRoot, agentRoot string, mb *models.Mailbox, keepData bool) error {
	if err := deleteMailboxRow(db, mb.ID); err != nil {
		return err
	}
	if keepData {
		return nil
	}
	if err := mailengine.DeleteMailboxData(mailsRoot, mb.Address); err != nil {
		return fmt.Errorf("mailbox removed but its data could not be deleted: %w", err)
	}
	if err := os.RemoveAll(AgentWorkspaceDir(agentRoot, mb.Address)); err != nil {
		return fmt.Errorf("mailbox removed but its agent workspaces could not be deleted: %w", err)
	}
	return nil
}

// deleteMailboxRow cancels the queued jobs of the mailbox and deletes its
// row in one transaction. The cancel statement takes the write lock first,
// so a worker cannot claim one of those jobs in between; a job that is
// already running makes the transaction roll back with ErrMailboxBusy.
func deleteMailboxRow(db *sql.DB, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := models.CancelQueuedJobsForMailbox(tx, id); err != nil {
		return err
	}
	running, err := models.HasRunningJobForMailbox(tx, id)
	if err != nil {
		return err
	}
	if running {
		return ErrMailboxBusy
	}
	if err := models.DeleteMailbox(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MailboxPassword decrypts the stored IMAP password of a mailbox.
func MailboxPassword(key []byte, mb *models.Mailbox) (string, error) {
	if mb.ImapPasswordEnc == "" {
		return "", errIMAPPasswordRequired
	}
	return DecryptSecret(key, mb.ImapPasswordEnc)
}

// TestMailboxConnection validates the input and tries to connect, log in and
// select the folder. When the password is empty and ID refers to a stored
// mailbox, the stored password is used, but only for the stored connection
// (same host, port, security and username): a test against another server
// or account must carry a password, so the stored one cannot be
// tried elsewhere.
func TestMailboxConnection(ctx context.Context, db *sql.DB, key []byte, in *MailboxInput) error {
	if err := ValidateMailboxInput(in); err != nil {
		return err
	}
	password := in.ImapPassword
	if password == "" {
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
		if !sameConnection(stored, in) {
			return ErrPasswordRequired
		}
		password, err = MailboxPassword(key, stored)
		if err != nil {
			return err
		}
	}
	mb := &models.Mailbox{}
	probe := *in
	probe.ImapPassword = ""
	if err := applyMailboxInput(key, mb, &probe); err != nil {
		return err
	}
	// Only the connection is bounded (mailengine.IMAPConnectTimeout); the
	// commands after it are not.
	if err := mailengine.TestConnection(ctx, mb, password); err != nil {
		// The words of the server are the useful part; the Web UI shows them
		// as the detail of its message.
		return wording.New("result.mailbox.testFailed", err.Error()).With("detail", err.Error())
	}
	return nil
}

// sameConnection reports whether the input names the connection stored for
// the mailbox: host (case-insensitively), port, security and username.
func sameConnection(stored *models.Mailbox, in *MailboxInput) bool {
	return strings.EqualFold(stored.ImapHost, in.ImapHost) && stored.ImapPort == in.ImapPort &&
		stored.ImapSecurity == in.ImapSecurity && stored.ImapUsername == in.ImapUsername
}
