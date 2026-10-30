// Package server implements SourceService and Git smart HTTP.
//
// Repositories are bare Git repositories in Dir, named <name>.git. Each has
// a magi.json file with owner, default branch, and creation time.
//
// Access: the owner, a caller with source:act-as, and a caller with
// source:admin can read and change a repository. Other callers get "not
// found".
package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/source/api"
)

const (
	ScopeActAs = "source:act-as"
	ScopeAdmin = "source:admin"

	// MaxRequest limits RPC request bodies, which can hold a seed bundle.
	MaxRequest = 256 << 20

	defaultDiffBytes = 1 << 20
	maxDiffBytes     = 16 << 20
)

// Service implements api.SourceService.
type Service struct {
	Dir string
	Now func() time.Time
	// AllowFileUpstream permits file:// upstream URLs. Only tests set it:
	// a file URL would let an admin read any repository on the host.
	AllowFileUpstream bool

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var _ api.SourceService = (*Service)(nil)

// Register adds the service to s.
func Register(s *rpc.Server, impl *Service) {
	s.Register("SourceService", api.SourceServiceOps(impl))
}

// Handler serves RPC at /rpc/, Git smart HTTP at /git/, and /healthz.
func Handler(s *rpc.Server, impl *Service) http.Handler {
	Register(s, impl)
	m := rpc.Mux(s, func() error { _, err := os.Stat(impl.Dir); return err })
	m.Handle("/git/", impl.GitHTTP(s.Verifier))
	return m
}

// lock serializes ref changes in one repository.
func (s *Service) lock(name string) func() {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	l := s.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[name] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (s *Service) now() uint64 {
	if s.Now != nil {
		return uint64(s.Now().UnixMilli())
	}
	return uint64(time.Now().UnixMilli())
}

func canAccess(p authn.Principal, owner string) bool {
	return owner == p.Subject || p.Has(ScopeActAs) || p.Has(ScopeAdmin)
}

// open returns the metadata of a repository that the caller can access.
func (s *Service) open(ctx context.Context, name string) (authn.Principal, meta, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return p, meta{}, err
	}
	m, err := s.readMeta(name)
	if err == nil && !canAccess(p, m.Owner) {
		err = rpc.NotFound("repository %s not found", name)
	}
	return p, m, err
}

func repository(name string, m meta) api.Repository {
	return api.Repository{Name: name, Owner: m.Owner, DefaultBranch: m.DefaultBranch, ClonePath: "/git/" + name + ".git", CreatedMs: m.CreatedMs}
}

func (s *Service) CreateRepo(ctx context.Context, req api.CreateRepoRequest) (api.Repository, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.Repository{}, err
	}
	owner := p.Subject
	if req.Owner != nil && *req.Owner != "" && *req.Owner != p.Subject {
		if !p.Has(ScopeActAs) && !p.Has(ScopeAdmin) {
			return api.Repository{}, rpc.Denied("acting for another owner needs %s", ScopeActAs)
		}
		owner = *req.Owner
	}
	if err := validName(req.Name); err != nil {
		return api.Repository{}, err
	}
	branch := "main"
	if req.DefaultBranch != nil && *req.DefaultBranch != "" {
		branch = *req.DefaultBranch
	}
	if err := validBranch(ctx, branch); err != nil {
		return api.Repository{}, err
	}
	upstream := ""
	if req.Upstream != nil && *req.Upstream != "" {
		if !p.Has(ScopeAdmin) {
			return api.Repository{}, rpc.Denied("upstream needs %s", ScopeAdmin)
		}
		if !s.validUpstream(*req.Upstream) {
			return api.Repository{}, rpc.Invalid("upstream must be an ssh://, https://, or user@host:path Git URL")
		}
		upstream = *req.Upstream
	}
	if _, err := os.Stat(s.repoDir(req.Name)); err == nil {
		return api.Repository{}, rpc.Conflict("repository %s exists", req.Name)
	}
	// Build the repository in a temporary directory, then rename it into
	// place. A rename onto an existing repository fails.
	tmp, err := os.MkdirTemp(s.Dir, ".create-")
	if err != nil {
		return api.Repository{}, err
	}
	defer os.RemoveAll(tmp)
	if _, err := git(ctx, tmp, nil, nil, nil, "init", "--quiet", "--bare", "--initial-branch="+branch, tmp); err != nil {
		return api.Repository{}, err
	}
	if req.SeedBundle != nil && len(*req.SeedBundle) > 0 {
		bundle := filepath.Join(tmp, "seed.bundle")
		if err := os.WriteFile(bundle, *req.SeedBundle, 0o600); err != nil {
			return api.Repository{}, err
		}
		if _, err := git(ctx, tmp, nil, nil, nil, "bundle", "verify", "--quiet", bundle); err != nil {
			return api.Repository{}, rpc.Invalid("seed_bundle is not a valid bundle")
		}
		if _, err := git(ctx, tmp, nil, nil, nil, "fetch", "--quiet", "--no-tags", bundle, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
			return api.Repository{}, rpc.Invalid("seed_bundle import failed: %v", err)
		}
		os.Remove(bundle)
	} else if upstream != "" {
		if _, err := git(ctx, tmp, upstreamEnv, nil, nil, "fetch", "--quiet", "--no-tags", upstream, "refs/heads/"+branch+":refs/heads/"+branch); err != nil {
			return api.Repository{}, rpc.Unavailable("fetch from upstream failed: %v", err)
		}
	}
	m := meta{Owner: owner, DefaultBranch: branch, CreatedMs: s.now(), Upstream: upstream}
	if err := writeMeta(tmp, m); err != nil {
		return api.Repository{}, err
	}
	if err := os.Rename(tmp, s.repoDir(req.Name)); err != nil {
		if errors.Is(err, os.ErrExist) || strings.Contains(err.Error(), "not empty") {
			return api.Repository{}, rpc.Conflict("repository %s exists", req.Name)
		}
		return api.Repository{}, err
	}
	return repository(req.Name, m), nil
}

