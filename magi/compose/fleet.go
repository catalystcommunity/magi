package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// ScopeOperate lets a caller make operator calls: runner and app releases.
const ScopeOperate = "magi:operate"

// A runner that has not said hello for this long is not connected.
const runnerGone = 3 * time.Minute

// fleet is what Magi knows about its runners. Runners say hello about once
// a minute, so the registry fills again soon after Magi restarts.
type fleet struct {
	mu      sync.Mutex
	runners map[string]magiapi.RunnerInfo
}

func (f *fleet) seen(info magiapi.RunnerInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runners == nil {
		f.runners = map[string]magiapi.RunnerInfo{}
	}
	f.runners[info.Name] = info
}

// connected returns the runners that said hello recently, by name.
func (f *fleet) connected() []magiapi.RunnerInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	cut := uint64(time.Now().Add(-runnerGone).UnixMilli())
	var out []magiapi.RunnerInfo
	for _, r := range f.runners {
		if r.SeenMs >= cut {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f *fleet) get(name string) (magiapi.RunnerInfo, bool) {
	for _, r := range f.connected() {
		if r.Name == name {
			return r, true
		}
	}
	return magiapi.RunnerInfo{}, false
}

// chooseRunners picks the runner that works on a change and the runner
// that publishes it. An explicit runner must be connected and have the
// repository.
func (s *System) chooseRunners(runner, repo string) (string, string, error) {
	names := func() string {
		var n []string
		for _, r := range s.fleet.connected() {
			n = append(n, r.Name)
		}
		if len(n) == 0 {
			return "none"
		}
		return strings.Join(n, ", ")
	}
	if runner == "" {
		runner = s.Cfg.Runner
	} else if info, ok := s.fleet.get(runner); !ok {
		return "", "", rpc.Invalid("runner %s is not connected (connected: %s)", runner, names())
	} else if !slices.Contains(info.Repos, repo) {
		return "", "", rpc.Invalid("runner %s has no repository %s", runner, repo)
	}
	if runner == "" {
		return "", "", nil
	}
	publisher := s.Cfg.Publisher
	if publisher == "" {
		if info, ok := s.fleet.get(runner); !ok || slices.Contains(info.Publishes, repo) {
			publisher = runner
		} else {
			for _, r := range s.fleet.connected() {
				if slices.Contains(r.Publishes, repo) {
					publisher = r.Name
					break
				}
			}
		}
	}
	if publisher == "" {
		return "", "", rpc.Invalid("no connected runner can publish %s (connected: %s)", repo, names())
	}
	return runner, publisher, nil
}

// isOperator reports whether the caller can make operator calls.
func (s *System) isOperator(ctx context.Context) error {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return err
	}
	if p.Has(ScopeOperate) || slices.Contains(s.Cfg.Admins, p.Subject) {
		return nil
	}
	return rpc.Denied("an operator call needs scope %s", ScopeOperate)
}

// runnerRelease is the manifest of a runner release. Packages maps
// "<os>/<arch>" to the package digest.
type runnerRelease struct {
	Version  string            `json:"version"`
	Packages map[string]string `json:"packages"`
}

// Artifact labels of fleet records. Magi owns these artifacts.
const (
	kindRunnerPackage = "runner-package"
	kindRunnerRelease = "runner-release"
	kindAppRelease    = "app-release"
)

func (s *System) putRunnerPackage(ctx context.Context, version, goos, arch string, body []byte) (string, error) {
	if version == "" || goos == "" || arch == "" || len(body) == 0 {
		return "", rpc.Invalid("version, os, arch, and body are required")
	}
	a, err := s.Artifacts.Put(s.AsService(ctx), artifactsapi.PutRequest{MediaType: "application/gzip", Body: body,
		Labels: map[string]string{"kind": kindRunnerPackage, "version": version, "os": goos, "arch": arch}})
	if err != nil {
		return "", err
	}
	return a.Digest, nil
}

