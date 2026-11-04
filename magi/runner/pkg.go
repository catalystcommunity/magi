package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Platforms are the runner platforms that a release has packages for.
var Platforms = []string{"linux/amd64", "linux/arm64", "darwin/arm64"}

// Pack writes the runner package for goos/arch, built from the repository
// at src, to w. A package is a gzip tar file with magi-runner and
// apprentice/ (the harness and the Python clients). The same source and Go
// toolchain give the same bytes, so equal digests mean equal releases.
func Pack(src, goos, arch string, w io.Writer) error {
	tmp, err := os.MkdirTemp("", "magi-runner-pack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "magi-runner")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", bin, "./cmd/magi-runner")
	cmd.Dir = filepath.Join(src, "magi")
	cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+arch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build magi-runner %s/%s: %v\n%s", goos, arch, err, out)
	}
	files := map[string]string{"magi-runner": bin} // name in the package -> host path
	add := func(root, prefix string) error {
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			if d.Type().IsRegular() && strings.HasSuffix(p, ".py") {
				rel, _ := filepath.Rel(root, p)
				files[path.Join(prefix, filepath.ToSlash(rel))] = p
			}
			return nil
		})
	}
	if err := add(filepath.Join(src, "primitives", "apprentice", "magi_apprentice"), "apprentice/magi_apprentice"); err != nil {
		return err
	}
	clients, _ := filepath.Glob(filepath.Join(src, "clients", "python", "magi_*"))
	for _, c := range clients {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			if err := add(c, "apprentice/"+filepath.Base(c)); err != nil {
				return err
			}
		} else if strings.HasSuffix(c, ".py") {
			files["apprentice/"+filepath.Base(c)] = c
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	zw, _ := gzip.NewWriterLevel(w, gzip.BestCompression) // no name, no time
	tw := tar.NewWriter(zw)
	for _, n := range names {
		b, err := os.ReadFile(files[n])
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if n == "magi-runner" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: mode, Size: int64(len(b)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// PackBytes is Pack into memory.
func PackBytes(src, goos, arch string) ([]byte, error) {
	var b bytes.Buffer
	err := Pack(src, goos, arch, &b)
	return b.Bytes(), err
}

// Unpack extracts a runner package into dir, which must not exist. It
// accepts only regular files with relative paths in the package.
func Unpack(body []byte, dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s exists", dir)
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	seen := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if h.Typeflag != tar.TypeReg || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("bad package entry %q", h.Name)
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(tr, 256<<20))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		seen = seen || name == "magi-runner"
	}
	if !seen {
		return errors.New("the package has no magi-runner")
	}
	return nil
}
