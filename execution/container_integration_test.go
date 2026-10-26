//go:build integration

package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIsolatedContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var random [8]byte
	rand.Read(random[:])
	id := "fixture-" + hex.EncodeToString(random[:])
	c, e := Create(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if e := c.Destroy(cleanup, id); e != nil {
			t.Error(e)
		}
	}()
	if e = c.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	source := "def format_elapsed(ms):\n if isinstance(ms,bool) or not isinstance(ms,int): raise TypeError()\n if ms<0: raise ValueError()\n return f'{ms} ms' if ms<1000 else f'{ms/1000:.2f} s'\n"
	result, e := c.Call(ctx, id, Proposal{Tool: "write_file", Arguments: map[string]string{"path": "solution.py", "text": source}})
	if e != nil || result.ExitCode != 0 {
		t.Fatal(e, result)
	}
	result, e = c.Call(ctx, id, Proposal{Tool: "check", Arguments: map[string]string{}})
	hash := sha256.Sum256([]byte(source))
	if e != nil || result.ExitCode != 0 || result.CheckedDigest != hex.EncodeToString(hash[:]) || !strings.Contains(result.Text, "8 formatting checks passed") {
		t.Fatal(e, result)
	}
	result, e = c.Call(ctx, id, Proposal{Tool: "read_file", Arguments: map[string]string{"path": "solution.py"}})
	if e != nil || result.Text != source {
		t.Fatal("artifact differs", e)
	}
	if c.Verify(ctx, id+"-other") == nil {
		t.Fatal("wrong environment binding")
	}
	// A candidate can run Python, but it must not acquire host resources or egress.
	hostHome, homeError := os.UserHomeDir()
	if homeError != nil {
		t.Fatal(homeError)
	}
	quotedHome, _ := json.Marshal(hostHome)
	attack := "import os,socket\nassert not os.path.exists('/var/run/docker.sock')\nassert not os.path.exists(" + string(quotedHome) + ")\ntry:\n open('/escape','w').write('x')\n raise AssertionError('root filesystem writable')\nexcept OSError: pass\ns=socket.socket();s.settimeout(0.2)\ntry:\n s.connect(('1.1.1.1',443))\n raise AssertionError('network reachable')\nexcept OSError: pass\n" + source
	_, e = c.Call(ctx, id, Proposal{Tool: "write_file", Arguments: map[string]string{"path": "solution.py", "text": attack}})
	if e != nil {
		t.Fatal(e)
	}
	result, e = c.Call(ctx, id, Proposal{Tool: "check", Arguments: map[string]string{}})
	if e != nil || result.ExitCode != 0 {
		t.Fatal("controls failed", e, result)
	}
}
