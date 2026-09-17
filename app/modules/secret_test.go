package modules

import (
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"mailcare/app/models"
)

func TestSecretKeyAndEncryption(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	if len(key) != secretKeyLen {
		t.Fatalf("key length %d", len(key))
	}
	// The key lives in the settings table as 64 hex digits and nowhere else.
	stored, found, err := models.GetSettingStrict(db, SettingSecretKey)
	if err != nil || !found {
		t.Fatalf("key not stored in settings: %v (found %v)", err, found)
	}
	if len(stored) != 2*secretKeyLen || stored != hex.EncodeToString(key) {
		t.Errorf("stored key %q does not match the loaded key", stored)
	}
	dataDir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), DBFileName) {
			t.Errorf("unexpected file in the data directory: %s", e.Name())
		}
	}
	again, err := LoadSecretKey(db)
	if err != nil || string(again) != string(key) {
		t.Errorf("key changed on reload: %v", err)
	}
	// The CLI neither shows nor sets the key.
	if slices.Contains(SettingKeys(), SettingSecretKey) {
		t.Error("secret_key must not be a settable setting")
	}
	if _, err := ApplySetting(db, SettingSecretKey, "0123", nil); err == nil {
		t.Error("settings set secret_key must be rejected")
	}
	if fresh, _ := LoadSecretKey(db); string(fresh) != string(key) {
		t.Error("the rejected settings set must not touch the key")
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
	// A corrupt stored key is reported, never replaced.
	if err := models.SetSetting(db, SettingSecretKey, "nonsense"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecretKey(db); err == nil {
		t.Errorf("corrupt key accepted")
	}
	if v := models.GetSetting(db, SettingSecretKey); v != "nonsense" {
		t.Errorf("corrupt key was overwritten with %q", v)
	}
}

func TestSessionCookieRoundTrip(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	u, err := CreateUserFrom(db, NewUser{Username: "alice", Password: "password123", Role: RoleUser})
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
