package models

import (
	"encoding/json"
	"testing"
)

// TestMailboxSmtpHostRoundTrip checks that the SMTP host is written into
// detail_info and read back, that an update can change and clear it, and
// that a row saved before the setting existed (no smtp_host key) reads as
// not set while its other details stay readable.
func TestMailboxSmtpHostRoundTrip(t *testing.T) {
	db := openTestMasterDB(t)
	mb := &Mailbox{Address: "ops@example.jp", Enabled: true, ImapHost: "imap.example.jp", ImapPort: 993,
		ImapSecurity: "ssl", ImapUsername: "ops", ImapPasswordEnc: "enc", Folder: "INBOX", InitialDays: 90,
		RecentDays: 10, ServerKeepDays: 60, SmtpHost: "mail.example.jp"}
	if err := InsertMailbox(db, mb); err != nil {
		t.Fatal(err)
	}
	detail := func() map[string]any {
		t.Helper()
		var raw string
		if err := db.QueryRow(`SELECT detail_info FROM mailboxes WHERE id = ?`, mb.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var j map[string]any
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	if got := detail()["smtp_host"]; got != "mail.example.jp" {
		t.Errorf("stored smtp_host = %#v", got)
	}
	stored, err := GetMailboxByID(db, mb.ID)
	if err != nil || stored.SmtpHost != "mail.example.jp" || stored.ServerKeepDays != 60 || stored.ImapPasswordEnc != "enc" {
		t.Fatalf("read back: %+v (err %v)", stored, err)
	}

	// An update changes it and keeps the stored password; "" clears it.
	stored.SmtpHost, stored.ImapPasswordEnc = "smtp.example.jp", ""
	if err := UpdateMailbox(db, stored); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetMailboxByID(db, mb.ID); got.SmtpHost != "smtp.example.jp" || got.ImapPasswordEnc != "enc" {
		t.Errorf("after the update: %+v", got)
	}
	stored.SmtpHost = ""
	if err := UpdateMailbox(db, stored); err != nil {
		t.Fatal(err)
	}
	if got, _ := GetMailboxByID(db, mb.ID); got.SmtpHost != "" {
		t.Errorf("after clearing: %q", got.SmtpHost)
	}

	// A row saved before the setting existed has no smtp_host key.
	if _, err := db.Exec(`UPDATE mailboxes SET detail_info = json_remove(detail_info, '$.smtp_host') WHERE id = ?`, mb.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := detail()["smtp_host"]; ok {
		t.Fatal("the key was not removed")
	}
	old, err := GetMailboxByID(db, mb.ID)
	if err != nil || old.SmtpHost != "" || old.ImapHost != "imap.example.jp" || old.ServerKeepDays != 60 {
		t.Errorf("old row: %+v (err %v)", old, err)
	}
	list, err := ListMailboxes(db)
	if err != nil || len(list) != 1 || list[0].SmtpHost != "" {
		t.Errorf("old row in the list: %+v (err %v)", list, err)
	}
}
