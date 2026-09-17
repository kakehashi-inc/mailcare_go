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
)

// ErrMailboxBusy is returned when a mailbox cannot be renamed or deleted
// because one of its jobs is running (the HTTP layer answers 409).
var ErrMailboxBusy = errors.New("a job is running for this mailbox")

// ErrPasswordRequired is returned when the connection settings of a mailbox
// (host, port, security, username) differ from the stored ones and no
// password was given: the stored password belongs to the stored connection
// and is never carried over to, or tried against, another one.
var ErrPasswordRequired = errors.New("imap_password is required when the connection settings change")

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
}

// MailboxInputFrom builds an input pre-filled from a stored mailbox (without
// the password) so a partial update can override single fields.
func MailboxInputFrom(mb *models.Mailbox) *MailboxInput {
	enabled := mb.Enabled
	return &MailboxInput{
		ID: mb.ID, Address: mb.Address, DisplayName: mb.DisplayName, ImapHost: mb.ImapHost,
		ImapPort: mb.ImapPort, ImapSecurity: mb.ImapSecurity, ImapUsername: mb.ImapUsername,
		Folder: mb.Folder, Enabled: &enabled, InitialDays: mb.InitialDays, RecentDays: mb.RecentDays,
	}
}

// ValidateMailboxInput normalizes the input in place (lower-cased address,
// trimmed strings, defaults applied) and reports the first validation error.
func ValidateMailboxInput(in *MailboxInput) error {
	in.Address = strings.ToLower(strings.TrimSpace(in.Address))
	if in.Address == "" {
		return errors.New("address is required")
	}
	if len(in.Address) > 254 || strings.ContainsAny(in.Address, " \t\r\n<>\"") {
		return errors.New("address is not a valid mail address")
	}
	if a, err := mail.ParseAddress(in.Address); err != nil || a.Address != in.Address || !strings.Contains(in.Address, "@") {
		return errors.New("address is not a valid mail address")
	}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(in.DisplayName) > 128 {
		return errors.New("display_name must be 128 characters or fewer")
	}
	in.ImapHost = strings.TrimSpace(in.ImapHost)
	if in.ImapHost == "" {
		return errors.New("imap_host is required")
	}
	if !validIMAPHost(in.ImapHost) {
		return errors.New("imap_host is not a valid host name")
	}
	if in.ImapPort == 0 {
		in.ImapPort = DefaultIMAPPort
	}
	if in.ImapPort < 1 || in.ImapPort > 65535 {
		return errors.New("imap_port must be between 1 and 65535")
	}
	in.ImapSecurity = strings.ToLower(strings.TrimSpace(in.ImapSecurity))
	if in.ImapSecurity == "" {
		in.ImapSecurity = IMAPSecuritySSL
	}
	switch in.ImapSecurity {
	case IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone:
	default:
		return fmt.Errorf("imap_security must be %s, %s or %s", IMAPSecuritySSL, IMAPSecurityStartTLS, IMAPSecurityNone)
	}
	in.ImapUsername = strings.TrimSpace(in.ImapUsername)
	if in.ImapUsername == "" {
		return errors.New("imap_username is required")
	}
	in.Folder = strings.TrimSpace(in.Folder)
	if in.Folder == "" {
		in.Folder = "INBOX"
	}
	if strings.ContainsAny(in.Folder, "\r\n") {
		return errors.New("folder is not a valid folder name")
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
		return errors.New("initial_days must be between 1 and 3650")
	}
	if in.RecentDays < 1 || in.RecentDays > 3650 {
		return errors.New("recent_days must be between 1 and 3650")
	}
	return nil
}

// validIMAPHost accepts a host name or an IP literal (IPv4, or IPv6 such as
// "::1", as net.ParseIP reads it). A "host:port" value is refused: the port
// has its own field.
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
		return fmt.Errorf("mailbox %q already exists", address)
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
			return fmt.Errorf("mailbox %q conflicts with %q (same data directory name)", address, other.Address)
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
		return nil, errors.New("imap_password is required")
	}
	if err := checkAddressAvailable(db, in.Address, 0); err != nil {
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
	if err := ValidateMailboxInput(in); err != nil {
		return err
	}
	if in.ImapPassword == "" && mb.ImapPasswordEnc != "" && !sameConnection(mb, in) {
		return ErrPasswordRequired
	}
	if err := checkAddressAvailable(db, in.Address, mb.ID); err != nil {
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
			return fmt.Errorf("%s already exists", p[1])
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
		return "", errors.New("no password is stored for this mailbox")
	}
	return DecryptSecret(key, mb.ImapPasswordEnc)
}

// TestMailboxConnection validates the input and tries to connect, log in and
// select the folder. When the password is empty and ID refers to a stored
// mailbox, the stored password is used, but only for the stored connection
// (same host, port, security and username): a test against another server
// or account must carry its own password, so the stored one cannot be
// tried elsewhere.
func TestMailboxConnection(ctx context.Context, db *sql.DB, key []byte, in *MailboxInput) error {
	if err := ValidateMailboxInput(in); err != nil {
		return err
	}
	password := in.ImapPassword
	if password == "" {
		if in.ID == 0 {
			return errors.New("imap_password is required")
		}
		stored, err := models.GetMailboxByID(db, in.ID)
		if err == sql.ErrNoRows {
			return errors.New("mailbox not found")
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
	ctx, cancel := context.WithTimeout(ctx, IMAPTimeout)
	defer cancel()
	return mailengine.TestConnection(ctx, mb, password)
}

// sameConnection reports whether the input names the connection stored for
// the mailbox: host (case-insensitively), port, security and username.
func sameConnection(stored *models.Mailbox, in *MailboxInput) bool {
	return strings.EqualFold(stored.ImapHost, in.ImapHost) && stored.ImapPort == in.ImapPort &&
		stored.ImapSecurity == in.ImapSecurity && stored.ImapUsername == in.ImapUsername
}
