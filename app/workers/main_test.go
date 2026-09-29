package workers

import (
	"os"
	"path/filepath"
	"testing"

	"mailcare/app/modules"
	"mailcare/app/modules/dbschema"
)

// TestMain wires in the files the binary embeds (embed.go), read from the
// repository: the migrations, the templates, the fonts and the language files.
func TestMain(m *testing.M) {
	root := filepath.Join("..", "..")
	dbschema.FS = os.DirFS(filepath.Join(root, "embedded"))
	modules.EmbeddedFS = os.DirFS(filepath.Join(root, "embedded"))
	modules.FrontendFS = os.DirFS(root)
	os.Exit(m.Run())
}
