package execution

import (
	"strings"
	"testing"
)

func TestToolPolicy(t *testing.T) {
	for _, p := range []Proposal{{Tool: "read_file", Arguments: map[string]string{"path": "solution.py"}}, {Tool: "write_file", Arguments: map[string]string{"path": "solution.py", "text": "pass"}}, {Tool: "check", Arguments: map[string]string{}}} {
		if e := Evaluate(p); e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range []Proposal{{Tool: "shell", Arguments: map[string]string{}}, {Tool: "read_file", Arguments: map[string]string{"path": "/etc/passwd"}}, {Tool: "write_file", Arguments: map[string]string{"path": "solution.py", "text": strings.Repeat("a", 16385)}}, {Tool: "check", Arguments: map[string]string{"command": "anything"}}} {
		if Evaluate(p) == nil {
			t.Fatal("unauthorized tool", p.Tool)
		}
	}
	if _, e := ContainerName("../host"); e == nil {
		t.Fatal("invalid name accepted")
	}
}
