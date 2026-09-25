package mailengine

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ErrInvalidMessageKey is returned when a message key does not have the
// expected shape (it would otherwise be able to address files outside the
// mailbox directory).
var ErrInvalidMessageKey = errors.New("mailengine: invalid message key")

// messageKeyPattern is the shape of every key produced by MessageKey:
// YYYYMMDD-HHMMSS_<12 hex digits>.
var messageKeyPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}_[0-9a-f]{12}$`)

// windowsReservedNames are device names that Windows refuses as file names,
// with or without an extension, in any letter case.
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeAddress maps a mail address to a name that is safe as a directory or
// file name on Windows, macOS and Linux (system design document, section 3.1):
// only A-Z a-z 0-9 . _ - @ + are kept, everything else becomes "_", a trailing
// "." or space becomes "_", Windows reserved device names get a leading "_"
// and an empty result becomes "_". Letter case is preserved.
func SanitizeAddress(address string) string {
	var b strings.Builder
	b.Grow(len(address))
	for _, r := range address {
		switch {
		case r > unicode.MaxASCII:
			b.WriteByte('_')
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-', r == '@', r == '+':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	// Trailing dots and spaces are stripped silently by Windows; spaces were
	// already replaced above, so only dots remain.
	for strings.HasSuffix(s, ".") {
		s = s[:len(s)-1] + "_"
	}
	if s == "" {
		return "_"
	}
	base := s
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if windowsReservedNames[strings.ToUpper(base)] {
		s = "_" + s
	}
	return s
}

// MailboxDir returns mailsRoot/<sanitized address>.
func MailboxDir(mailsRoot, address string) string {
	return filepath.Join(mailsRoot, SanitizeAddress(address))
}

// MailboxIndexPath returns mailsRoot/<sanitized address>.sqlite.
func MailboxIndexPath(mailsRoot, address string) string {
	return filepath.Join(mailsRoot, SanitizeAddress(address)+".sqlite")
}

// ValidMessageKey reports whether key has the shape produced by MessageKey.
// Keys of any other shape are rejected by the file helpers so that a key
// coming from an HTTP request can never leave the mailbox directory.
func ValidMessageKey(key string) bool {
	return messageKeyPattern.MatchString(key)
}

// MessageDir returns the directory that holds the files of one message
// inside the mailbox directory dir (design 3.2): <dir>/<YYYY>/<MM>, the year
// and month of the message date taken from the key (UTC). Splitting by month
// keeps every directory small; a single directory with every message slows
// file access down considerably on Windows. An invalid key yields "".
func MessageDir(dir, key string) string {
	if !ValidMessageKey(key) {
		return ""
	}
	return filepath.Join(dir, key[0:4], key[4:6])
}

// rawFilePath returns the path of the raw file (<key>.eml) of a message
// inside the mailbox directory dir, or "" for an invalid key.
func rawFilePath(dir, key string) string {
	if !ValidMessageKey(key) {
		return ""
	}
	return filepath.Join(MessageDir(dir, key), key+".eml")
}

// MessageFilePath returns the path of the raw file (<key>.eml) of a message;
// the body sections are addressed with SectionFilePath. An invalid key or an
// extension other than "eml" yields "" (ReadMessageFile reports the error).
func MessageFilePath(mailsRoot, address, messageKey, ext string) string {
	if ext != "eml" {
		return ""
	}
	return rawFilePath(MailboxDir(mailsRoot, address), messageKey)
}

// validSectionExt reports whether ext names a body section kind.
func validSectionExt(ext string) bool {
	return ext == "txt" || ext == "html"
}

// SectionFilePath returns the path of the n-th body section file of a
// message inside the mailbox directory dir (design 3.2): sections are
// numbered from 1, <key>-<n>.<ext>, whatever their count, and live next to
// the raw file in MessageDir. ext is "txt" or "html". An invalid key,
// extension or n < 1 yields "".
func SectionFilePath(dir, key, ext string, n int) string {
	if !ValidMessageKey(key) || !validSectionExt(ext) || n < 1 {
		return ""
	}
	return filepath.Join(MessageDir(dir, key), sectionFileName(key, ext, n))
}

// sectionFileName is the file name of the n-th section (n >= 1) of one kind.
func sectionFileName(key, ext string, n int) string {
	return key + "-" + strconv.Itoa(n) + "." + ext
}

// ReadMessageFile reads the raw file (.eml) of a message. A missing file
// yields os.ErrNotExist.
func ReadMessageFile(mailsRoot, address, messageKey, ext string) ([]byte, error) {
	path := MessageFilePath(mailsRoot, address, messageKey, ext)
	if path == "" {
		return nil, ErrInvalidMessageKey
	}
	return os.ReadFile(path)
}

// ReadBodySection reads the n-th body section (n >= 1) of one kind of a
// message: <key>-<n>.<ext> with ext "txt" or "html". A missing file yields
// os.ErrNotExist, an invalid key ErrInvalidMessageKey, an invalid extension
// or n an error.
func ReadBodySection(mailsRoot, address, key, ext string, n int) (string, error) {
	if !ValidMessageKey(key) {
		return "", ErrInvalidMessageKey
	}
	if !validSectionExt(ext) {
		return "", fmt.Errorf("mailengine: %q is not a body section extension", ext)
	}
	if n < 1 {
		return "", fmt.Errorf("mailengine: section number %d is not positive", n)
	}
	data, err := os.ReadFile(SectionFilePath(MailboxDir(mailsRoot, address), key, ext, n))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ReadBodySections reads the body sections of one kind of a message in
// order: <key>-1.<ext>, <key>-2.<ext>, ... up to count files (ext "txt" with
// messages.text_count, "html" with messages.html_count). A section file that
// does not exist is skipped, so the result can be shorter than count; it is
// never nil. An invalid key yields ErrInvalidMessageKey, an extension other
// than txt / html an error.
func ReadBodySections(mailsRoot, address, key, ext string, count int) ([]string, error) {
	if !ValidMessageKey(key) {
		return nil, ErrInvalidMessageKey
	}
	if !validSectionExt(ext) {
		return nil, fmt.Errorf("mailengine: %q is not a body section extension", ext)
	}
	dir := MailboxDir(mailsRoot, address)
	out := []string{}
	for n := 1; n <= count; n++ {
		data, err := os.ReadFile(SectionFilePath(dir, key, ext, n))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, string(data))
	}
	return out, nil
}

// DeleteMailboxData removes the raw files directory and the index of a mailbox
// (including the SQLite WAL side files). Missing files are not an error.
func DeleteMailboxData(mailsRoot, address string) error {
	if err := os.RemoveAll(MailboxDir(mailsRoot, address)); err != nil {
		return err
	}
	return removeIndexFiles(MailboxIndexPath(mailsRoot, address))
}

// removeIndexFiles deletes an index database and its -wal / -shm side files.
func removeIndexFiles(indexPath string) error {
	for _, p := range []string{indexPath, indexPath + "-wal", indexPath + "-shm", indexPath + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// MessageKey builds the file name base of one message (design 3.2):
// <YYYYMMDD-HHMMSS>_<hash> where the date is the UTC form of date (the caller
// passes the Date header, else INTERNALDATE, else the fetch time) and hash is
// the first 12 hex digits of sha1(folder + "\x00" + uidvalidity + "\x00" +
// uid). The same message therefore always yields the same key.
func MessageKey(date time.Time, folder string, uidValidity, uid uint32) string {
	sum := sha1.Sum([]byte(folder + "\x00" + strconv.FormatUint(uint64(uidValidity), 10) + "\x00" +
		strconv.FormatUint(uint64(uid), 10)))
	return date.UTC().Format("20060102-150405") + "_" + hex.EncodeToString(sum[:])[:12]
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory followed by a rename, so readers never observe a partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s: %w", filepath.Base(path), err)
	}
	return nil
}
