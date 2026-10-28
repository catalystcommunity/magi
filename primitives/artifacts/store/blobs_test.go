package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigest(t *testing.T) {
	// sha256 of the empty string.
	if got := Digest(nil); got != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("Digest(nil) = %s", got)
	}
	for d, want := range map[string]bool{
		Digest([]byte("x")):                    true,
		"sha256:" + strings.Repeat("A", 64):    false,
		"sha256:" + strings.Repeat("a", 63):    false,
		"md5:" + strings.Repeat("a", 64):       false,
		"sha256:../" + strings.Repeat("a", 61): false,
	} {
		if ValidDigest(d) != want {
			t.Errorf("ValidDigest(%q) = %v", d, !want)
		}
	}
}

func TestBlobsWriteRead(t *testing.T) {
	b := Blobs{Dir: t.TempDir()}
	d, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if d2, err := b.Write([]byte("hello")); err != nil || d2 != d {
		t.Fatalf("second write: %s %v", d2, err)
	}
	got, err := b.Read(d)
	if err != nil || string(got) != "hello" {
		t.Fatalf("read: %q %v", got, err)
	}
	if _, err := b.Read(Digest([]byte("other"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing read: %v", err)
	}
	if _, err := b.Read("sha256:bad"); err == nil {
		t.Fatal("bad digest read succeeded")
	}
	h := d[len("sha256:"):]
	entries, _ := os.ReadDir(filepath.Join(b.Dir, "sha256", h[:2]))
	if len(entries) != 1 || entries[0].Name() != h {
		t.Fatalf("directory has %v, want only %s", entries, h)
	}
}
