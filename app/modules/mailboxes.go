package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"

	"mailcare/app/models"
	"mailcare/app/modules/mailengine"
)

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
	ImapPassword string `json:"imap_password"` // "" on update = keep the stored password
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
	if len(in.DisplayName) > 128 {
		return errors.New("display_name must be 128 characters or fewer")
	}
	in.ImapHost = strings.TrimSpace(in.ImapHost)
	if in.ImapHost == "" {
		return errors.New("imap_host is required")
	}
	if len(in.ImapHost) > 253 || strings.ContainsAny(in.ImapHost, " \t\r\n/\\:") {
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
	if len(in.ImapUsername) > 254 {
		return errors.New("imap_username must be 254 characters or fewer")
	}
	if len(in.ImapPassword) > 1024 {
		return errors.New("imap_password must be 1024 characters or fewer")
	}
	in.Folder = strings.TrimSpace(in.Folder)
	if in.Folder == "" {
		in.Folder = "INBOX"
	}
	if len(in.Folder) > 255 || strings.ContainsAny(in.Folder, "\r\n") {
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

// CreateMailbox validates the input and inserts a mailbox. The password is
// required on creation.
func CreateMailbox(db *sql.DB, key []byte, in *MailboxInput) (*models.Mailbox, error) {
	if err := ValidateMailboxInput(in); err != nil {
		return nil, err
	}
	if in.ImapPassword == "" {
		return nil, errors.New("imap_password is required")
	}
	if taken, err := addressTaken(db, in.Address, 0); err != nil {
		return nil, err
	} else if taken {
		return nil, fmt.Errorf("mailbox %q already exists", in.Address)
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
// empty password keeps the stored one. Renaming the address moves the raw
// files and the index along with it.
func UpdateMailbox(db *sql.DB, key []byte, mailsRoot string, mb *models.Mailbox, in *MailboxInput) error {
	if err := ValidateMailboxInput(in); err != nil {
		return err
	}
	if taken, err := addressTaken(db, in.Address, mb.ID); err != nil {
		return err
	} else if taken {
		return fmt.Errorf("mailbox %q already exists", in.Address)
	}
	oldAddress := mb.Address
	storedEnc := mb.ImapPasswordEnc
	if err := applyMailboxInput(key, mb, in); err != nil {
		return err
	}
	if oldAddress != mb.Address {
		if err := renameMailboxData(mailsRoot, oldAddress, mb.Address); err != nil {
			mb.Address = oldAddress
			return fmt.Errorf("failed to move the mail data to the new address: %w", err)
		}
	}
	if err := models.UpdateMailbox(db, mb); err != nil {
		return err
	}
	if mb.ImapPasswordEnc == "" {
		mb.ImapPasswordEnc = storedEnc
	}
	return nil
}

// renameMailboxData moves the raw files directory and the index database (with
// its WAL side files) of oldAddress to the names derived from newAddress.
// Missing sources are skipped (a mailbox that was never checked has no data).
func renameMailboxData(mailsRoot, oldAddress, newAddress string) error {
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
	}
	for _, p := range pairs {
		if p[0] == "" || p[1] == "" {
			continue
		}
		if _, err := os.Stat(p[0]); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := os.Stat(p[1]); err == nil {
			return fmt.Errorf("%s already exists", p[1])
		}
		if err := os.Rename(p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// DeleteMailbox removes the mailbox row and, unless keepData, its raw files
// and index.
func DeleteMailbox(db *sql.DB, mailsRoot string, mb *models.Mailbox, keepData bool) error {
	if err := models.DeleteMailbox(db, mb.ID); err != nil {
		return err
	}
	if keepData {
		return nil
	}
	if err := mailengine.DeleteMailboxData(mailsRoot, mb.Address); err != nil {
		return fmt.Errorf("mailbox removed but its data could not be deleted: %w", err)
	}
	return nil
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
// mailbox, the stored password is used.
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