func (s *Service) GetRepo(ctx context.Context, req api.GetRepoRequest) (api.Repository, error) {
	_, m, err := s.open(ctx, req.Name)
	if err != nil {
		return api.Repository{}, err
	}
	return repository(req.Name, m), nil
}

func (s *Service) ListRepos(ctx context.Context, req api.ListReposRequest) (api.ListReposResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListReposResponse{}, err
	}
	owner, all := p.Subject, p.Has(ScopeAdmin)
	if req.Owner != nil && *req.Owner != "" {
		if *req.Owner != p.Subject && !p.Has(ScopeActAs) && !p.Has(ScopeAdmin) {
			return api.ListReposResponse{}, rpc.Denied("listing another owner needs %s", ScopeActAs)
		}
		owner, all = *req.Owner, false
	}
	limit := 1000
	if req.Limit != nil && *req.Limit > 0 && *req.Limit < uint64(limit) {
		limit = int(*req.Limit)
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return api.ListReposResponse{}, err
	}
	out := []api.Repository{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".git")
		if !ok || !e.IsDir() || len(out) >= limit {
			continue
		}
		m, err := s.readMeta(name)
		if err != nil || (!all && m.Owner != owner) {
			continue
		}
		out = append(out, repository(name, m))
	}
	return api.ListReposResponse{Repositories: out}, nil
}

func (s *Service) ListRefs(ctx context.Context, req api.ListRefsRequest) (api.ListRefsResponse, error) {
	if _, _, err := s.open(ctx, req.Repo); err != nil {
		return api.ListRefsResponse{}, err
	}
	out, err := git(ctx, s.repoDir(req.Repo), nil, nil, nil, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return api.ListRefsResponse{}, err
	}
	refs := []api.Ref{}
	for line := range strings.Lines(string(out)) {
		if name, commit, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			refs = append(refs, api.Ref{Name: name, Commit: commit})
		}
	}
	return api.ListRefsResponse{Refs: refs}, nil
}

// Diff shows the changes on head since it left base. It is the three-dot
// form "git diff base...head": the merge base of base and head is compared
// with head. base_commit is the commit that base names.
func (s *Service) Diff(ctx context.Context, req api.DiffRequest) (api.DiffResult, error) {
	if _, _, err := s.open(ctx, req.Repo); err != nil {
		return api.DiffResult{}, err
	}
	dir := s.repoDir(req.Repo)
	base, err := resolve(ctx, dir, req.Base)
	if err != nil {
		return api.DiffResult{}, err
	}
	head, err := resolve(ctx, dir, req.Head)
	if err != nil {
		return api.DiffResult{}, err
	}
	rng := base + "..." + head
	opts := []string{"diff", "--no-color", "--no-ext-diff", "--no-renames", "--no-textconv"}
	num, err := git(ctx, dir, nil, nil, nil, append(opts, "--numstat", "-z", rng)...)
	if err != nil {
		return api.DiffResult{}, err
	}
	files := []api.FileChange{}
	for rec := range strings.SplitSeq(string(num), "\x00") {
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		a, _ := strconv.ParseUint(parts[0], 10, 64) // "-" for binary files
		d, _ := strconv.ParseUint(parts[1], 10, 64)
		files = append(files, api.FileChange{Path: parts[2], Additions: a, Deletions: d})
	}
	max := uint64(defaultDiffBytes)
	if req.MaxBytes != nil && *req.MaxBytes > 0 {
		max = min(*req.MaxBytes, maxDiffBytes)
	}
	var patch bytes.Buffer
	lw := &limited{w: &patch, n: int(max)}
	if _, err := git(ctx, dir, nil, nil, lw, append(opts, rng)...); err != nil {
		return api.DiffResult{}, err
	}
	return api.DiffResult{BaseCommit: base, HeadCommit: head, Patch: patch.String(), Files: files, Truncated: lw.truncated}, nil
}