// runnerPackages returns the package digests of version by "<os>/<arch>".
func (s *System) runnerPackages(ctx context.Context, version string) (map[string]string, error) {
	res, err := s.Artifacts.List(s.AsService(ctx), artifactsapi.ListRequest{LabelKey: ptr("version"), LabelValue: ptr(version), Limit: ptr(uint64(100))})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range res.Artifacts {
		if a.Labels["kind"] == kindRunnerPackage {
			key := a.Labels["os"] + "/" + a.Labels["arch"]
			if _, ok := out[key]; !ok { // newest first
				out[key] = a.Digest
			}
		}
	}
	return out, nil
}

// setRunnerRelease makes version the release that runners run.
func (s *System) setRunnerRelease(ctx context.Context, version string) (runnerRelease, error) {
	pk, err := s.runnerPackages(ctx, version)
	if err != nil {
		return runnerRelease{}, err
	}
	if len(pk) == 0 {
		return runnerRelease{}, rpc.Invalid("runner release %s has no packages", version)
	}
	rel := runnerRelease{Version: version, Packages: pk}
	b, _ := json.Marshal(rel)
	if _, err := s.Artifacts.Put(s.AsService(ctx), artifactsapi.PutRequest{MediaType: "application/json", Body: b,
		Labels: map[string]string{"kind": kindRunnerRelease, "version": version}}); err != nil {
		return runnerRelease{}, err
	}
	s.Log.Info("runner release set", "version", version, "packages", len(pk))
	return rel, nil
}

// runnerRelease returns the newest runner release, if one is set.
func (s *System) runnerRelease(ctx context.Context) (runnerRelease, bool, error) {
	sctx := s.AsService(ctx)
	res, err := s.Artifacts.List(sctx, artifactsapi.ListRequest{LabelKey: ptr("kind"), LabelValue: ptr(kindRunnerRelease), Limit: ptr(uint64(1))})
	if err != nil || len(res.Artifacts) == 0 {
		return runnerRelease{}, false, err
	}
	g, err := s.Artifacts.Get(sctx, artifactsapi.GetRequest{Digest: res.Artifacts[0].Digest})
	if err != nil {
		return runnerRelease{}, false, err
	}
	var rel runnerRelease
	if err := json.Unmarshal(g.Body, &rel); err != nil {
		return runnerRelease{}, false, fmt.Errorf("runner release manifest: %w", err)
	}
	return rel, true, nil
}

func (s *System) putAppRelease(ctx context.Context, req magiapi.PutAppReleaseRequest) (magiapi.AppRelease, error) {
	if req.VersionCode == 0 || len(req.Body) == 0 {
		return magiapi.AppRelease{}, rpc.Invalid("version_code and body are required")
	}
	if cur, ok, err := s.appRelease(ctx); err != nil {
		return magiapi.AppRelease{}, err
	} else if ok && cur.VersionCode >= req.VersionCode {
		return magiapi.AppRelease{}, rpc.Conflict("version_code %d is not newer than %d", req.VersionCode, cur.VersionCode)
	}
	labels := map[string]string{"kind": kindAppRelease, "version_code": strconv.FormatUint(req.VersionCode, 10), "version_name": req.VersionName}
	if t := deref(req.Task); t != "" {
		labels["task"] = t
	}
	a, err := s.Artifacts.Put(s.AsService(ctx), artifactsapi.PutRequest{MediaType: "application/vnd.android.package-archive", Body: req.Body, Labels: labels})
	if err != nil {
		return magiapi.AppRelease{}, err
	}
	s.Log.Info("app release", "version_code", req.VersionCode, "version_name", req.VersionName, "size", a.Size, "task", deref(req.Task))
	rel := magiapi.AppRelease{VersionCode: req.VersionCode, VersionName: req.VersionName, Digest: a.Digest, Size: a.Size}
	s.announceAppRelease(ctx, rel)
	return rel, nil
}

