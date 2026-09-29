package modules

import (
	"os"
	"path/filepath"
	"testing"

	"mailcare/app/modules/dbschema"
)

// TestMain wires in the files the binary embeds (embed.go), read from the
// repository: the migrations, the templates and the language files.
func TestMain(m *testing.M) {
	root := filepath.Join("..", "..")
	dbschema.FS = os.DirFS(filepath.Join(root, "embedded"))
	EmbeddedFS = os.DirFS(filepath.Join(root, "embedded"))
	FrontendFS = os.DirFS(root)
	os.Exit(m.Run())
}
