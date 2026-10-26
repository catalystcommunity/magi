package workshop

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// StageSource writes only a new temporary directory. It never applies an edit
// to the source checkout, executes candidate code, or copies a credential.
// The operator selects the parent. The bundle cannot select output paths.
func StageSource(checkout, parent string, bundle SourceBundle) (string, error) {
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, path := range []string{"harness", SourcePath} {
		info, e := root.Lstat(path)
		if e != nil || info.Mode()&os.ModeSymlink != 0 || path == SourcePath && !info.Mode().IsRegular() {
			return "", errors.New("source path is not an ordinary file or directory")
		}
	}
	file, err := root.Open(SourcePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return "", errors.New("invalid source file")
	}
	current, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(current) > 16384 {
		return "", errors.New("source file read failed")
	}
	if err = ValidateSource(bundle, current); err != nil {
		return "", err
	}
	path, err := os.MkdirTemp(parent, "opal-source-")
	if err != nil {
		return "", err
	}
	// A failed write leaves an incomplete artifact for inspection. No existing
	// directory is overwritten or removed. A complete manifest is written last.
	for _, sub := range []string{"base/harness", "source/harness"} {
		if err = os.MkdirAll(filepath.Join(path, sub), 0700); err != nil {
			return path, err
		}
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return path, err
	}
	files := []struct{ name, text string }{{"base/" + SourcePath, bundle.Base}, {"source/" + SourcePath, bundle.Candidate}, {"bundle.json", string(raw) + "\n"}}
	for _, file := range files {
		if err = os.WriteFile(filepath.Join(path, file.name), []byte(file.text), 0400); err != nil {
			return path, err
		}
	}
	manifest, err := json.MarshalIndent(map[string]any{"version": SourceBundleVersion, "state": "staged", "bundle_sha256": sourceDigest(append(raw, '\n')), "path": SourcePath, "base_digest": bundle.BaseDigest, "candidate_digest": bundle.CandidateDigest, "evidence": bundle.Evidence, "checks_digest": bundle.ChecksDigest, "source_checkout_changed": false, "build_performed": false, "deployment_authorized": false}, "", "  ")
	if err != nil {
		return path, err
	}
	err = os.WriteFile(filepath.Join(path, "manifest.json"), append(manifest, '\n'), 0400)
	return path, err
}
