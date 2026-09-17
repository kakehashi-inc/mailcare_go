package modules

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"mailcare/app/models"
)

// Web session cookie (mlc_session).
//
// The cookie value is one opaque base64url string: nonce || AES-256-GCM(
// user_id || SHA256(password_hash) || expiry ) sealed with the master key.
//
//   - user_id identifies the session owner.
//   - SHA256(password_hash) binds the session to the password in force when it
//     was issued: changing the password (or deleting the user) invalidates every
//     existing session without any server-side session table.
//   - expiry is authenticated by the GCM tag, so it cannot be extended by the
//     client; it is enforced server-side on every request.

const (
	sessionUserIDLen = 8
	sessionHashLen   = sha256.Size
	sessionExpiryLen = 8
	sessionPlainLen  = sessionUserIDLen + sessionHashLen + sessionExpiryLen
)

// ErrSessionInvalid marks a cookie that is genuinely not a valid session:
// malformed, expired, or its user is gone or changed the password. Only this
// error may be answered with 401 (login required). Any other error from
// ValidateSessionCookie is an internal failure (e.g. a transient DB error);
// the session may well be valid, so callers must NOT treat it as "not logged
// in" or the login screen would appear despite a perfectly good cookie.
var ErrSessionInvalid = errors.New("invalid session")

func passwordFingerprint(passwordHash string) []byte {
	sum := sha256.Sum256([]byte(passwordHash))
	return sum[:]
}

// IssueSessionCookie builds the cookie value for a user, valid until expiry.
func IssueSessionCookie(key []byte, u *models.User, expiry time.Time) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plain := make([]byte, 0, sessionPlainLen)
	plain = binary.BigEndian.AppendUint64(plain, uint64(u.ID))
	plain = append(plain, passwordFingerprint(u.PasswordHash)...)
	plain = binary.BigEndian.AppendUint64(plain, uint64(expiry.Unix()))
	blob, err := seal(gcm, plain)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

// ValidateSessionCookie decodes a cookie value, rejects it when expired and
// returns the owning user after confirming the password fingerprint still
// matches. It also returns the cookie's expiry so the caller can derive when
// it was issued (to throttle sliding re-issuance). A genuinely invalid cookie
// yields an error wrapping ErrSessionInvalid; internal failures yield other
// errors.
func ValidateSessionCookie(db *sql.DB, key []byte, value string) (*models.User, time.Time, error) {
	if value == "" {
		return nil, time.Time{}, ErrSessionInvalid
	}
	blob, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, time.Time{}, ErrSessionInvalid
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, time.Time{}, err
	}
	plain, err := open(gcm, blob)
	if err != nil || len(plain) != sessionPlainLen {
		return nil, time.Time{}, ErrSessionInvalid
	}
	userID := int64(binary.BigEndian.Uint64(plain[:sessionUserIDLen]))
	storedHash := plain[sessionUserIDLen : sessionUserIDLen+sessionHashLen]
	expiry := time.Unix(int64(binary.BigEndian.Uint64(plain[sessionUserIDLen+sessionHashLen:])), 0)
	if time.Now().After(expiry) {
		return nil, time.Time{}, fmt.Errorf("session expired: %w", ErrSessionInvalid)
	}
	u, err := models.GetUserByID(db, userID)
	if err == sql.ErrNoRows {
		return nil, time.Time{}, fmt.Errorf("user not found: %w", ErrSessionInvalid)
	}
	if err != nil {
		// Internal failure (e.g. DB busy): NOT an invalid session.
		return nil, time.Time{}, fmt.Errorf("session user lookup failed: %w", err)
	}
	if subtle.ConstantTimeCompare(passwordFingerprint(u.PasswordHash), storedHash) != 1 {
		return nil, time.Time{}, fmt.Errorf("password changed: %w", ErrSessionInvalid)
	}
	return u, expiry, nil
}
