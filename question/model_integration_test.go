//go:build integration && model

package question

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This test cannot pass through the deterministic transport fixture. The runner
// starts a hash-checked local model, and the CLI must receive its actual answer.
func TestQuestionRealModel(t *testing.T) {
	endpoint := os.Getenv("OPAL_QA_TEST_MODEL_URL")
	binary := os.Getenv("OPAL_QA_TEST_CLI")
	if endpoint == "" || binary == "" {
		t.Fatal("use npm run test:qa:model; a local model and built CLI are required")
	}
	f := newQAFixture(t, endpoint)
	f.serve(t)
	profile := filepath.Join(t.TempDir(), "client.json")
	raw, e := json.Marshal(f.profile)
	must(t, e)
	must(t, os.WriteFile(profile, raw, 0600))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "ask", "-profile", profile, "-run", "real-model-question", "-wait", "140s")
	command.Stdin = strings.NewReader("What is 2 + 2? Reply with only the single digit.\n")
	var output, diagnostic bytes.Buffer
	command.Stdout = &output
	command.Stderr = &diagnostic
	if e = command.Run(); e != nil {
		t.Fatalf("Q&A CLI failed: %v: %s", e, diagnostic.String())
	}
	if strings.TrimSpace(output.String()) != "4" {
		t.Fatalf("actual model answer was %q", output.String())
	}
	ev, e := f.client.Evidence(ctx, "real-model-question")
	must(t, e)
	if ev.Outcome != "returned" || ev.Response == nil || bytes.Contains(*ev.Response, []byte("fixture")) || !bytes.Contains(ev.Request, []byte("2 + 2")) {
		t.Fatal("missing real-model evidence")
	}
	// Retain the synthetic question and exact model exchange in the test report.
	rendered, e := DisplayEvidence(ev)
	must(t, e)
	t.Logf("Real model evidence: %s", rendered)
}
