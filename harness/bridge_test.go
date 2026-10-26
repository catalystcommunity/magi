package harness

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPythonProposalBridge(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Fatal(e)
	}
	python, e = filepath.Abs(python)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	calls := []string{}
	result, e := Run(ctx, python, "Read the file.", 3, func(_ context.Context, f Frame) (json.RawMessage, error) {
		calls = append(calls, f.Operation)
		switch f.Operation {
		case "model-0":
			return json.RawMessage(`{"text":"{\"tool\":\"read_file\",\"arguments\":{\"path\":\"solution.py\"}}"}`), nil
		case "tool-0":
			return json.RawMessage(`{"outcome":"returned","text":"observed bytes","exit_code":0}`), nil
		case "model-1":
			if !strings.Contains(string(f.Payload), "observed bytes") {
				t.Fatal("missing context")
			}
			return json.RawMessage(`{"text":"{\"tool\":\"check\",\"arguments\":{}}"}`), nil
		case "tool-1":
			return json.RawMessage(`{"outcome":"returned","text":"checks passed","exit_code":0}`), nil
		case "model-2":
			return json.RawMessage(`{"text":"{\"answer\":\"Done.\"}"}`), nil
		}
		t.Fatal("unexpected proposal")
		return nil, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(result), "Done.") || len(calls) != 5 {
		t.Fatal(string(result), calls)
	}
}