// announceAppRelease sends a notice to each phone that reported an older
// build. The notice wakes the phone, and the answer of wait-notices has the
// new build, so the app updates at once. A phone that never reported a
// build gets no notice: it can be a client that is not this app.
func (s *System) announceAppRelease(ctx context.Context, rel magiapi.AppRelease) {
	res, err := s.Conversation.ListDevices(s.AsService(ctx), conversationapi.ListDevicesRequest{All: ptr(true)})
	if err != nil {
		s.Log.Warn("app release: list devices", "err", err)
		return
	}
	for _, d := range res.Devices {
		if d.Kind != "phone" || deref(d.AppBuild) == 0 || deref(d.AppBuild) >= rel.VersionCode {
			continue
		}
		_, err := s.sendNotice(ctx, noticeReq{owner: d.Person, persona: Engineering, urgency: "low", device: d.Id,
			title: "App update",
			text:  fmt.Sprintf("A new build of the Magi app is ready (%s). Your phone %q installs it now. If it does not, open the app.", rel.VersionName, d.Name),
			key:   fmt.Sprintf("app-release:%d/%s/%s", rel.VersionCode, d.Person, d.Id)})
		if err != nil {
			s.Log.Warn("app release: notice", "device", d.Id, "err", err)
		}
	}
}

// appUpdateEvent tells what changed on a device for the newest app
// release: the device runs the build now ("App installed"), or it gives a
// new reason that the update did not install ("App not installed"). what
// is the end of the sentence "Task ... ...". The reason is text of a
// client, so it is cut and has no marks of the prompt.
func appUpdateEvent(before, after conversationapi.Device, rel magiapi.AppRelease) (report, what string) {
	switch {
	case deref(before.AppBuild) < rel.VersionCode && deref(after.AppBuild) >= rel.VersionCode:
		return "App installed", fmt.Sprintf("has its app build on the %s %q: it runs build %d (%s) now",
			after.Kind, neutralize(after.Name), deref(after.AppBuild), neutralize(deref(after.AppVersion)))
	case deref(after.UpdateError) != "" && deref(after.UpdateError) != deref(before.UpdateError) && deref(after.AppBuild) < rel.VersionCode:
		return "App not installed", fmt.Sprintf("has an app build that did not install on the %s %q. The phone gives this reason: %s",
			after.Kind, neutralize(after.Name), clip(neutralize(deref(after.UpdateError)), 300))
	}
	return "", ""
}

// appUpdated starts a turn of the persona of the task that made the newest
// app release, when a device of the owner of that task runs the build or
// fails to install it.
func (s *System) appUpdated(ctx context.Context, before, after conversationapi.Device) {
	rel, task, ok, err := s.appReleaseTask(ctx)
	if err != nil || !ok || task == "" {
		return
	}
	report, what := appUpdateEvent(before, after, rel)
	if report == "" {
		return
	}
	run, err := s.Workflow.GetRun(s.AsService(ctx), workflowapi.GetRunRequest{Id: task})
	if err != nil || run.Owner != after.Person {
		return
	}
	s.wakeFor(ctx, workflowapi.Assignment{Run: run.Id, Owner: run.Owner, Vars: run.Vars}, fmt.Sprintf("%s %d %s", report, rel.VersionCode, after.Id), what)
}

// appRelease returns the app release with the highest version code.
func (s *System) appRelease(ctx context.Context) (magiapi.AppRelease, bool, error) {
	rel, _, ok, err := s.appReleaseTask(ctx)
	return rel, ok, err
}

// appReleaseTask is appRelease with the task that made the release ("" for
// a release of an operator).
func (s *System) appReleaseTask(ctx context.Context) (magiapi.AppRelease, string, bool, error) {
	res, err := s.Artifacts.List(s.AsService(ctx), artifactsapi.ListRequest{LabelKey: ptr("kind"), LabelValue: ptr(kindAppRelease), Limit: ptr(uint64(20))})
	if err != nil {
		return magiapi.AppRelease{}, "", false, err
	}
	var best magiapi.AppRelease
	task := ""
	for _, a := range res.Artifacts {
		vc, _ := strconv.ParseUint(a.Labels["version_code"], 10, 64)
		if vc > best.VersionCode {
			best = magiapi.AppRelease{VersionCode: vc, VersionName: a.Labels["version_name"], Digest: a.Digest, Size: a.Size}
			task = a.Labels["task"]
		}
	}
	return best, task, best.VersionCode > 0, nil
}

