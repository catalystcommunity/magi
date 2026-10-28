package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/source/api"
)

func ptr[T any](v T) *T { return &v }

// must returns v. An error panics, which fails the test with a stack trace.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func code(t *testing.T, err error, want int) {
	t.Helper()
	if got := rpc.CodeOf(err); got != want {
		t.Fatalf("want code %d, got %v", want, err)
	}
}

const (
	alice = "user:alice@example.local"
	bob   = "user:bob@example.local"
)

type env struct {
	url                    string
	dir                    string
	tokens                 map[string]string
	alice, bob, magi, anon *api.SourceClient
}

func newEnv(t *testing.T) *env {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	s := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, MaxBody: MaxRequest}
	e := &env{dir: t.TempDir(), tokens: map[string]string{}}
	ts := httptest.NewServer(Handler(s, &Service{Dir: e.dir}))
	t.Cleanup(ts.Close)
	e.url = ts.URL
	client := func(p authn.Principal) *api.SourceClient {
		tok, err := iss.Issue(p, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		e.tokens[p.Subject] = tok
		return api.NewSourceClient(rpc.NewClient(ts.URL, rpc.StaticToken(tok)))
	}
	e.alice = client(authn.Principal{Subject: alice, Kind: authn.KindUser})
	e.bob = client(authn.Principal{Subject: bob, Kind: authn.KindUser})
	e.magi = client(authn.Principal{Subject: "service:magi", Kind: authn.KindService, Scopes: []string{ScopeActAs}})
	e.anon = api.NewSourceClient(rpc.NewClient(ts.URL, nil))
	return e
}

// cloneURL returns the clone URL with the token of subject as password.
func (e *env) cloneURL(repo api.Repository, subject string) string {
	u, _ := url.Parse(e.url + repo.ClonePath)
	if subject != "" {
		u.User = url.UserPassword("x", e.tokens[subject])
	}
	return u.String()
}

// sh runs git in dir with an isolated configuration and returns stdout.
func sh(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	home := t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.local", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), &gitError{args: args, code: cmd.ProcessState.ExitCode(), stderr: string(out)}
	}
	return strings.TrimSpace(string(out)), nil
}

func gitOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := sh(t, dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOK(t, dir, "add", name)
	gitOK(t, dir, "commit", "-q", "-m", msg)
}

