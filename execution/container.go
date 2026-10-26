// Package execution contains operator-installed, bounded execution backends.
// This package does not authenticate callers or own workflow task state.
package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const PythonImage = "python@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a"
const Profile = "python-offline-demo-v1"
const PolicyRevision = "offline-solution-only-v1"

//go:embed tool.py
var toolSource string
var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)

type Proposal struct {
	Tool      string            `json:"tool"`
	Arguments map[string]string `json:"arguments"`
}
type Result struct {
	Outcome       string `json:"outcome"`
	Text          string `json:"text"`
	ExitCode      int    `json:"exit_code"`
	CheckedDigest string `json:"checked_digest,omitempty"`
}
type Container struct{ Name, ID string }
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("output limit")
	}
	return b.Buffer.Write(p)
}
func docker(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "docker", args...)
	c.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	c.Stdin = bytes.NewReader(input)
	c.Stderr = &boundedBuffer{limit: 4096}
	out := &boundedBuffer{limit: 65536}
	c.Stdout = out
	c.WaitDelay = time.Second
	e := c.Run()
	if e != nil {
		return nil, errors.New("container runtime operation failed")
	}
	return out.Bytes(), nil
}
func ContainerName(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", errors.New("invalid environment ID")
	}
	h := sha256.Sum256([]byte(id))
	return "opal-task-" + hex.EncodeToString(h[:16]), nil
}

// Create never mounts a host path, publishes a port, or injects a credential.
// The fixed supervisor exits after 15 minutes even if the coordinator is lost.
func Create(ctx context.Context, id string) (Container, error) {
	var out Container
	name, e := ContainerName(id)
	if e != nil {
		return out, e
	}
	args := []string{"create", "--pull=never", "--name", name, "--label", "opal.execution=" + id, "--label", "opal.profile=" + Profile, "--network=none", "--read-only", "--user=65532:65532", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--memory=256m", "--memory-swap=256m", "--cpus=1", "--pids-limit=64", "--ulimit", "nofile=128:128", "--ulimit", "fsize=16777216:16777216", "--log-driver=none", "--tmpfs", "/workspace:rw,nosuid,nodev,noexec,size=64m,uid=65532,gid=65532,mode=0700", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16m,uid=65532,gid=65532,mode=0700", "--workdir=/workspace", "--env", "PYTHONDONTWRITEBYTECODE=1", "--entrypoint=python", PythonImage, "-I", "-B", "-c", "import time; time.sleep(900)"}
	raw, e := docker(ctx, nil, args...)
	if e != nil {
		return out, e
	}
	containerID := strings.TrimSpace(string(raw))
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(containerID) {
		return out, errors.New("invalid runtime receipt")
	}
	out = Container{Name: name, ID: containerID}
	if _, e = docker(ctx, nil, "start", containerID); e != nil {
		return out, e
	}
	return out, nil
}
func (c Container) Verify(ctx context.Context, id string) error {
	name, e := ContainerName(id)
	if e != nil || name != c.Name || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.ID) {
		return errors.New("environment binding mismatch")
	}
	raw, e := docker(ctx, nil, "inspect", "--format", `{{json .}}`, c.ID)
	if e != nil {
		return e
	}
	var v struct {
		ID     string `json:"Id"`
		Name   string
		Config struct {
			Image  string
			User   string
			Labels map[string]string
		}
		HostConfig struct {
			NetworkMode    string
			ReadonlyRootfs bool
			Privileged     bool
			Memory         int64
			NanoCpus       int64
			PidsLimit      int64
			CapDrop        []string
			SecurityOpt    []string
			Binds          []string
			LogConfig      struct{ Type string }
		}
		Mounts []struct{ Type string }
	}
	if json.Unmarshal(raw, &v) != nil || v.ID != c.ID || v.Name != "/"+name || v.Config.Image != PythonImage || v.Config.User != "65532:65532" || v.Config.Labels["opal.execution"] != id || v.Config.Labels["opal.profile"] != Profile || v.HostConfig.NetworkMode != "none" || !v.HostConfig.ReadonlyRootfs || v.HostConfig.Privileged || v.HostConfig.Memory != 256*1024*1024 || v.HostConfig.NanoCpus != 1000000000 || v.HostConfig.PidsLimit != 64 || len(v.HostConfig.Binds) != 0 || v.HostConfig.LogConfig.Type != "none" || len(v.HostConfig.CapDrop) != 1 || strings.ToLower(v.HostConfig.CapDrop[0]) != "all" || len(v.HostConfig.SecurityOpt) != 1 || v.HostConfig.SecurityOpt[0] != "no-new-privileges:true" {
		return errors.New("environment controls differ from admission")
	}
	for _, m := range v.Mounts {
		if m.Type != "tmpfs" {
			return errors.New("unexpected environment mount")
		}
	}
	return nil
}

