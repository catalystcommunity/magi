package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
)

// sourceCtx is the caller of the in-process source service: the runner
// itself, as admin of its own mirrors.
func sourceCtx(ctx context.Context) context.Context {
	return authn.With(ctx, authn.Principal{Subject: "service:runner", Kind: authn.KindService, Scopes: []string{"source:admin"}})
}

// ensureRepos creates the mirror of each configured repository from its
// upstream.
func (r *Runner) ensureRepos(ctx context.Context) error {
	if err := os.MkdirAll(r.Source.Dir, 0o700); err != nil {
		return err
	}
	for name, repo := range r.Cfg.Repos {
		if _, err := r.Source.GetRepo(sourceCtx(ctx), sourceapi.GetRepoRequest{Name: name}); err == nil {
			continue
		} else if rpc.CodeOf(err) != 404 {
			return err
		}
		r.Log.Info("creating mirror", "repo", name, "upstream", repo.Upstream)
		if _, err := r.Source.CreateRepo(sourceCtx(ctx), sourceapi.CreateRepoRequest{
			Name: name, DefaultBranch: ptr(repo.DefaultBranch), Upstream: ptr(repo.Upstream),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) sandboxEnv() map[string]string { return map[string]string{"GOFLAGS": "-mod=mod"} }

// envd returns a client for the envd of an environment.
func (r *Runner) envd(id string) (*envd.Client, error) {
	if !r.Boxes.Alive(id) {
		if !r.Boxes.Exists(id) {
			return nil, rpc.NotFound("environment %s not found", id)
		}
		if err := r.Boxes.Resume(id, r.sandboxEnv()); err != nil {
			return nil, err
		}
	}
	base, err := r.Boxes.Wait(id, 30*time.Second)
	if err != nil {
		return nil, err
	}
	tok, err := r.Boxes.Token(id)
	if err != nil {
		return nil, err
	}
	return &envd.Client{Base: base, Token: tok, HTTP: &http.Client{Timeout: 30 * time.Minute}}, nil
}

// startExec serves the execution API that the agent uses, on loopback,
// with a token that only the agent gets.
func (r *Runner) startExec() (func(), error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	iss := &authn.Issuer{Name: "magi-runner-local", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: iss.Name, Keys: map[string]ed25519.PublicKey{"k1": pub}}
	tok, err := iss.Issue(authn.Principal{Subject: "service:agent", Kind: authn.KindService}, 10*365*24*time.Hour)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 8)
	rand.Read(b)
	r.execTokenFile = filepath.Join(r.Cfg.StateDir, "agent-token-"+hex.EncodeToString(b))
	if err := os.WriteFile(r.execTokenFile, []byte(tok), 0o600); err != nil {
		return nil, err
	}
	srv := &rpc.Server{Verifier: ver, MaxBody: 64 << 20, Log: r.Log}
	srv.Register("ExecutionService", executionapi.ExecutionServiceOps(&localExec{r: r}))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r.execURL = "http://" + l.Addr().String()
	hs := &http.Server{Handler: rpc.Mux(srv, nil)}
	go hs.Serve(l)
	return func() { hs.Close(); os.Remove(r.execTokenFile) }, nil
}

// localExec is the execution API of the runner's sandboxes. It offers
// only the operations that the agent uses.
type localExec struct{ r *Runner }

var _ executionapi.ExecutionService = (*localExec)(nil)

var errNotHere = rpc.Errorf(http.StatusNotImplemented, "not available on a runner")

func (l *localExec) Create(context.Context, executionapi.CreateRequest) (executionapi.Environment, error) {
	return executionapi.Environment{}, errNotHere
}
func (l *localExec) Get(context.Context, executionapi.GetRequest) (executionapi.Environment, error) {
	return executionapi.Environment{}, errNotHere
}
func (l *localExec) List(context.Context, executionapi.ListRequest) (executionapi.ListResponse, error) {
	return executionapi.ListResponse{}, errNotHere
}
func (l *localExec) Destroy(context.Context, executionapi.DestroyRequest) (executionapi.Environment, error) {
	return executionapi.Environment{}, errNotHere
}
func (l *localExec) Hold(context.Context, executionapi.HoldRequest) (executionapi.Environment, error) {
	return executionapi.Environment{}, errNotHere
}
func (l *localExec) Extend(context.Context, executionapi.ExtendRequest) (executionapi.Environment, error) {
	return executionapi.Environment{}, errNotHere
}

func (l *localExec) Exec(ctx context.Context, req executionapi.ExecRequest) (executionapi.ExecResult, error) {
	c, err := l.r.envd(req.Environment)
	if err != nil {
		return executionapi.ExecResult{}, err
	}
	return c.Exec(ctx, req)
}

func (l *localExec) ReadFile(ctx context.Context, req executionapi.ReadFileRequest) (executionapi.FileContent, error) {
	c, err := l.r.envd(req.Environment)
	if err != nil {
		return executionapi.FileContent{}, err
	}
	return c.ReadFile(ctx, req)
}

func (l *localExec) WriteFile(ctx context.Context, req executionapi.WriteFileRequest) (executionapi.WriteFileResponse, error) {
	c, err := l.r.envd(req.Environment)
	if err != nil {
		return executionapi.WriteFileResponse{}, err
	}
	return c.WriteFile(ctx, req)
}

func (l *localExec) ListFiles(ctx context.Context, req executionapi.ListFilesRequest) (executionapi.ListFilesResponse, error) {
	c, err := l.r.envd(req.Environment)
	if err != nil {
		return executionapi.ListFilesResponse{}, err
	}
	return c.ListFiles(ctx, req)
}