func TestValidation(t *testing.T) {
	for _, n := range []string{"", "A", "-x", ".x", "a/b", "a b", "x.git", strings.Repeat("a", 65)} {
		if validName(n) == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	for _, n := range []string{"magi", "a.b-c_d", "0"} {
		if validName(n) != nil {
			t.Errorf("name %q rejected", n)
		}
	}
	ctx := context.Background()
	for _, b := range []string{"", "-f", "a..b", "a b", "refs/heads/x", "x.lock", "a~1"} {
		if validBranch(ctx, b) == nil {
			t.Errorf("branch %q accepted", b)
		}
	}
	if validBranch(ctx, "feature/x") != nil {
		t.Error("feature/x rejected")
	}
}

func TestSource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	repo := must(e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "demo"}))
	if repo.Owner != alice || repo.DefaultBranch != "main" || repo.ClonePath != "/git/demo.git" {
		t.Fatalf("repo: %+v", repo)
	}
	_, err := e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "demo"})
	code(t, err, 409)
	_, err = e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "Bad/Name"})
	code(t, err, 400)
	_, err = e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "x", DefaultBranch: ptr("a..b")})
	code(t, err, 400)
	_, err = e.alice.Bundle(ctx, api.BundleRequest{Repo: "demo"})
	code(t, err, 409)

	// Clone the empty repository, commit, and push over smart HTTP.
	work := t.TempDir()
	gitOK(t, work, "clone", "-q", e.cloneURL(repo, alice), "w")
	w := filepath.Join(work, "w")
	commitFile(t, w, "a.txt", "one\ntwo\nthree\n", "first")
	gitOK(t, w, "push", "-q", "origin", "HEAD:main")

	// Branch "clean" changes b.txt. Branch "conflict" changes a.txt line two.
	gitOK(t, w, "checkout", "-q", "-b", "clean")
	commitFile(t, w, "b.txt", "new\n", "add b")
	gitOK(t, w, "checkout", "-q", "-b", "conflict", "main")
	commitFile(t, w, "a.txt", "one\nTWO-conflict\nthree\n", "conflict a")
	gitOK(t, w, "checkout", "-q", "main")
	gitOK(t, w, "branch", "behind")
	commitFile(t, w, "a.txt", "one\nTWO-main\nthree\n", "main a")
	gitOK(t, w, "push", "-q", "origin", "main", "clean", "conflict", "behind")

	refs := must(e.alice.ListRefs(ctx, api.ListRefsRequest{Repo: "demo"}))
	if len(refs.Refs) != 4 {
		t.Fatalf("refs: %+v", refs.Refs)
	}
	got := must(e.alice.GetRepo(ctx, api.GetRepoRequest{Name: "demo"}))
	if got != repo {
		t.Fatalf("get-repo: %+v", got)
	}

	// diff uses the merge base of base and head.
	d := must(e.alice.Diff(ctx, api.DiffRequest{Repo: "demo", Base: "main", Head: "clean"}))
	if len(d.Files) != 1 || d.Files[0] != (api.FileChange{Path: "b.txt", Additions: 1}) || !strings.Contains(d.Patch, "+new") || d.Truncated {
		t.Fatalf("diff: %+v", d)
	}
	if d.HeadCommit != gitOK(t, w, "rev-parse", "clean") || d.BaseCommit != gitOK(t, w, "rev-parse", "main") {
		t.Fatal("diff commits")
	}
	if d := must(e.alice.Diff(ctx, api.DiffRequest{Repo: "demo", Base: "main", Head: "conflict", MaxBytes: ptr(uint64(10))})); !d.Truncated || len(d.Patch) != 10 {
		t.Fatalf("truncated diff: %+v", d)
	}
	_, err = e.alice.Diff(ctx, api.DiffRequest{Repo: "demo", Base: "main", Head: "nope"})
	code(t, err, 404)
	_, err = e.alice.Diff(ctx, api.DiffRequest{Repo: "demo", Base: "--output=/tmp/x", Head: "main"})
	code(t, err, 400)

	// merge: fast-forward only refused, conflict, clean, then fast-forward.
	r := must(e.alice.Merge(ctx, api.MergeRequest{Repo: "demo", Source: "clean", Target: "main", FastForwardOnly: ptr(true)}))
	if r.Merged {
		t.Fatalf("ff-only merged: %+v", r)
	}
	main0 := gitOK(t, w, "rev-parse", "main")
	r = must(e.alice.Merge(ctx, api.MergeRequest{Repo: "demo", Source: "conflict", Target: "main"}))
	if r.Merged || r.Commit != main0 || !strings.Contains(*r.Message, "a.txt") {
		t.Fatalf("conflict: %+v", r)
	}
	r = must(e.alice.Merge(ctx, api.MergeRequest{Repo: "demo", Source: "clean", Target: "main", Message: ptr("Merge clean")}))
	if !r.Merged || r.Commit == main0 {
		t.Fatalf("clean merge: %+v", r)
	}
	gitOK(t, w, "fetch", "-q", "origin")
	if gitOK(t, w, "rev-parse", "origin/main") != r.Commit || gitOK(t, w, "log", "-1", "--format=%s%n%P", r.Commit) != "Merge clean\n"+main0+" "+gitOK(t, w, "rev-parse", "clean") {
		t.Fatal("merge commit")
	}
	if gitOK(t, w, "show", "origin/main:b.txt") != "new" {
		t.Fatal("merged content")
	}
	if ff := must(e.alice.Merge(ctx, api.MergeRequest{Repo: "demo", Source: "main", Target: "behind", FastForwardOnly: ptr(true)})); !ff.Merged || ff.Commit != r.Commit {
		t.Fatalf("fast-forward: %+v", ff)
	}
	if got := gitOK(t, w, "ls-remote", "origin", "refs/heads/behind"); !strings.HasPrefix(got, r.Commit) {
		t.Fatalf("fast-forward ref: %s", got)
	}
	if up := must(e.alice.Merge(ctx, api.MergeRequest{Repo: "demo", Source: "behind", Target: "main"})); !up.Merged || up.Commit != r.Commit {
		t.Fatalf("up to date: %+v", up)
	}
	_, err = e.alice.Merge(ctx, api.MergeRequest{Repo: "demo", Source: "clean", Target: "missing"})
	code(t, err, 404)

	// delete-branch.
	if d := must(e.alice.DeleteBranch(ctx, api.DeleteBranchRequest{Repo: "demo", Branch: "clean"})); !d.Deleted {
		t.Fatal("not deleted")
	}
	if d := must(e.alice.DeleteBranch(ctx, api.DeleteBranchRequest{Repo: "demo", Branch: "clean"})); d.Deleted {
		t.Fatal("deleted twice")
	}
	_, err = e.alice.DeleteBranch(ctx, api.DeleteBranchRequest{Repo: "demo", Branch: "main"})
	code(t, err, 400)

	// bundle, then seed a new repository from it.
	b := must(e.alice.Bundle(ctx, api.BundleRequest{Repo: "demo"}))
	copyRepo := must(e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "copy", SeedBundle: &b.Bundle}))
	cr := must(e.alice.ListRefs(ctx, api.ListRefsRequest{Repo: copyRepo.Name}))
	if len(cr.Refs) != 3 {
		t.Fatalf("seeded refs: %+v", cr.Refs)
	}
	gitOK(t, work, "clone", "-q", e.cloneURL(copyRepo, alice), "c")
	if gitOK(t, filepath.Join(work, "c"), "rev-parse", "HEAD") != r.Commit {
		t.Fatal("seeded clone HEAD")
	}
	_, err = e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "badseed", SeedBundle: ptr([]byte("nope"))})
	code(t, err, 400)

	if l := must(e.alice.ListRepos(ctx, api.ListReposRequest{})); len(l.Repositories) != 2 {
		t.Fatalf("list: %+v", l)
	}
	if l := must(e.alice.ListRepos(ctx, api.ListReposRequest{Limit: ptr(uint64(1))})); len(l.Repositories) != 1 {
		t.Fatalf("list limit: %+v", l)
	}
}

func TestAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := must(e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "private"}))

	_, err := e.anon.GetRepo(ctx, api.GetRepoRequest{Name: "private"})
	code(t, err, 401)
	_, err = e.bob.GetRepo(ctx, api.GetRepoRequest{Name: "private"})
	code(t, err, 404)
	_, err = e.bob.ListRefs(ctx, api.ListRefsRequest{Repo: "private"})
	code(t, err, 404)
	_, err = e.bob.Diff(ctx, api.DiffRequest{Repo: "private", Base: "main", Head: "main"})
	code(t, err, 404)
	_, err = e.bob.Merge(ctx, api.MergeRequest{Repo: "private", Source: "a", Target: "main"})
	code(t, err, 404)
	_, err = e.bob.DeleteBranch(ctx, api.DeleteBranchRequest{Repo: "private", Branch: "x"})
	code(t, err, 404)
	_, err = e.bob.Bundle(ctx, api.BundleRequest{Repo: "private"})
	code(t, err, 404)
	_, err = e.bob.CreateRepo(ctx, api.CreateRepoRequest{Name: "forged", Owner: ptr(alice)})
	code(t, err, 403)
	_, err = e.bob.ListRepos(ctx, api.ListReposRequest{Owner: ptr(alice)})
	code(t, err, 403)
	if l := must(e.bob.ListRepos(ctx, api.ListReposRequest{})); len(l.Repositories) != 0 {
		t.Fatal("bob lists alice's repository")
	}

	// Git HTTP: no credentials, a bad token, and another user are refused.
	work := t.TempDir()
	for _, u := range []string{e.cloneURL(repo, ""), strings.Replace(e.cloneURL(repo, alice), "magi1.", "magi1.x", 1), e.cloneURL(repo, bob)} {
		if out, err := sh(t, work, "clone", "-q", u, "denied"); err == nil {
			t.Fatalf("clone allowed: %s", out)
		}
	}
	for _, p := range []string{"/git/private.git/HEAD", "/git/private.git/objects/info/packs", "/git/private.git/magi.json"} {
		resp, err := http.Get(e.url + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}

	// Bob cannot push to alice's repository.
	gitOK(t, work, "init", "-q", "-b", "main", "b")
	bw := filepath.Join(work, "b")
	commitFile(t, bw, "x", "x", "x")
	if _, err := sh(t, bw, "push", "-q", e.cloneURL(repo, bob), "main"); err == nil {
		t.Fatal("bob pushed")
	}

	// A service with act-as creates a repository for alice and pushes to it.
	svcRepo := must(e.magi.CreateRepo(ctx, api.CreateRepoRequest{Name: "for-alice", Owner: ptr(alice), DefaultBranch: ptr("trunk")}))
	if svcRepo.Owner != alice {
		t.Fatalf("act-as owner: %s", svcRepo.Owner)
	}
	gitOK(t, bw, "push", "-q", e.cloneURL(svcRepo, "service:magi"), "main:trunk")
	if refs := must(e.alice.ListRefs(ctx, api.ListRefsRequest{Repo: "for-alice"})); len(refs.Refs) != 1 || refs.Refs[0].Name != "refs/heads/trunk" {
		t.Fatalf("act-as push: %+v", refs)
	}
	if l := must(e.magi.ListRepos(ctx, api.ListReposRequest{Owner: ptr(alice)})); len(l.Repositories) != 2 {
		t.Fatalf("act-as list: %+v", l)
	}
	// Alice clones it.
	gitOK(t, work, "clone", "-q", e.cloneURL(svcRepo, alice), "a")
}

