package modules

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"mailcare/app/models"
)

// bcryptCost is the work factor for password hashes.
const bcryptCost = 12

// dummyPasswordHash is a valid bcrypt hash (cost 12) of a throwaway password.
// It is compared against when the username does not exist so that a login
// attempt takes the same time whether or not the user exists.
const dummyPasswordHash = "$2a$12$hU6M9KWBs4epQ45MC43i2.C7ThAbeorfWt51.wt5qKZoPkzlrLKGy"

// ErrInvalidCredentials is returned by the login helpers for any wrong
// username, password or token so that callers cannot tell them apart.
var ErrInvalidCredentials = errors.New("invalid credentials")

var (
	usernameRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	identifierRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	nonSlugRe    = regexp.MustCompile(`[^a-z0-9]+`)
)

// --- Users ---

// ValidateUsername checks the character set and length of a username.
func ValidateUsername(username string) error {
	if !usernameRe.MatchString(username) {
		return errors.New("username must be 1-64 characters of [A-Za-z0-9_.-]")
	}
	return nil
}

// ValidateRole checks that role is admin or user.
func ValidateRole(role string) error {
	if role != RoleAdmin && role != RoleUser {
		return fmt.Errorf("role must be %q or %q", RoleAdmin, RoleUser)
	}
	return nil
}

// ValidatePassword enforces the minimum password length.
func ValidatePassword(password string) error {
	if len(password) < PasswordMinLength {
		return fmt.Errorf("password must be at least %d characters", PasswordMinLength)
	}
	if len(password) > 256 {
		return errors.New("password must be 256 characters or fewer")
	}
	return nil
}

// HashPassword returns the bcrypt hash of a password.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CreateUser validates the input, hashes the password and inserts the user.
// An empty display name falls back to the username.
func CreateUser(db *sql.DB, username, displayName, password, role string) (*models.User, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if role == "" {
		role = RoleUser
	}
	if err := ValidateRole(role); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = username
	}
	if _, err := models.GetUserByUsername(db, username); err == nil {
		return nil, fmt.Errorf("user %q already exists", username)
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &models.User{Username: username, DisplayName: displayName, PasswordHash: hash, Role: role}
	if err := models.InsertUser(db, u); err != nil {
		return nil, err
	}
	return u, nil
}

