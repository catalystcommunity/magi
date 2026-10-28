package compose

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	releaseapi "github.com/catalystcommunity/magi/primitives/release/goapi"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// ReleaseConfig selects where Magi pushes its own images and which release
// target receives them. An empty Registry turns the release step off.
type ReleaseConfig struct {
	Registry     string // push address, such as registry.magi.svc:5000
	PullRegistry string // address in image references, such as localhost:30500
	Username     string
	Password     string
	BaseImage    string
	Target       string // release target name of the magi Deployment
}

// release builds the merged magi-server in the task environment, pushes an
// image, and asks the release controller to roll it out. The environment
// gets no registry credential: Magi reads the binary and pushes it itself.
//
// The rollout replaces the process that runs this step. The step is safe to
// resume: a second attempt finds the release by its reason and waits for it.
func (s *System) release(ctx context.Context, a workflowapi.Assignment, note func(string)) (Result, error) {
	rc := s.ReleaseCfg
	if rc.Registry == "" || s.Release == nil {
		return Result{Outcome: "skipped"}, nil
	}
	reason := "run:" + a.Run
	if rel, ok := s.findRelease(ctx, reason); ok {
		note("Resuming release " + rel.Id)
		return s.waitRelease(ctx, a, rel.Id, note)
	}
	envID, workdir := a.Vars["environment"], a.Vars["workdir"]
	note("Building magi-server from the merged change.")
	r, err := s.exec(ctx, envID, workdir, `set -e
mkdir -p /work/out
cd magi && GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /work/out/app ./cmd/magi-server`, 15*60000)
	if err != nil {
		return Result{}, err
	}
	if r.ExitCode != 0 {
		s.post(ctx, a, "The release build failed. Nothing was released.")
		return Result{Outcome: "build-failed", Outputs: map[string]string{"release.output": tail(string(r.Stdout)+string(r.Stderr), 4000)}}, nil
	}
	bin, err := s.Execution.ReadFile(ctx, executionapi.ReadFileRequest{Environment: envID, Path: "/work/out/app", MaxBytes: ptr(uint64(64 << 20))})
	if err != nil || bin.Truncated {
		return Result{}, fmt.Errorf("read binary: %v (truncated %v)", err, bin.Truncated)
	}
	tag := fmt.Sprintf("run-%s-%d", strings.TrimPrefix(a.Run, "run_"), time.Now().Unix())
	digest, err := pushImage(ctx, rc, "magi/magi", tag, bin.Body)
	if err != nil {
		return Result{}, fmt.Errorf("push image: %w", err)
	}
	image := fmt.Sprintf("%s/magi/magi:%s@%s", rc.PullRegistry, tag, digest)
	note("Releasing " + image)
	rel, err := s.Release.Create(ctx, releaseapi.CreateReleaseRequest{
		Images: []releaseapi.TargetImage{{Target: rc.Target, Image: image}}, Reason: ptr(reason),
	})
	if err != nil {
		return Result{}, fmt.Errorf("create release: %w", err)
	}
	s.post(ctx, a, fmt.Sprintf("I built the merged change and started release %s. If the new version fails its health check, the controller rolls it back.", rel.Id))
	return s.waitRelease(ctx, a, rel.Id, note)
}

func (s *System) findRelease(ctx context.Context, reason string) (releaseapi.Release, bool) {
	res, err := s.Release.List(ctx, releaseapi.ListReleasesRequest{Limit: ptr(uint64(50))})
	if err != nil {
		return releaseapi.Release{}, false
	}
	for _, r := range res.Releases {
		if deref(r.Reason) == reason {
			return r, true
		}
	}
	return releaseapi.Release{}, false
}

func (s *System) waitRelease(ctx context.Context, a workflowapi.Assignment, id string, note func(string)) (Result, error) {
	for {
		rel, err := s.Release.Get(ctx, releaseapi.GetReleaseRequest{Id: id})
		if err != nil {
			return Result{}, err
		}
		out := map[string]string{"release.id": id, "release.status": rel.Status}
		switch rel.Status {
		case "succeeded":
			s.post(ctx, a, fmt.Sprintf("Release %s succeeded. The new version is running.", id))
			out["task.result"] = "released"
			return Result{Outcome: "done", Outputs: out}, nil
		case "rolled-back", "failed":
			msg := ""
			for _, it := range rel.Items {
				msg += deref(it.Message) + " "
			}
			s.post(ctx, a, fmt.Sprintf("Release %s did not pass and is %s: %s", id, rel.Status, strings.TrimSpace(msg)))
			out["task.result"] = "release-" + rel.Status
			return Result{Outcome: "rolled-back", Outputs: out}, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// pushImage writes base + one layer with /app as the entrypoint.
func pushImage(ctx context.Context, rc ReleaseConfig, repo, tag string, binary []byte) (string, error) {
	baseRef, err := name.ParseReference(rc.BaseImage)
	if err != nil {
		return "", err
	}
	base, err := remote.Image(baseRef, remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"}))
	if err != nil {
		return "", fmt.Errorf("base image: %w", err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "app", Mode: 0o755, Size: int64(len(binary)), Uid: 0, Gid: 0, ModTime: time.Unix(0, 0)})
	tw.Write(binary)
	tw.Close()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil })
	if err != nil {
		return "", err
	}
	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		return "", err
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return "", err
	}
	cfg = cfg.DeepCopy()
	cfg.Config.Entrypoint, cfg.Config.Cmd = []string{"/app"}, nil
	if img, err = mutate.ConfigFile(img, cfg); err != nil {
		return "", err
	}
	ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s", rc.Registry, repo, tag), name.Insecure)
	if err != nil {
		return "", err
	}
	auth := remote.WithAuth(&authn.Basic{Username: rc.Username, Password: rc.Password})
	if err := remote.Write(ref, img, auth, remote.WithContext(ctx)); err != nil {
		return "", err
	}
	d, err := img.Digest()
	return d.String(), err
}
