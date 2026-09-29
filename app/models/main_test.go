package models

import (
	"os"
	"path/filepath"
	"testing"

	"mailcare/app/modules/dbschema"
)

// TestMain wires in the files the binary embeds (embed.go), read from the
// repository: the migrations.
func TestMain(m *testing.M) {
	root := filepath.Join("..", "..")
	dbschema.FS = os.DirFS(filepath.Join(root, "embedded"))
	os.Exit(m.Run())
}