// Find identifies an admitted container after a lost create or bind receipt.
// It never creates, starts, or repeats a task. Empty means confirmed absence.
func Find(ctx context.Context, id string) (Container, error) {
	name, e := ContainerName(id)
	if e != nil {
		return Container{}, e
	}
	raw, e := docker(ctx, nil, "container", "ls", "--all", "--no-trunc", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	if e != nil {
		return Container{}, e
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return Container{}, nil
	}
	c := Container{Name: name, ID: value}
	if e = c.Verify(ctx, id); e != nil {
		return Container{}, e
	}
	return c, nil
}

// Evaluate is outside the proposing process and uses the installed profile.
// It is a deterministic policy check, not a claim of semantic model safety.
func Evaluate(p Proposal) error {
	switch p.Tool {
	case "read_file":
		if len(p.Arguments) != 1 || p.Arguments["path"] != "solution.py" {
			return errors.New("path denied")
		}
	case "write_file":
		if len(p.Arguments) != 2 || p.Arguments["path"] != "solution.py" || len(p.Arguments["text"]) == 0 || len(p.Arguments["text"]) > 16384 {
			return errors.New("write denied")
		}
	case "check":
		if len(p.Arguments) != 0 {
			return errors.New("check arguments denied")
		}
	default:
		return errors.New("tool denied")
	}
	return nil
}
func (c Container) Call(ctx context.Context, id string, p Proposal) (Result, error) {
	if e := Evaluate(p); e != nil {
		return Result{Outcome: "denied", Text: e.Error()}, nil
	}
	if e := c.Verify(ctx, id); e != nil {
		return Result{Outcome: "unknown"}, e
	}
	raw, _ := json.Marshal(p)
	bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, e := docker(bounded, raw, "exec", "-i", c.ID, "python", "-I", "-B", "-c", toolSource)
	if e != nil {
		return Result{Outcome: "unknown", Text: "The command may have run. Do not repeat it without investigation."}, e
	}
	var result Result
	if json.Unmarshal(out, &result) != nil || result.Outcome != "returned" || len(result.Text) > 32768 {
		return Result{Outcome: "unknown"}, errors.New("invalid command result")
	}
	return result, nil
}

// Destroy is only for this exact, verified container. Evidence belongs elsewhere.
func (c Container) Destroy(ctx context.Context, id string) error {
	if e := c.Verify(ctx, id); e != nil {
		// A confirmed absence after a lost remove reply is safe to reconcile.
		// An unavailable daemon or a changed binding is not absence.
		name, valid := ContainerName(id)
		if valid == nil && name == c.Name && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.ID) {
			raw, check := docker(ctx, nil, "container", "ls", "--all", "--filter", "id="+c.ID, "--format", "{{.ID}}")
			if check == nil && strings.TrimSpace(string(raw)) == "" {
				return nil
			}
		}
		return e
	}
	_, e := docker(ctx, nil, "rm", "-f", c.ID)
	return e
}
func (c Container) ShellHint() string {
	return fmt.Sprintf("docker exec -it --user 65532:65532 %s /bin/sh", c.ID)
}
