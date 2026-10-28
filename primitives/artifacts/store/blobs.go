package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Blobs stores bodies by digest under Dir: <Dir>/sha256/<2 hex>/<64 hex>.
type Blobs struct{ Dir string }

// Digest returns "sha256:<hex>" of body.
func Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidDigest reports whether d is "sha256:" and 64 lowercase hex digits.
func ValidDigest(d string) bool {
	h, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (b Blobs) path(digest string) (string, error) {
	if !ValidDigest(digest) {
		return "", fmt.Errorf("bad digest %q", digest)
	}
	h := digest[len("sha256:"):]
	return filepath.Join(b.Dir, "sha256", h[:2], h), nil
}

// Write stores body under its digest and returns the digest. It writes a
// temporary file and renames it, so a reader never sees a partial body.
func (b Blobs) Write(body []byte) (string, error) {
	digest := Digest(body)
	p, err := b.path(digest)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return digest, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return digest, os.Rename(f.Name(), p)
}

// Read returns the body for digest. A missing body returns ErrNotFound.
func (b Blobs) Read(digest string) ([]byte, error) {
	p, err := b.path(digest)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return body, err
}
