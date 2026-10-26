package capture

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	wire "opal.local/experiments/knowledge/generated-capture/go"
)

func sampleBatch(t *testing.T) wire.CaptureBatch {
	s := new(Stream)
	must(t, s.Register(binding("one"), []byte("synthetic-secret-value")))
	return wire.CaptureBatch{SchemaVersion: 1, AttemptId: "attempt", StreamId: "stdout", Sequence: 1, Segments: captureAll(t, s, []byte("hello synthetic-secret-value!")), Final: true, Complete: true}
}
func openTestSpool(t *testing.T, dir string) *Spool {
	t.Helper()
	s, err := OpenSpool(dir)
	must(t, err)
	t.Cleanup(func() { must(t, s.Close()) })
	return s
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must(t, os.Chmod(dir, 0700))
	return dir
}

func TestSpoolRetriesAndConflicts(t *testing.T) {
	dir := privateDir(t)
	s := openTestSpool(t, dir)
	batch := sampleBatch(t)
	first, err := s.Put(batch)
	must(t, err)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := s.Put(batch)
			if err != nil || !bytes.Equal(receipt.Digest, first.Digest) {
				t.Error("retry changed receipt", err)
			}
		}()
	}
	wg.Wait()
	batch.Complete = false
	if _, err = s.Put(batch); !errors.Is(err, ErrConflict) {
		t.Fatal("changed bytes accepted under old sequence", err)
	}
	reopened := openTestSpool(t, dir)
	got, err := reopened.Read("attempt", "stdout", 1)
	must(t, err)
	if !got.Complete {
		t.Fatal("conflict overwrote accepted data")
	}
	manifest, err := reopened.Inspect("attempt", "stdout", 1)
	must(t, err)
	if !manifest.Complete {
		t.Fatal(manifest)
	}
	entries, err := os.ReadDir(dir)
	must(t, err)
	if len(entries) != 1 {
		t.Fatal("unexpected retained files", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	must(t, err)
	if bytes.Contains(data, []byte("synthetic-secret-value")) {
		t.Fatal("credential in durable spool")
	}
	if info, err := entries[0].Info(); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe file permissions")
	}
}

func TestSpoolManifestAndBadInputs(t *testing.T) {
	s := openTestSpool(t, privateDir(t))
	batch := sampleBatch(t)
	batch.Sequence = 3
	_, err := s.Put(batch)
	must(t, err)
	m, err := s.Inspect("attempt", "stdout", 3)
	must(t, err)
	if m.Complete || len(m.Missing) != 2 {
		t.Fatal("gaps reported complete")
	}
	batch.Sequence = 2
	batch.Segments = []wire.Segment{wire.GapSegment{Start: 0, End: 4, Reason: "producer_lost"}}
	if _, err = s.Put(batch); !errors.Is(err, ErrInvalid) {
		t.Fatal("gap accepted as complete")
	}
	batch.Complete = false
	_, err = s.Put(batch)
	must(t, err)
	batch.SchemaVersion = 2
	if _, err = s.Put(batch); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown schema accepted")
	}
	if _, err = s.Read("../../outside", "stdout", 1); err == nil {
		t.Fatal("invalid resource key accepted")
	}
	if _, err = s.Inspect("attempt", "stdout", 4097); !errors.Is(err, ErrLimit) {
		t.Fatal("manifest not bounded")
	}
}

func TestSpoolCorruptionAndSymlink(t *testing.T) {
	for _, kind := range []string{"corrupt", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateDir(t)
			s := openTestSpool(t, dir)
			batch := sampleBatch(t)
			name, _ := batchName(batch.AttemptId, batch.StreamId, batch.Sequence)
			if kind == "corrupt" {
				must(t, os.WriteFile(filepath.Join(dir, name), []byte("broken"), 0600))
			} else {
				must(t, os.Symlink("elsewhere", filepath.Join(dir, name)))
			}
			if _, err := s.Read(batch.AttemptId, batch.StreamId, batch.Sequence); !errors.Is(err, ErrInvalid) {
				t.Fatal("unsafe file read", err)
			}
		})
	}
}

func TestSpoolCrashRecovery(t *testing.T) {
	if point := os.Getenv("OPAL_CAPTURE_CRASH_POINT"); point != "" {
		s, err := OpenSpool(os.Getenv("OPAL_CAPTURE_CRASH_DIR"))
		must(t, err)
		s.checkpoint = func(reached string) {
			if reached == point {
				process, err := os.FindProcess(os.Getpid())
				must(t, err)
				must(t, process.Kill())
				select {}
			}
		}
		_, err = s.Put(sampleBatch(t))
		must(t, err)
		t.Fatal("crash point not reached")
		return
	}
	for _, point := range []string{"file_synced", "published", "directory_synced"} {
		t.Run(point, func(t *testing.T) {
			dir := privateDir(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestSpoolCrashRecovery$")
			cmd.Env = append(os.Environ(), "OPAL_CAPTURE_CRASH_POINT="+point, "OPAL_CAPTURE_CRASH_DIR="+dir)
			if err := cmd.Run(); err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != -1 {
				t.Fatal("child did not crash")
			}
			s := openTestSpool(t, dir)
			batch := sampleBatch(t)
			_, err := s.Read("attempt", "stdout", 1)
			if point == "file_synced" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unpublished file treated as accepted", err)
				}
			} else {
				must(t, err)
			}
			first, err := s.Put(batch)
			must(t, err)
			again, err := s.Put(batch)
			must(t, err)
			if !bytes.Equal(first.Digest, again.Digest) {
				t.Fatal("crash retry changed receipt")
			}
			m, err := s.Inspect("attempt", "stdout", 1)
			must(t, err)
			if !m.Complete {
				t.Fatal("retry did not recover")
			}
			entries, err := os.ReadDir(dir)
			must(t, err)
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				must(t, err)
				if bytes.Contains(data, []byte("synthetic-secret-value")) {
					t.Fatal("secret in crash residue")
				}
			}
		})
	}
}
