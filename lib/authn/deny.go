package authn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DenyListEnv names the environment variable that LoadVerifier reads. When
// it is set, every verifier made by LoadVerifier refuses the tokens that
// the deny list file names.
const DenyListEnv = "MAGI_DENY_LIST"

// DefaultDenyReload is how often a verifier looks at the deny list file for
// a change. A revocation takes effect in a running service within this time
// after the file changes on disk. A service does not need a restart.
const DefaultDenyReload = 10 * time.Second

// ErrRevoked is returned for a token that the deny list names.
var ErrRevoked = errors.New("token revoked")

// denyFile is the on-disk form of the deny list.
//
//	{"subjects": {"runner:laptop": 1767225600}, "tokens": ["<jti>"]}
//
// A subject entry refuses every token of that subject issued at or before
// the Unix time (seconds). A token minted after that time works again. A
// token entry refuses the one token with that id (the "jti" of the token).
type denyFile struct {
	Subjects map[string]int64 `json:"subjects,omitempty"`
	Tokens   []string         `json:"tokens,omitempty"`
}

// DenyList is a revocation list that follows a file. It is safe for
// concurrent use. A missing file is an empty list. A file that cannot be
// parsed keeps the last good list (a half-written file must not lift a
// revocation); Err reports it.
type DenyList struct {
	Path     string
	Interval time.Duration    // zero means DefaultDenyReload
	Now      func() time.Time // clock for the reload timer; nil means time.Now

	mu       sync.Mutex
	checked  time.Time
	modTime  time.Time
	size     int64
	loaded   bool
	subjects map[string]int64
	tokens   map[string]bool
	err      error
}

// LoadDenyList reads path now. A missing file is an empty list; any other
// failure to read or parse it is an error, so a service does not start with
// a list it cannot read.
func LoadDenyList(path string) (*DenyList, error) {
	d := &DenyList{Path: path}
	if err := d.reload(); err != nil {
		return nil, err
	}
	d.checked = d.now()
	return d, nil
}

func (d *DenyList) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Err returns the last reload error, or nil.
func (d *DenyList) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// reload reads the file if it changed. The caller holds mu, or owns d.
func (d *DenyList) reload() error {
	st, err := os.Stat(d.Path)
	if errors.Is(err, os.ErrNotExist) {
		d.subjects, d.tokens, d.loaded, d.modTime, d.size = nil, nil, true, time.Time{}, 0
		return nil
	}
	if err != nil {
		return err
	}
	if d.loaded && st.ModTime().Equal(d.modTime) && st.Size() == d.size {
		return nil
	}
	b, err := os.ReadFile(d.Path)
	if err != nil {
		return err
	}
	var f denyFile
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("deny list %s: %w", d.Path, err)
	}
	d.subjects = f.Subjects
	d.tokens = make(map[string]bool, len(f.Tokens))
	for _, id := range f.Tokens {
		d.tokens[id] = true
	}
	d.loaded, d.modTime, d.size = true, st.ModTime(), st.Size()
	return nil
}

// Denied reports whether the deny list refuses p. It looks at the file at
// most once per Interval.
func (d *DenyList) Denied(p Principal) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultDenyReload
	}
	if now := d.now(); now.Sub(d.checked) >= interval {
		d.checked = now
		d.err = d.reload()
	}
	if cut, ok := d.subjects[p.Subject]; ok && p.IssuedAt <= cut {
		return true
	}
	return p.ID != "" && d.tokens[p.ID]
}

// updateDenyFile reads path (a missing file is empty), applies change, and
// replaces the file with a rename, so a reader never sees half a file.
func updateDenyFile(path string, change func(*denyFile)) error {
	var f denyFile
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &f); err != nil {
			return fmt.Errorf("deny list %s: %w", path, err)
		}
	}
	change(&f)
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".deny-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// RevokeSubject adds subject to the deny list file at path: every token of
// the subject issued at or before at is refused.
func RevokeSubject(path, subject string, at time.Time) error {
	if subject == "" {
		return errors.New("revoke: subject is required")
	}
	return updateDenyFile(path, func(f *denyFile) {
		if f.Subjects == nil {
			f.Subjects = map[string]int64{}
		}
		f.Subjects[subject] = max(f.Subjects[subject], at.Unix())
	})
}

// RevokeToken adds one token id (the "jti" of a token) to the deny list file.
func RevokeToken(path, id string) error {
	if id == "" {
		return errors.New("revoke: token id is required")
	}
	return updateDenyFile(path, func(f *denyFile) {
		for _, t := range f.Tokens {
			if t == id {
				return
			}
		}
		f.Tokens = append(f.Tokens, id)
	})
}
