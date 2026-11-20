package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory with no write permission does not keep an environment on
// the disk.
func TestRemoveTreeWithReadOnlyDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "env")
	ro := filepath.Join(root, "tmp", "cadir")
	if err := os.MkdirAll(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ro, "a.pem"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	if err := removeTree(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the environment is still there: %v", err)
	}
}