// Merge merges source into the target branch without a work tree. It uses
// "git merge-tree --write-tree" and moves the target ref with a
// compare-and-swap update.
func (s *Service) Merge(ctx context.Context, req api.MergeRequest) (api.MergeResult, error) {
	p, _, err := s.open(ctx, req.Repo)
	if err != nil {
		return api.MergeResult{}, err
	}
	if err := validBranch(ctx, req.Target); err != nil {
		return api.MergeResult{}, err
	}
	defer s.lock(req.Repo)()
	dir := s.repoDir(req.Repo)
	ref := "refs/heads/" + req.Target
	target, err := resolve(ctx, dir, ref)
	if err != nil {
		return api.MergeResult{}, rpc.NotFound("target branch %s not found", req.Target)
	}
	source, err := resolve(ctx, dir, req.Source)
	if err != nil {
		return api.MergeResult{}, err
	}
	msg := func(s string) *string { return &s }
	if ok, err := isAncestor(ctx, dir, source, target); err != nil || ok {
		return api.MergeResult{Merged: ok, Commit: target, Message: msg("already up to date")}, err
	}
	ff, err := isAncestor(ctx, dir, target, source)
	if err != nil {
		return api.MergeResult{}, err
	}
	commit, note := source, "fast-forward"
	if !ff {
		if req.FastForwardOnly != nil && *req.FastForwardOnly {
			return api.MergeResult{Merged: false, Commit: target, Message: msg("not a fast-forward")}, nil
		}
		out, err := git(ctx, dir, nil, nil, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", target, source)
		if exitCode(err) == 1 {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			return api.MergeResult{Merged: false, Commit: target, Message: msg("conflict: " + strings.Join(slices.Compact(lines[1:]), ", "))}, nil
		}
		if err != nil {
			return api.MergeResult{}, err
		}
		tree := strings.TrimSpace(string(out))
		text := fmt.Sprintf("Merge %s into %s", req.Source, req.Target)
		if req.Message != nil && strings.TrimSpace(*req.Message) != "" {
			text = *req.Message
		}
		env := []string{"GIT_AUTHOR_NAME=" + p.Subject, "GIT_AUTHOR_EMAIL=" + p.Subject,
			"GIT_COMMITTER_NAME=Magi source", "GIT_COMMITTER_EMAIL=source@magi.invalid"}
		out, err = git(ctx, dir, env, strings.NewReader(text), nil, "commit-tree", tree, "-p", target, "-p", source)
		if err != nil {
			return api.MergeResult{}, err
		}
		commit, note = strings.TrimSpace(string(out)), "merged"
	}
	if _, err := git(ctx, dir, nil, nil, nil, "update-ref", ref, commit, target); err != nil {
		return api.MergeResult{}, rpc.Conflict("target branch %s moved during merge", req.Target)
	}
	return api.MergeResult{Merged: true, Commit: commit, Message: msg(note)}, nil
}

func (s *Service) DeleteBranch(ctx context.Context, req api.DeleteBranchRequest) (api.DeleteBranchResponse, error) {
	_, m, err := s.open(ctx, req.Repo)
	if err != nil {
		return api.DeleteBranchResponse{}, err
	}
	if err := validBranch(ctx, req.Branch); err != nil {
		return api.DeleteBranchResponse{}, err
	}
	if req.Branch == m.DefaultBranch {
		return api.DeleteBranchResponse{}, rpc.Invalid("cannot delete the default branch")
	}
	defer s.lock(req.Repo)()
	dir, ref := s.repoDir(req.Repo), "refs/heads/"+req.Branch
	if _, err := resolve(ctx, dir, ref); err != nil {
		return api.DeleteBranchResponse{Deleted: false}, nil
	}
	if _, err := git(ctx, dir, nil, nil, nil, "update-ref", "-d", ref); err != nil {
		return api.DeleteBranchResponse{}, err
	}
	return api.DeleteBranchResponse{Deleted: true}, nil
}

// Bundle returns a bundle of the named branches, or of all refs.
func (s *Service) Bundle(ctx context.Context, req api.BundleRequest) (api.BundleResponse, error) {
	if _, _, err := s.open(ctx, req.Repo); err != nil {
		return api.BundleResponse{}, err
	}
	dir := s.repoDir(req.Repo)
	revs := []string{"--all"}
	if len(req.Refs) > 0 {
		revs = revs[:0]
		for _, b := range req.Refs {
			if err := validBranch(ctx, b); err != nil {
				return api.BundleResponse{}, err
			}
			if _, err := resolve(ctx, dir, "refs/heads/"+b); err != nil {
				return api.BundleResponse{}, rpc.NotFound("branch %s not found", b)
			}
			revs = append(revs, "refs/heads/"+b)
		}
	} else if refs, err := git(ctx, dir, nil, nil, nil, "for-each-ref", "--count=1"); err != nil || len(refs) == 0 {
		if err != nil {
			return api.BundleResponse{}, err
		}
		return api.BundleResponse{}, rpc.Conflict("repository %s has no refs", req.Repo)
	}
	out, err := git(ctx, dir, nil, nil, nil, append([]string{"bundle", "create", "--quiet", "-"}, revs...)...)
	if err != nil {
		return api.BundleResponse{}, err
	}
	return api.BundleResponse{Bundle: out}, nil
}

// PushBundle imports the refs/heads/ refs of a bundle. The objects are
// fetched into a private ref namespace first. Then all branch updates are
// checked and applied in one "git update-ref --stdin" transaction. Creating
// the default branch in a repository that has none is allowed.
func (s *Service) PushBundle(ctx context.Context, req api.PushBundleRequest) (api.PushBundleResponse, error) {
	_, m, err := s.open(ctx, req.Repo)
	if err != nil {
		return api.PushBundleResponse{}, err
	}
	force := req.Force != nil && *req.Force
	dir := s.repoDir(req.Repo)
	f, err := os.CreateTemp(s.Dir, ".push-*.bundle")
	if err != nil {
		return api.PushBundleResponse{}, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(req.Bundle)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return api.PushBundleResponse{}, err
	}
	if _, err := git(ctx, dir, nil, nil, nil, "bundle", "verify", "--quiet", f.Name()); err != nil {
		return api.PushBundleResponse{}, rpc.Invalid("bundle is not valid for this repository")
	}
	heads, err := git(ctx, dir, nil, nil, nil, "bundle", "list-heads", f.Name())
	if err != nil {
		return api.PushBundleResponse{}, rpc.Invalid("bundle is not valid: %v", err)
	}
	type update struct{ branch, commit string }
	var ups []update
	for line := range strings.Lines(string(heads)) {
		commit, ref, _ := strings.Cut(strings.TrimSpace(line), " ")
		branch, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok {
			continue
		}
		if err := validBranch(ctx, branch); err != nil {
			return api.PushBundleResponse{}, err
		}
		ups = append(ups, update{branch, commit})
	}
	if len(ups) == 0 {
		return api.PushBundleResponse{}, rpc.Invalid("bundle has no refs under refs/heads/")
	}

	defer s.lock(req.Repo)()
	ns := "refs/magi-import/" + strconv.FormatInt(time.Now().UnixNano(), 36)
	defer func() {
		out, _ := git(context.WithoutCancel(ctx), dir, nil, nil, nil, "for-each-ref", "--format=delete %(refname)", ns+"/")
		if len(out) > 0 {
			git(context.WithoutCancel(ctx), dir, nil, bytes.NewReader(out), nil, "update-ref", "--stdin")
		}
	}()
	if _, err := git(ctx, dir, nil, nil, nil, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", f.Name(), "+refs/heads/*:"+ns+"/*"); err != nil {
		return api.PushBundleResponse{}, rpc.Invalid("bundle import failed: %v", err)
	}
	var tx strings.Builder
	updated := []api.Ref{}
	for _, u := range ups {
		ref := "refs/heads/" + u.branch
		cur, err := resolve(ctx, dir, ref)
		switch {
		case rpc.CodeOf(err) == 404:
			fmt.Fprintf(&tx, "create %s %s\n", ref, u.commit)
		case err != nil:
			return api.PushBundleResponse{}, err
		case cur == u.commit:
			continue
		default:
			if !force && u.branch == m.DefaultBranch {
				return api.PushBundleResponse{}, rpc.Conflict("update of default branch %s needs force", u.branch)
			}
			if ok, err := isAncestor(ctx, dir, cur, u.commit); err != nil {
				return api.PushBundleResponse{}, err
			} else if !ok && !force {
				return api.PushBundleResponse{}, rpc.Conflict("update of %s is not a fast-forward", u.branch)
			}
			fmt.Fprintf(&tx, "update %s %s %s\n", ref, u.commit, cur)
		}
		updated = append(updated, api.Ref{Name: ref, Commit: u.commit})
	}
	if tx.Len() > 0 {
		if _, err := git(ctx, dir, nil, strings.NewReader(tx.String()), nil, "update-ref", "--stdin"); err != nil {
			return api.PushBundleResponse{}, rpc.Conflict("refs moved during import: %v", err)
		}
	}
	return api.PushBundleResponse{Updated: updated}, nil
}

// upstreamEnv makes Git fail instead of asking for a password or a host key.
var upstreamEnv = []string{"GIT_SSH_COMMAND=ssh -o BatchMode=yes"}

var scpRE = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$`)

func (s *Service) validUpstream(u string) bool {
	switch {
	case strings.HasPrefix(u, "ssh://"), strings.HasPrefix(u, "https://"):
		return !strings.ContainsAny(u, " \t\n") && !strings.Contains(u, "::")
	case s.AllowFileUpstream && strings.HasPrefix(u, "file://"):
		return true
	}
	return scpRE.MatchString(u)
}

// fetchUpstream fetches the upstream default branch to refs/upstream/<branch>
// and returns the local and upstream commits.
func (s *Service) fetchUpstream(ctx context.Context, name string, m meta) (local, up string, err error) {
	if m.Upstream == "" {
		return "", "", rpc.Errorf(412, "repository %s has no upstream", name)
	}
	dir := s.repoDir(name)
	track := "refs/upstream/" + m.DefaultBranch
	if _, err := git(ctx, dir, upstreamEnv, nil, nil, "fetch", "--quiet", "--no-tags", m.Upstream, "+refs/heads/"+m.DefaultBranch+":"+track); err != nil {
		return "", "", rpc.Unavailable("fetch from upstream failed: %v", err)
	}
	if up, err = resolve(ctx, dir, track); err != nil {
		return "", "", err
	}
	local, err = resolve(ctx, dir, "refs/heads/"+m.DefaultBranch)
	return local, up, err
}

func (s *Service) Sync(ctx context.Context, req api.SyncRequest) (api.SyncResult, error) {
	_, m, err := s.open(ctx, req.Repo)
	if err != nil {
		return api.SyncResult{}, err
	}
	defer s.lock(req.Repo)()
	local, up, err := s.fetchUpstream(ctx, req.Repo, m)
	if err != nil {
		return api.SyncResult{}, err
	}
	dir := s.repoDir(req.Repo)
	res := api.SyncResult{Commit: local, UpstreamCommit: up}
	if local == up {
		res.Status = "current"
		return res, nil
	}
	if ok, err := isAncestor(ctx, dir, up, local); err != nil || ok {
		res.Status = "ahead"
		return res, err
	}
	ff, err := isAncestor(ctx, dir, local, up)
	if err != nil {
		return res, err
	}
	if !ff {
		res.Status = "diverged"
		return res, nil
	}
	if _, err := git(ctx, dir, nil, nil, nil, "update-ref", "refs/heads/"+m.DefaultBranch, up, local); err != nil {
		return res, rpc.Conflict("branch %s moved during sync", m.DefaultBranch)
	}
	res.Status, res.Commit = "updated", up
	return res, nil
}

func (s *Service) Publish(ctx context.Context, req api.PublishRequest) (api.PublishResult, error) {
	_, m, err := s.open(ctx, req.Repo)
	if err != nil {
		return api.PublishResult{}, err
	}
	defer s.lock(req.Repo)()
	local, up, err := s.fetchUpstream(ctx, req.Repo, m)
	if err != nil {
		return api.PublishResult{}, err
	}
	dir := s.repoDir(req.Repo)
	msg := func(s string) *string { return &s }
	if local == up {
		return api.PublishResult{Pushed: false, Commit: local, Message: msg("already published")}, nil
	}
	if ok, err := isAncestor(ctx, dir, up, local); err != nil || !ok {
		return api.PublishResult{Pushed: false, Commit: local, Message: msg("the upstream has commits that are not here; sync first")}, err
	}
	if _, err := git(ctx, dir, upstreamEnv, nil, nil, "push", "--quiet", "--no-verify", m.Upstream, local+":refs/heads/"+m.DefaultBranch); err != nil {
		return api.PublishResult{Pushed: false, Commit: local, Message: msg(err.Error())}, nil
	}
	return api.PublishResult{Pushed: true, Commit: local}, nil
}