// VerifyPassword reports whether password matches the user's hash.
func VerifyPassword(u *models.User, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

// ChangePassword validates and stores a new password. Every existing session
// of the user becomes invalid (sessions are bound to the password hash).
func ChangePassword(db *sql.DB, userID int64, newPassword string) error {
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return models.UpdateUserPassword(db, userID, hash)
}

// AuthenticateUser checks a username/password pair and returns the user.
// A wrong pair yields ErrInvalidCredentials; other errors are internal.
func AuthenticateUser(db *sql.DB, username, password string) (*models.User, error) {
	u, err := models.GetUserByUsername(db, strings.TrimSpace(username))
	if err == sql.ErrNoRows {
		// Burn comparable time so a missing user is not distinguishable.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyPasswordHash), []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(u, password) {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// AuthenticateToken checks a login token value and returns its owner and the
// token row. Unknown or expired tokens yield ErrInvalidCredentials.
func AuthenticateToken(db *sql.DB, value string) (*models.User, *models.Token, error) {
	tok, err := ValidateLoginToken(db, value)
	if err != nil {
		return nil, nil, err
	}
	u, err := models.GetUserByID(db, tok.UserID)
	if err == sql.ErrNoRows {
		return nil, nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, nil, err
	}
	return u, tok, nil
}

// --- Login tokens ---

// ValidateIdentifier checks a token identifier against the allowed character set.
func ValidateIdentifier(id string) error {
	if id == "" {
		return errors.New("identifier must not be empty")
	}
	if len(id) > 64 {
		return errors.New("identifier must be 64 characters or fewer")
	}
	if !identifierRe.MatchString(id) {
		return errors.New("identifier may only contain [A-Za-z0-9_-]")
	}
	return nil
}

// slugify produces a base identifier from a display name.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonSlugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// GenerateUniqueIdentifier returns an identifier derived from name that does
// not collide with an existing token.
func GenerateUniqueIdentifier(db *sql.DB, name string) (string, error) {
	base := slugify(name)
	if base == "" {
		r, err := RandomHex(6)
		if err != nil {
			return "", err
		}
		base = "token-" + r
	}
	candidate := base
	for i := 2; ; i++ {
		_, err := models.GetTokenByIdentifier(db, candidate)
		if err == sql.ErrNoRows {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
		if i > 1000 {
			return "", errors.New("could not allocate a unique identifier")
		}
	}
}

// GenerateTokenValue returns a new login token value (TokenPrefix + random hex).
func GenerateTokenValue() (string, error) {
	r, err := RandomHex(TokenRandomBytes)
	if err != nil {
		return "", err
	}
	return TokenPrefix + r, nil
}

// ParseExpiry parses an expiry argument. It accepts a Go duration (e.g. "720h")
// interpreted as time-from-now, or a timestamp (RFC 3339, "2006-01-02 15:04:05"
// or "2006-01-02", the latter two in local time). An empty string means no
// expiry (an invalid NullTime).
func ParseExpiry(s string) (sql.NullTime, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullTime{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return sql.NullTime{}, fmt.Errorf("invalid expiry %q (the duration must be positive)", s)
		}
		return sql.NullTime{Time: time.Now().Add(d).UTC(), Valid: true}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return sql.NullTime{Time: t.UTC(), Valid: true}, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return sql.NullTime{Time: t.UTC(), Valid: true}, nil
		}
	}
	return sql.NullTime{}, fmt.Errorf("invalid expiry %q (use a duration like 720h or an RFC3339 timestamp)", s)
}

// CreateLoginToken issues a login token for a user. The identifier is derived
// from name when empty. The returned Token carries the plain value, which is
// shown to the caller once.
func CreateLoginToken(db *sql.DB, userID int64, name, identifier string, expiresAt sql.NullTime) (*models.Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if len(name) > 128 {
		return nil, errors.New("name must be 128 characters or fewer")
	}
	if _, err := models.GetUserByID(db, userID); err == sql.ErrNoRows {
		return nil, errors.New("user not found")
	} else if err != nil {
		return nil, err
	}
	if identifier == "" {
		var err error
		identifier, err = GenerateUniqueIdentifier(db, name)
		if err != nil {
			return nil, err
		}
	} else {
		if err := ValidateIdentifier(identifier); err != nil {
			return nil, err
		}
		if _, err := models.GetTokenByIdentifier(db, identifier); err == nil {
			return nil, fmt.Errorf("identifier %q already exists", identifier)
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}
	value, err := GenerateTokenValue()
	if err != nil {
		return nil, err
	}
	tok := &models.Token{UserID: userID, Identifier: identifier, Name: name, Token: value, ExpiresAt: expiresAt}
	if err := models.InsertToken(db, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

// ValidateLoginToken looks up a token value and rejects unknown or expired
// tokens with ErrInvalidCredentials.
func ValidateLoginToken(db *sql.DB, value string) (*models.Token, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, TokenPrefix) {
		return nil, ErrInvalidCredentials
	}
	t, err := models.GetTokenByValue(db, value)
	if err == sql.ErrNoRows {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if t.IsExpired() {
		return nil, ErrInvalidCredentials
	}
	return t, nil
}

// DeleteLoginToken removes a token by identifier (sql.ErrNoRows when absent).
func DeleteLoginToken(db *sql.DB, identifier string) error {
	if _, err := models.GetTokenByIdentifier(db, identifier); err != nil {
		return err
	}
	return models.DeleteToken(db, identifier)
}
