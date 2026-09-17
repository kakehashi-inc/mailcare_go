package modules

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"develop_app/app/models"
)

var identifierRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateIdentifier checks an identifier against the allowed character set.
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

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

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
		// Fall back to a random identifier.
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

// GenerateTokenValue returns a new bearer token string (TokenPrefix + random hex).
func GenerateTokenValue() (string, error) {
	r, err := RandomHex(TokenRandomBytes)
	if err != nil {
		return "", err
	}
	return TokenPrefix + r, nil
}

// ParseExpiry parses an expiry argument. It accepts a Go duration (e.g. "720h")
// interpreted as time-from-now, or an RFC3339 timestamp. An empty string means
// no expiry (returns an invalid NullTime).
func ParseExpiry(s string) (sql.NullTime, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullTime{}, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return sql.NullTime{Time: time.Now().Add(d).UTC(), Valid: true}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return sql.NullTime{Time: t.UTC(), Valid: true}, nil
		}
	}
	return sql.NullTime{}, fmt.Errorf("invalid expiry %q (use a duration like 720h or an RFC3339 timestamp)", s)
}

// CreateToken creates a token, allocating an identifier from name when empty,
// generating the token value and marking default appropriately.
func CreateToken(db *sql.DB, name, identifier string, expiresAt sql.NullTime, makeDefault bool) (*models.Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
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

	count, err := models.CountTokens(db)
	if err != nil {
		return nil, err
	}
	isDefault := makeDefault || count == 0

	tok := &models.Token{
		Identifier: identifier,
		Name:       name,
		Token:      value,
		IsDefault:  isDefault,
		ExpiresAt:  expiresAt,
	}
	if err := models.InsertToken(db, tok); err != nil {
		return nil, err
	}
	// Ensure exactly one default if this one is default.
	if isDefault {
		if err := models.SetDefaultToken(db, identifier); err != nil {
			return nil, err
		}
		tok.IsDefault = true
	}
	return tok, nil
}

// ResolveToken returns the token for the given identifier, or the default token
// when identifier is empty.
func ResolveToken(db *sql.DB, identifier string) (*models.Token, error) {
	if identifier == "" {
		t, err := models.GetDefaultToken(db)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no default token; create one with: %s token create --identifier <identifier>", AppName)
		}
		return t, err
	}
	t, err := models.GetTokenByIdentifier(db, identifier)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("token %q not found", identifier)
	}
	return t, err
}

// ValidateBearer looks up a bearer token value and rejects expired/unknown
// tokens. Returns the token on success.
func ValidateBearer(db *sql.DB, value string) (*models.Token, error) {
	if value == "" {
		return nil, errors.New("missing token")
	}
	t, err := models.GetTokenByValue(db, value)
	if err == sql.ErrNoRows {
		return nil, errors.New("invalid token")
	}
	if err != nil {
		return nil, err
	}
	if t.IsExpired() {
		return nil, errors.New("token expired")
	}
	return t, nil
}
