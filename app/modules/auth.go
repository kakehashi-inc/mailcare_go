package modules

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
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
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	// emailRe accepts a plausible address: one local part, one "@", a dotted
	// domain with at least one dot, no whitespace and no angle brackets.
	emailRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~.-]+@[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
)

// MaxEmailLength bounds a mail address (RFC 5321 path limit).
const MaxEmailLength = 254

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

// NormalizeEmail trims and lower-cases a notification address and checks
// that it is empty (no address) or plausible: one local part, one "@" and a
// dotted domain, at most MaxEmailLength characters.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", nil
	}
	if len(email) > MaxEmailLength {
		return "", fmt.Errorf("email must be %d characters or fewer", MaxEmailLength)
	}
	if !emailRe.MatchString(email) {
		return "", fmt.Errorf("invalid email address %q", email)
	}
	return email, nil
}

// MaxTimezoneLength bounds an IANA zone name (users.timezone).
const MaxTimezoneLength = 64

// NormalizeTimezone trims a display timezone and checks that it is an IANA
// zone name the runtime can load (e.g. "Asia/Tokyo", "UTC"). Empty means
// the default. "Local" is rejected: it names the server's zone, not one of
// the user's.
func NormalizeTimezone(tz string) (string, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return models.DefaultTimezone, nil
	}
	if len(tz) > MaxTimezoneLength || tz == "Local" {
		return "", fmt.Errorf("invalid timezone %q (use an IANA name such as Asia/Tokyo)", tz)
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", fmt.Errorf("invalid timezone %q (use an IANA name such as Asia/Tokyo)", tz)
	}
	return tz, nil
}

// UserLocation returns the display location of a user (the default zone
// when the stored name cannot be loaded).
func UserLocation(u *models.User) *time.Location {
	if loc, err := time.LoadLocation(u.Timezone); err == nil && u.Timezone != "" {
		return loc
	}
	loc, err := time.LoadLocation(models.DefaultTimezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ServerTimezone returns the name of the server's local zone, in which the
// check times and the notify time are interpreted: the zone name of
// time.Local when known, else the TZ environment variable, else the
// /etc/localtime link target, else the current abbreviation and offset.
func ServerTimezone() string {
	name := time.Local.String()
	if name != "" && name != "Local" {
		return name
	}
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(link, "zoneinfo/"); i >= 0 {
			return link[i+len("zoneinfo/"):]
		}
	}
	return time.Now().Format("MST -07:00")
}

// Languages and themes a user may choose (users.language / users.theme).
var (
	KnownLanguages = []string{"ja", "en"}
	KnownThemes    = []string{"auto", "light", "dark"}
)

// NormalizeLanguage trims a UI language and checks it is one of
// KnownLanguages. Empty means the default.
func NormalizeLanguage(lang string) (string, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return models.DefaultLanguage, nil
	}
	if !slices.Contains(KnownLanguages, lang) {
		return "", fmt.Errorf("invalid language %q (use %s)", lang, strings.Join(KnownLanguages, " or "))
	}
	return lang, nil
}

// NormalizeTheme trims a UI theme and checks it is one of KnownThemes.
// Empty means the default.
func NormalizeTheme(theme string) (string, error) {
	theme = strings.ToLower(strings.TrimSpace(theme))
	if theme == "" {
		return models.DefaultTheme, nil
	}
	if !slices.Contains(KnownThemes, theme) {
		return "", fmt.Errorf("invalid theme %q (use %s)", theme, strings.Join(KnownThemes, ", "))
	}
	return theme, nil
}

// NewUser is the input of CreateUserFrom. Empty preferences mean the
// defaults; an empty display name falls back to the username.
type NewUser struct {
	Username    string
	DisplayName string
	Email       string
	Language    string
	Timezone    string
	Theme       string
	Password    string
	Role        string
}

// CreateUser validates the input, hashes the password and inserts the user
// with the default preferences and no notification address.
func CreateUser(db *sql.DB, username, displayName, password, role string) (*models.User, error) {
	return CreateUserFrom(db, NewUser{Username: username, DisplayName: displayName, Password: password, Role: role})
}

// CreateUserWithEmail is CreateUser with an optional notification address.
func CreateUserWithEmail(db *sql.DB, username, displayName, email, password, role string) (*models.User, error) {
	return CreateUserFrom(db, NewUser{Username: username, DisplayName: displayName, Email: email, Password: password, Role: role})
}

// CreateUserFrom validates every field of in, hashes the password and
// inserts the user.
func CreateUserFrom(db *sql.DB, in NewUser) (*models.User, error) {
	username := strings.TrimSpace(in.Username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	role := in.Role
	if role == "" {
		role = RoleUser
	}
	if err := ValidateRole(role); err != nil {
		return nil, err
	}
	if err := ValidatePassword(in.Password); err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = username
	}
	if len(displayName) > 128 {
		return nil, errors.New("display name must be 128 characters or fewer")
	}
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	language, err := NormalizeLanguage(in.Language)
	if err != nil {
		return nil, err
	}
	timezone, err := NormalizeTimezone(in.Timezone)
	if err != nil {
		return nil, err
	}
	theme, err := NormalizeTheme(in.Theme)
	if err != nil {
		return nil, err
	}
	if _, err := models.GetUserByUsername(db, username); err == nil {
		return nil, fmt.Errorf("user %q already exists", username)
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	u := &models.User{Username: username, DisplayName: displayName, Email: email, Language: language, Timezone: timezone,
		Theme: theme, PasswordHash: hash, Role: role}
	if err := models.InsertUser(db, u); err != nil {
		return nil, err
	}
	return u, nil
}

// ProfileInput carries the profile fields a user (or an administrator on
// their behalf) may change; nil fields are left unchanged. An empty Email
// clears the address; an empty preference restores its default.
type ProfileInput struct {
	DisplayName *string
	Email       *string
	Language    *string
	Timezone    *string
	Theme       *string
}

// ApplyProfile validates the given fields against the current user and
// returns the resulting profile values (display name, email, language,
// timezone, theme) without storing them.
func ApplyProfile(u *models.User, in ProfileInput) (displayName, email, language, timezone, theme string, err error) {
	displayName, email, language, timezone, theme = u.DisplayName, u.Email, u.Language, u.Timezone, u.Theme
	if in.DisplayName != nil {
		displayName = strings.TrimSpace(*in.DisplayName)
		if displayName == "" {
			displayName = u.Username
		}
		if len(displayName) > 128 {
			return "", "", "", "", "", errors.New("display name must be 128 characters or fewer")
		}
	}
	if in.Email != nil {
		if email, err = NormalizeEmail(*in.Email); err != nil {
			return "", "", "", "", "", err
		}
	}
	if in.Language != nil {
		if language, err = NormalizeLanguage(*in.Language); err != nil {
			return "", "", "", "", "", err
		}
	}
	if in.Timezone != nil {
		if timezone, err = NormalizeTimezone(*in.Timezone); err != nil {
			return "", "", "", "", "", err
		}
	}
	if in.Theme != nil {
		if theme, err = NormalizeTheme(*in.Theme); err != nil {
			return "", "", "", "", "", err
		}
	}
	return displayName, email, language, timezone, theme, nil
}

// UpdateProfile validates and stores the profile fields of a user.
func UpdateProfile(db *sql.DB, u *models.User, in ProfileInput) error {
	displayName, email, language, timezone, theme, err := ApplyProfile(u, in)
	if err != nil {
		return err
	}
	return models.UpdateUserProfile(db, u.ID, displayName, email, language, timezone, theme)
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
