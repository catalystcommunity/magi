package workshop

import (
	"bytes"
	"encoding/json"
	"github.com/catalystcommunity/magi/execution"
	w "github.com/catalystcommunity/magi/generated/go"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sourceFixture(t testing.TB) (w.ScopeRef, Record) {
	t.Helper()
	owner := w.ScopeRef{Authority: w.IdentityRef{Domain: "local.opal.test", AccountId: "tester"}, ScopeId: "public"}
	record := Record{Run: "dev-fixture", EnvironmentID: "env-fixture", State: "candidate_ready", Candidate: "def format_elapsed(ms): return str(ms)\n"}
	record.Digest = sourceDigest([]byte(record.Candidate))
	for i, operation := range []string{"seed", "final-check", "export"} {
		proposal := execution.Proposal{Tool: "check", Arguments: map[string]string{}}
		result := execution.Result{Outcome: "returned", ExitCode: 0}
		switch operation {
		case "seed":
			proposal = execution.Proposal{Tool: "write_file", Arguments: map[string]string{"path": "solution.py", "text": "def format_elapsed(ms): return 'old'\n"}}
		case "final-check":
			result.CheckedDigest = record.Digest
		case "export":
			proposal = execution.Proposal{Tool: "read_file", Arguments: map[string]string{"path": "solution.py"}}
			result.Text = record.Candidate
		}
		request, _ := json.Marshal(map[string]any{"proposal": proposal, "decision": "allowed", "policy": execution.PolicyRevision, "environment": record.EnvironmentID, "image": execution.PythonImage})
		response, _ := json.Marshal(result)
		at := time.Unix(1000+int64(i)*10, 0).UTC()
		finished := at.Add(time.Second)
		record.Events = append(record.Events, Event{Operation: operation, Kind: "tool", Request: request, Response: response, Outcome: "returned", StartedAt: at, FinishedAt: &finished})
	}
	return owner, record
}

func sourceBundle(t testing.TB) SourceBundle {
	t.Helper()
	owner, record := sourceFixture(t)
	bundle, err := PrepareSource(owner, owner.Authority, record)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestSourceBundleSurvivesCleanupWithoutChangingItsBytes(t *testing.T) {
	owner, record := sourceFixture(t)
	before, err := PrepareSource(owner, owner.Authority, record)
	if err != nil {
		t.Fatal(err)
	}
	record.State = "closed"
	record.Events = append(record.Events, Event{Operation: "environment-cleanup", Kind: "environment"})
	after, err := PrepareSource(owner, owner.Authority, record)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) || ValidateSource(after, []byte(before.Base)) != nil {
		t.Fatal("source bundle changed after cleanup")
	}
}

func TestSourceBundleRequiresMatchingSuccessfulEvidence(t *testing.T) {
	changes := map[string]func(*Record){
		"running":        func(r *Record) { r.State = "running" },
		"failed":         func(r *Record) { r.State = "checks_failed" },
		"candidate":      func(r *Record) { r.Candidate += "# changed" },
		"missing-check":  func(r *Record) { r.Events = append(r.Events[:1], r.Events[2:]...) },
		"duplicate":      func(r *Record) { r.Events = append(r.Events, r.Events[1]) },
		"unfinished":     func(r *Record) { r.Events[1].FinishedAt = nil },
		"unknown":        func(r *Record) { r.Events[1].Outcome = "unknown" },
		"late-check":     func(r *Record) { r.Events[1].StartedAt = r.Events[2].StartedAt.Add(time.Hour) },
		"wrong-env":      func(r *Record) { r.EnvironmentID = "other" },
		"unsafe-run":     func(r *Record) { r.Run = "../../escape" },
		"empty-response": func(r *Record) { r.Events[1].Response = json.RawMessage(`{}`) },
		"no-exit-code": func(r *Record) {
			r.Events[1].Response = json.RawMessage(`{"outcome":"returned","text":"ok","checked_digest":"` + r.Digest + `"}`)
		},
		"wrong-digest": func(r *Record) {
			r.Events[1].Response = json.RawMessage(`{"outcome":"returned","text":"ok","exit_code":0,"checked_digest":"wrong"}`)
		},
		"failed-check": func(r *Record) {
			r.Events[1].Response = json.RawMessage(`{"outcome":"returned","text":"no","exit_code":1,"checked_digest":"` + r.Digest + `"}`)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			owner, record := sourceFixture(t)
			change(&record)
			if _, err := PrepareSource(owner, owner.Authority, record); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestSourceBundleRefusesChangedHashesPathsAndBase(t *testing.T) {
	for _, change := range []func(*SourceBundle){
		func(b *SourceBundle) { b.Path = "../outside" },
		func(b *SourceBundle) { b.BaseDigest = "wrong" },
		func(b *SourceBundle) { b.CandidateDigest = "wrong" },
		func(b *SourceBundle) { b.ChecksDigest = "wrong" },
		func(b *SourceBundle) { b.Version = "future" },
		func(b *SourceBundle) { b.Evidence.Kind = "release-approval" },
	} {
		bundle := sourceBundle(t)
		change(&bundle)
		if ValidateSource(bundle, []byte(bundle.Base)) == nil {
			t.Fatal("invalid bundle accepted")
		}
	}
	bundle := sourceBundle(t)
	if ValidateSource(bundle, []byte("human changed the base")) == nil {
		t.Fatal("stale base accepted")
	}
}

func TestStageSourceWritesOnlyAFreshArtifact(t *testing.T) {
	bundle := sourceBundle(t)
	checkout, output := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, "harness"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, SourcePath), []byte(bundle.Base), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "private-token"), []byte("do not copy"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := StageSource(checkout, output, bundle)
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		t.Helper()
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return string(raw)
	}
	if read(filepath.Join(checkout, SourcePath)) != bundle.Base || read(filepath.Join(path, "source", SourcePath)) != bundle.Candidate || read(filepath.Join(path, "base", SourcePath)) != bundle.Base {
		t.Fatal("source copy or base changed")
	}
	if _, err := os.Stat(filepath.Join(path, "source", "private-token")); !os.IsNotExist(err) {
		t.Fatal("unlisted input copied")
	}
	var manifest map[string]any
	if json.Unmarshal([]byte(read(filepath.Join(path, "manifest.json"))), &manifest) != nil || manifest["build_performed"] != false || manifest["deployment_authorized"] != false || manifest["bundle_sha256"] != sourceDigest([]byte(read(filepath.Join(path, "bundle.json")))) {
		t.Fatal("incorrect artifact claim")
	}
	other, err := StageSource(checkout, output, bundle)
	if err != nil || other == path {
		t.Fatal("existing artifact reused", err)
	}
	if err = os.WriteFile(filepath.Join(checkout, SourcePath), []byte("human edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = StageSource(checkout, output, bundle); err == nil {
		t.Fatal("stale checkout accepted")
	}
}

func TestStageSourceRefusesSymlink(t *testing.T) {
	checkout, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(checkout, "harness")); err != nil {
		t.Fatal(err)
	}
	if _, err := StageSource(checkout, t.TempDir(), sourceBundle(t)); err == nil {
		t.Fatal("symlink accepted")
	}
}