// fleetArtifact returns the body of a fleet artifact of the given kind.
func (s *System) fleetArtifact(ctx context.Context, digest, kind string) ([]byte, error) {
	g, err := s.Artifacts.Get(s.AsService(ctx), artifactsapi.GetRequest{Digest: digest})
	if err != nil {
		return nil, err
	}
	if g.Artifact.Labels["kind"] != kind {
		return nil, rpc.NotFound("no %s %s", kind, digest)
	}
	return g.Body, nil
}

// MagiService operations.

// GetAccess tells a client what the caller can do. The answer is only for
// the interface: a client shows or hides links with it, and the server
// checks each call again.
func (m *Service) GetAccess(ctx context.Context, _ magiapi.GetAccessRequest) (magiapi.Access, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.Access{}, err
	}
	return magiapi.Access{Subject: p.Subject, Operator: m.S.isOperator(ctx) == nil}, nil
}

func (m *Service) ListRunners(ctx context.Context, _ magiapi.ListRunnersRequest) (magiapi.ListRunnersResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return magiapi.ListRunnersResponse{}, err
	}
	out := magiapi.ListRunnersResponse{Runners: m.S.fleet.connected()}
	if out.Runners == nil {
		out.Runners = []magiapi.RunnerInfo{}
	}
	if rel, ok, err := m.S.runnerRelease(ctx); err == nil && ok {
		out.Release = ptr(rel.Version)
	}
	return out, nil
}

func (m *Service) PutRunnerPackage(ctx context.Context, req magiapi.PutRunnerPackageRequest) (magiapi.PutRunnerPackageResponse, error) {
	if err := m.S.isOperator(ctx); err != nil {
		return magiapi.PutRunnerPackageResponse{}, err
	}
	d, err := m.S.putRunnerPackage(ctx, req.Version, req.Os, req.Arch, req.Body)
	return magiapi.PutRunnerPackageResponse{Digest: d}, err
}

func (m *Service) SetRunnerRelease(ctx context.Context, req magiapi.SetRunnerReleaseRequest) (magiapi.SetRunnerReleaseResponse, error) {
	if err := m.S.isOperator(ctx); err != nil {
		return magiapi.SetRunnerReleaseResponse{}, err
	}
	rel, err := m.S.setRunnerRelease(ctx, req.Version)
	if err != nil {
		return magiapi.SetRunnerReleaseResponse{}, err
	}
	out := magiapi.SetRunnerReleaseResponse{Version: rel.Version, Packages: []string{}}
	for k := range rel.Packages {
		out.Packages = append(out.Packages, k)
	}
	sort.Strings(out.Packages)
	return out, nil
}

func (m *Service) PutAppRelease(ctx context.Context, req magiapi.PutAppReleaseRequest) (magiapi.AppRelease, error) {
	if err := m.S.isOperator(ctx); err != nil {
		return magiapi.AppRelease{}, err
	}
	return m.S.putAppRelease(ctx, req)
}

func (m *Service) GetAppRelease(ctx context.Context, _ magiapi.GetAppReleaseRequest) (magiapi.GetAppReleaseResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return magiapi.GetAppReleaseResponse{}, err
	}
	rel, ok, err := m.S.appRelease(ctx)
	if err != nil || !ok {
		return magiapi.GetAppReleaseResponse{}, err
	}
	return magiapi.GetAppReleaseResponse{Release: &rel}, nil
}

func (m *Service) GetAppPackage(ctx context.Context, req magiapi.GetAppPackageRequest) (magiapi.GetAppPackageResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return magiapi.GetAppPackageResponse{}, err
	}
	b, err := m.S.fleetArtifact(ctx, req.Digest, kindAppRelease)
	return magiapi.GetAppPackageResponse{Body: b}, err
}