func TestPushBundle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := must(e.alice.CreateRepo(ctx, api.CreateRepoRequest{Name: "pb"}))
	work := t.TempDir()
	gitOK(t, work, "init", "-q", "-b", "main", "w")
	w := filepath.Join(work, "w")
	commitFile(t, w, "a", "a\n", "a")
	bundle := func(args ...string) []byte {
		f := filepath.Join(work, "out.bundle")
		gitOK(t, w, append([]string{"bundle", "create", "-q", f}, args...)...)
		return must(os.ReadFile(f))
	}
	push := func(b []byte, force bool) (api.PushBundleResponse, error) {
		return e.alice.PushBundle(ctx, api.PushBundleRequest{Repo: repo.Name, Bundle: b, Force: &force})
	}

	// An empty repository takes its default branch from a bundle.
	if r := must(push(bundle("main"), false)); len(r.Updated) != 1 || r.Updated[0].Name != "refs/heads/main" || r.Updated[0].Commit != gitOK(t, w, "rev-parse", "main") {
		t.Fatalf("first import: %+v", r)
	}
	// A bundle with a prerequisite adds a branch. The same bundle again changes nothing.
	gitOK(t, w, "checkout", "-q", "-b", "feature")
	commitFile(t, w, "b", "b\n", "b")
	fb := bundle("feature", "^main")
	if r := must(push(fb, false)); len(r.Updated) != 1 || r.Updated[0].Name != "refs/heads/feature" {
		t.Fatalf("feature import: %+v", r)
	}
	if r := must(push(fb, false)); len(r.Updated) != 0 {
		t.Fatalf("repeat import: %+v", r)
	}
	// A rewritten branch needs force.
	gitOK(t, w, "commit", "-q", "--amend", "-m", "b2")
	_, err := push(bundle("feature", "^main"), false)
	code(t, err, 409)
	must(push(bundle("feature", "^main"), true))
	// The default branch needs force.
	gitOK(t, w, "checkout", "-q", "main")
	commitFile(t, w, "c", "c\n", "c")
	_, err = push(bundle("main"), false)
	code(t, err, 409)
	must(push(bundle("main"), true))
	refs := must(e.alice.ListRefs(ctx, api.ListRefsRequest{Repo: repo.Name}))
	if len(refs.Refs) != 2 || refs.Refs[0].Commit != gitOK(t, w, "rev-parse", "feature") || refs.Refs[1].Commit != gitOK(t, w, "rev-parse", "main") {
		t.Fatalf("refs: %+v", refs.Refs)
	}

	// Only refs/heads/ refs are imported; bad bundles are refused.
	gitOK(t, w, "tag", "v1")
	_, err = push(bundle("refs/tags/v1"), false)
	code(t, err, 400)
	_, err = push([]byte("not a bundle"), false)
	code(t, err, 400)
	_, err = e.bob.PushBundle(ctx, api.PushBundleRequest{Repo: repo.Name, Bundle: fb})
	code(t, err, 404)

	// bundle with refs returns only the named branch.
	b := must(e.alice.Bundle(ctx, api.BundleRequest{Repo: repo.Name, Refs: []string{"feature"}}))
	f := filepath.Join(work, "one.bundle")
	os.WriteFile(f, b.Bundle, 0o600)
	if heads := gitOK(t, w, "bundle", "list-heads", f); strings.Count(heads, "\n") != 0 || !strings.HasSuffix(heads, " refs/heads/feature") {
		t.Fatalf("list-heads: %q", heads)
	}
	_, err = e.alice.Bundle(ctx, api.BundleRequest{Repo: repo.Name, Refs: []string{"missing"}})
	code(t, err, 404)
	_, err = e.alice.Bundle(ctx, api.BundleRequest{Repo: repo.Name, Refs: []string{"--all"}})
	code(t, err, 400)
	if r := must(e.alice.ListRefs(ctx, api.ListRefsRequest{Repo: repo.Name})); len(r.Refs) != 2 {
		t.Fatalf("import refs left behind: %+v", r.Refs)
	}
}
