package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mailcare/app/modules/fileinuse"
)

// Control token (the system design document (Documents) 9.1): the control
// endpoints (/control/*) answer loopback clients only, and only with the
// token the running server wrote to its data directory together with its
// port. A process of the same OS user and data directory (the CLI) reads it
// and sends it to that port only; a reverse proxy on the same host, which
// reaches the server over loopback too, does not have it. On Windows the
// file is protected by the permissions of the data directory.

const (
	// ControlTokenFile is the file in the data directory that holds the
	// port and the token of the running server (JSON, mode 0600, rewritten
	// at every start once the server listens).
	ControlTokenFile = "control.token"
	// ControlTokenHeader carries the token on every control request.
	ControlTokenHeader = "X-MailCare-Control"
	// ControlTokenRefused is the error text of a control request without the
	// right token. The refusal also names the data directory of the server
	// (DataDirID), so the CLI can tell the server of its data directory from
	// another one.
	ControlTokenRefused = "control token missing or wrong"
	// controlTokenBytes is the size of the random token.
	controlTokenBytes = 32
)

// controlTokenContent is the JSON of ControlTokenFile.
type controlTokenContent struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// NewControlToken returns a fresh random control token.
func NewControlToken() (string, error) {
	return RandomHex(controlTokenBytes)
}

// DataDirID identifies a data directory without naming it: the first 16 hex
// digits of the SHA-256 of its canonical path (SameDataDir; lower case on
// Windows). It is no secret.
func DataDirID(dir string) string {
	c := canonicalDir(dir)
	if runtime.GOOS == "windows" {
		c = strings.ToLower(c)
	}
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:8])
}

// WriteControlToken stores the port and the token in the data directory with
// mode 0600 (a failing chmod is only logged; on Windows the directory's
// permissions protect it), replacing an older file in one step (a temporary
// file renamed over it; on Windows, where a reader may hold the old file
// open, the rename waits until it is closed or ctx ends: fileinuse.Retry).
func WriteControlToken(ctx context.Context, dataDir string, port int, token string) error {
	raw, err := json.Marshal(controlTokenContent{Port: port, Token: token})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, "."+ControlTokenFile+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // gone after the rename; left over only on failure
	if err := tmp.Chmod(0o600); err != nil {
		log.Printf("warning: could not restrict %s: %v", name, err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	target := filepath.Join(dataDir, ControlTokenFile)
	return fileinuse.Retry(ctx, func() error { return os.Rename(name, target) }, func(err error) {
		log.Printf("waiting for %s to be released: %v", target, err)
	})
}

// ReadControlToken returns the port and the token stored in the data
// directory (0 and "" when they cannot be read: no server runs, or another
// OS user owns the file).
func ReadControlToken(dataDir string) (int, string) {
	raw, err := os.ReadFile(filepath.Join(dataDir, ControlTokenFile))
	if err != nil {
		return 0, ""
	}
	var c controlTokenContent
	if json.Unmarshal(raw, &c) != nil {
		return 0, ""
	}
	return c.Port, c.Token
}

// RemoveControlToken removes the token file when it still holds token (the
// file of a server started later stays).
func RemoveControlToken(dataDir, token string) {
	if _, stored := ReadControlToken(dataDir); stored == token {
		_ = os.Remove(filepath.Join(dataDir, ControlTokenFile))
	}
}

// localControlToken is the token of the data directory of this process for
// a control request to port: only the server that wrote it listens there,
// so another port gets no token.
func localControlToken(port int) string {
	dir, err := DataDir()
	if err != nil {
		return ""
	}
	stored, token := ReadControlToken(dir)
	if stored != port {
		return ""
	}
	return token
}

// localDataDirID is DataDirID of the data directory of this process ("" when
// it cannot be resolved).
func localDataDirID() string {
	dir, err := DataDir()
	if err != nil {
		return ""
	}
	return DataDirID(dir)
}
