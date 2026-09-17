package modules

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mailcare/app/models"
)

func TestSecretKeyAndEncryption(t *testing.T) {
	dir := t.TempDir()
	SetDataDir(dir)
	key, err := LoadSecretKey()
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	if len(key) != secretKeyLen {
		t.Fatalf("key length %d", len(key))
	}
	info, err := os.Stat(filepath.Join(dir, SecretKeyFileName))
	if err != nil {
		t.Fatalf("key file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", info.Mode().Perm())
	}
	again, err := loadOrCreateSecretKey(filepath.Join(dir, SecretKeyFileName))
	if err != nil || string(again) != string(key) {
		t.Errorf("key changed on reload: %v", err)
	}
	enc, err := EncryptSecret(key, "p@ss w0rd")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == "p@ss w0rd" {
		t.Errorf("secret stored in clear")
	}
	dec, err := DecryptSecret(key, enc)
	if err != nil || dec != "p@ss w0rd" {
		t.Errorf("decrypt: %q, %v", dec, err)
	}
	other := make([]byte, secretKeyLen)
	if _, err := DecryptSecret(other, enc); err == nil {
		t.Errorf("decrypt with another key succeeded")
	}
	if err := os.WriteFile(filepath.Join(dir, SecretKeyFileName), []byte("nonsense"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateSecretKey(filepath.Join(dir, SecretKeyFileName)); err == nil {
		t.Errorf("corrupt key file accepted")
	}
}

func TestSessionCookieRoundTrip(t *testing.T) {
	SetDataDir(t.TempDir())
	MigrationsFS = os.DirFS("../..")
	db, err := OpenDB("")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	key, err := LoadSecretKey()
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	u, err := CreateUser(db, "alice", "", "password123", RoleUser)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	expiry := time.Now().Add(time.Hour)
	value, err := IssueSessionCookie(key, u, expiry, true)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := ValidateSessionCookie(db, key, value)
	if err != nil || got.User.ID != u.ID || got.Expiry.Unix() != expiry.Unix() || !got.Remember {
		t.Fatalf("validate: %v (%+v)", err, got)
	}
	// The remember flag travels inside the sealed value.
	plain, _ := IssueSessionCookie(key, u, expiry, false)
	if got, err := ValidateSessionCookie(db, key, plain); err != nil || got.Remember {
		t.Errorf("browser-session cookie: %v (%+v)", err, got)
	}
	if _, err := ValidateSessionCookie(db, key, value+"x"); err == nil {
		t.Errorf("tampered cookie accepted")
	}
	expired, _ := IssueSessionCookie(key, u, time.Now().Add(-time.Minute), true)
	if _, err := ValidateSessionCookie(db, key, expired); err == nil {
		t.Errorf("expired cookie accepted")
	}
	if err := ChangePassword(db, u.ID, "another-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSessionCookie(db, key, value); err == nil {
		t.Errorf("session survived a password change")
	}
	if _, err := AuthenticateUser(db, "alice", "another-password"); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
	if _, err := AuthenticateUser(db, "alice", "password123"); err != ErrInvalidCredentials {
		t.Errorf("login with the old password: %v", err)
	}
	if _, err := models.GetUserByUsername(db, "alice"); err != nil {
		t.Errorf("user vanished: %v", err)
	}
}
