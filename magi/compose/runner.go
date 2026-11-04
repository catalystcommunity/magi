package compose

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	"github.com/catalystcommunity/magi/primitives/execution/egressenv"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// ScopeRunner lets a runner use RunnerService and the model proxy.
const ScopeRunner = "magi:runner"

// RunnerQueue returns the queue of a step that runner runs.
func RunnerQueue(runner, step string) string { return "runner." + runner + "." + step }

// Runners implements RunnerService. A runner connects to Magi; Magi never
// connects to it. Magi forwards each call to the workflow, artifacts, and
// conversation services with its own service token.
type Runners struct{ S *System }

var _ magiapi.RunnerService = (*Runners)(nil)

// runnerName returns the name of the calling runner.
func runnerName(ctx context.Context) (string, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return "", err
	}
	name, ok := strings.CutPrefix(p.Subject, "runner:")
	if !ok || name == "" || p.Kind != authn.KindService || !p.Has(ScopeRunner) {
		return "", rpc.Denied("a runner token (subject runner:<name>, scope %s) is required", ScopeRunner)
	}
	return name, nil
}

// held checks that the caller is a runner that holds the claim of the step.
// A claim is a secret that only the claimant got, and a runner can claim
// only from its own queues.
func (r *Runners) held(ctx context.Context, st magiapi.RunnerStep) (context.Context, error) {
	if _, err := runnerName(ctx); err != nil {
		return nil, err
	}
	sctx := r.S.AsService(ctx)
	ack, err := r.S.Workflow.Heartbeat(sctx, workflowapi.HeartbeatRequest{Run: st.Run, Step: st.Step, Claim: st.Claim})
	if err != nil {
		return nil, err
	}
	if !ack.Ok {
		return nil, rpc.Conflict("the claim of %s/%s is not held", st.Run, st.Step)
	}
	return sctx, nil
}

func (r *Runners) Claim(ctx context.Context, req magiapi.RunnerClaimRequest) (magiapi.RunnerClaimResponse, error) {
	name, err := runnerName(ctx)
	if err != nil {
		return magiapi.RunnerClaimResponse{}, err
	}
	if !strings.HasPrefix(req.Queue, "runner."+name+".") {
		return magiapi.RunnerClaimResponse{}, rpc.Denied("runner %s can claim only from queues runner.%s.*", name, name)
	}
	wait := uint64(20000)
	if req.WaitMs != nil {
		wait = min(*req.WaitMs, 25000)
	}
	res, err := r.S.Workflow.Claim(r.S.AsService(ctx), workflowapi.ClaimRequest{
		Queue: req.Queue, Worker: "runner:" + name, WaitMs: &wait, LeaseMs: ptr(uint64(lease.Milliseconds())),
	})
	if err != nil || res.Assignment == nil {
		return magiapi.RunnerClaimResponse{}, err
	}
	a := res.Assignment
	return magiapi.RunnerClaimResponse{Assignment: &magiapi.RunnerAssignment{
		Run: a.Run, Step: a.Step, Attempt: a.Attempt, Claim: a.Claim, Owner: a.Owner, Vars: a.Vars,
	}}, nil
}

func (r *Runners) Heartbeat(ctx context.Context, req magiapi.RunnerStep) (magiapi.RunnerAck, error) {
	_, err := r.held(ctx, req)
	return magiapi.RunnerAck{Ok: err == nil}, err
}

func (r *Runners) Progress(ctx context.Context, req magiapi.RunnerProgressRequest) (magiapi.RunnerAck, error) {
	sctx, err := r.held(ctx, req.Step)
	if err != nil {
		return magiapi.RunnerAck{}, err
	}
	_, err = r.S.Workflow.Progress(sctx, workflowapi.ProgressRequest{Run: req.Step.Run, Step: req.Step.Step, Claim: req.Step.Claim, Note: req.Note, Ref: req.Ref})
	return magiapi.RunnerAck{Ok: err == nil}, err
}

func (r *Runners) Complete(ctx context.Context, req magiapi.RunnerCompleteRequest) (magiapi.RunnerAck, error) {
	if _, err := runnerName(ctx); err != nil {
		return magiapi.RunnerAck{}, err
	}
	_, err := r.S.Workflow.Complete(r.S.AsService(ctx), workflowapi.CompleteRequest{
		Run: req.Step.Run, Step: req.Step.Step, Claim: req.Step.Claim, Outcome: req.Outcome, Outputs: req.Outputs, Error: req.Error,
	})
	return magiapi.RunnerAck{Ok: err == nil}, err
}

func (r *Runners) PutArtifact(ctx context.Context, req magiapi.RunnerPutArtifactRequest) (magiapi.RunnerPutArtifactResponse, error) {
	sctx, err := r.held(ctx, req.Step)
	if err != nil {
		return magiapi.RunnerPutArtifactResponse{}, err
	}
	run, err := r.S.Workflow.GetRun(sctx, workflowapi.GetRunRequest{Id: req.Step.Run})
	if err != nil {
		return magiapi.RunnerPutArtifactResponse{}, err
	}
	labels := map[string]string{}
	for k, v := range req.Labels {
		labels[k] = v
	}
	labels["run"], labels["step"] = req.Step.Run, req.Step.Step
	a, err := r.S.Artifacts.Put(sctx, artifactsapi.PutRequest{MediaType: req.MediaType, Body: req.Body, Owner: ptr(run.Owner), Labels: labels})
	if err != nil {
		return magiapi.RunnerPutArtifactResponse{}, err
	}
	return magiapi.RunnerPutArtifactResponse{Digest: a.Digest}, nil
}

func (r *Runners) Post(ctx context.Context, req magiapi.RunnerPostRequest) (magiapi.RunnerAck, error) {
	sctx, err := r.held(ctx, req.Step)
	if err != nil {
		return magiapi.RunnerAck{}, err
	}
	run, err := r.S.Workflow.GetRun(sctx, workflowapi.GetRunRequest{Id: req.Step.Run})
	if err != nil {
		return magiapi.RunnerAck{}, err
	}
	r.S.post(sctx, workflowapi.Assignment{Run: run.Id, Owner: run.Owner, Vars: run.Vars}, req.Text)
	return magiapi.RunnerAck{Ok: true}, nil
}

func (r *Runners) RunStatus(ctx context.Context, req magiapi.RunnerRunStatusRequest) (magiapi.RunnerRunStatusResponse, error) {
	name, err := runnerName(ctx)
	if err != nil {
		return magiapi.RunnerRunStatusResponse{}, err
	}
	run, err := r.S.Workflow.GetRun(r.S.AsService(ctx), workflowapi.GetRunRequest{Id: req.Run})
	if err != nil {
		return magiapi.RunnerRunStatusResponse{}, err
	}
	// Only runs that this runner works on.
	if run.Vars["runner"] != name {
		return magiapi.RunnerRunStatusResponse{}, rpc.NotFound("run %s not found", req.Run)
	}
	return magiapi.RunnerRunStatusResponse{Status: run.Status}, nil
}

func (r *Runners) Hello(ctx context.Context, req magiapi.RunnerHelloRequest) (magiapi.RunnerHelloResponse, error) {
	name, err := runnerName(ctx)
	if err != nil {
		return magiapi.RunnerHelloResponse{}, err
	}
	r.S.fleet.seen(magiapi.RunnerInfo{Name: name, Version: req.Version, Os: req.Os, Arch: req.Arch, Repos: nonNil(req.Repos),
		Publishes: nonNil(req.Publishes), Busy: req.Busy, SeenMs: uint64(time.Now().UnixMilli()), UpdateError: req.UpdateError})
	rel, ok, err := r.S.runnerRelease(ctx)
	if err != nil || !ok || rel.Version == req.Version {
		return magiapi.RunnerHelloResponse{}, err
	}
	// A runner that went back from this version does not get it again.
	if e := deref(req.UpdateError); strings.Contains(e, rel.Version) {
		return magiapi.RunnerHelloResponse{}, nil
	}
	d, ok := rel.Packages[req.Os+"/"+req.Arch]
	if !ok {
		return magiapi.RunnerHelloResponse{}, nil
	}
	a, err := r.S.Artifacts.Stat(r.S.AsService(ctx), artifactsapi.StatRequest{Digest: d})
	if err != nil {
		return magiapi.RunnerHelloResponse{}, err
	}
	return magiapi.RunnerHelloResponse{Update: &magiapi.RunnerUpdate{Version: rel.Version, Digest: d, Size: a.Size}}, nil
}

func (r *Runners) GetPackage(ctx context.Context, req magiapi.RunnerGetPackageRequest) (magiapi.RunnerGetPackageResponse, error) {
	if _, err := runnerName(ctx); err != nil {
		return magiapi.RunnerGetPackageResponse{}, err
	}
	pk, err := r.S.runnerPackages(ctx, req.Version)
	if err != nil {
		return magiapi.RunnerGetPackageResponse{}, err
	}
	d, ok := pk[req.Os+"/"+req.Arch]
	if !ok {
		return magiapi.RunnerGetPackageResponse{}, rpc.NotFound("runner release %s has no package for %s/%s", req.Version, req.Os, req.Arch)
	}
	b, err := r.S.fleetArtifact(ctx, d, kindRunnerPackage)
	return magiapi.RunnerGetPackageResponse{Digest: d, Body: b}, err
}

func (r *Runners) GetArtifact(ctx context.Context, req magiapi.RunnerGetArtifactRequest) (magiapi.RunnerGetArtifactResponse, error) {
	sctx, err := r.held(ctx, req.Step)
	if err != nil {
		return magiapi.RunnerGetArtifactResponse{}, err
	}
	g, err := r.S.Artifacts.Get(sctx, artifactsapi.GetRequest{Digest: req.Digest})
	if err != nil {
		return magiapi.RunnerGetArtifactResponse{}, err
	}
	// Only evidence of this run.
	if g.Artifact.Labels["run"] != req.Step.Run {
		return magiapi.RunnerGetArtifactResponse{}, rpc.NotFound("artifact %s not found", req.Digest)
	}
	return magiapi.RunnerGetArtifactResponse{MediaType: g.Artifact.MediaType, Body: g.Body}, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ModelProxy serves an OpenAI-compatible Chat Completions endpoint for
// runners at /runner/v1/chat/completions. It sends the request to the
// coding model with Magi's own provider key, so a runner machine holds no
// provider key. The model name in the request is replaced.
func ModelProxy(ver *authn.Verifier, c *model.Client) http.Handler {
	hc := &http.Client{Timeout: 10 * time.Minute}
	if c.HTTP != nil {
		hc.Transport = c.HTTP.Transport
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, err := ver.Verify(tok)
		if err == nil {
			_, err = runnerName(authn.With(r.Context(), p))
		}
		if err != nil {
			http.Error(w, `{"error":{"message":"a runner token is required"}}`, http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&body); err != nil {
			http.Error(w, `{"error":{"message":"bad request body"}}`, http.StatusBadRequest)
			return
		}
		body["model"] = c.Model
		b, _ := json.Marshal(body)
		out, err := http.NewRequestWithContext(r.Context(), http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(b))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out.Header.Set("Content-Type", "application/json")
		if c.APIKey != "" {
			out.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		resp, err := hc.Do(out)
		if err != nil {
			http.Error(w, `{"error":{"message":"the model provider is not available"}}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
}

// EgressProxy is how runner sandboxes reach the egress proxy: through
// GET /runner/egress on Magi (EgressTunnel) to Addr.
type EgressProxy struct {
	Addr string // host:port of the proxy port, such as egress.magi.svc:3128
	Key  []byte // the environment key of the proxy
	CA   []byte // the proxy CA certificate, PEM
	// SSHPub is the OpenSSH public key of the SSH host key of the proxy;
	// empty when the proxy does not end SSH.
	SSHPub string
}

// ServiceUser is the proxy user of a Magi service: "svc_<name>". Magi
// answers its owner as "service:<name>" (Egress.Environment).
func ServiceUser(name string) string { return "svc_" + name }

// Transport sends requests through the proxy as the proxy user user, and
// trusts only the proxy CA: the proxy ends all TLS.
func (p *EgressProxy) Transport(user string) (*http.Transport, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.CA) {
		return nil, errors.New("the egress proxy CA is not PEM")
	}
	u, err := url.Parse(egressenv.URL(p.Addr, user, egressenv.Credential(p.Key, user)))
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = http.ProxyURL(u)
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return t, nil
}

// EgressAccess gives the sandbox of a run its proxy user. The environment
// is the run; EgressService/environment then answers with the owner of the
// run.
func (r *Runners) EgressAccess(ctx context.Context, req magiapi.RunnerStep) (magiapi.RunnerEgressAccessResponse, error) {
	if _, err := r.held(ctx, req); err != nil {
		return magiapi.RunnerEgressAccessResponse{}, err
	}
	p := r.S.EgressProxy
	if p == nil {
		return magiapi.RunnerEgressAccessResponse{}, nil
	}
	out := &magiapi.RunnerEgressProxy{Environment: req.Run, Credential: egressenv.Credential(p.Key, req.Run), Ca: p.CA}
	if p.SSHPub != "" {
		// One line for all hosts: the proxy ends each SSH connection.
		out.SshKnownHosts = ptr("* " + p.SSHPub)
	}
	return magiapi.RunnerEgressAccessResponse{Proxy: out}, nil
}

// EgressTunnel serves GET /runner/egress for runners: an HTTP upgrade
// that becomes one TCP connection to the egress proxy. The upgrade has the
// form of a WebSocket upgrade so that HTTP gateways pass it; after the 101
// answer the bytes are the proxy protocol. The proxy checks the proxy user
// of each request, so a runner reaches only the environments that Magi
// gave it.
func EgressTunnel(ver *authn.Verifier, p *EgressProxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p == nil {
			http.Error(w, "no egress proxy", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "expected an upgrade", http.StatusBadRequest)
			return
		}
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		pr, err := ver.Verify(tok)
		if err == nil {
			_, err = runnerName(authn.With(r.Context(), pr))
		}
		if err != nil {
			http.Error(w, "a runner token is required", http.StatusUnauthorized)
			return
		}
		up, err := net.DialTimeout("tcp", p.Addr, 10*time.Second)
		if err != nil {
			http.Error(w, "the egress proxy is not available", http.StatusBadGateway)
			return
		}
		defer up.Close()
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no upgrade on this connection", http.StatusInternalServerError)
			return
		}
		conn, brw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Time{})
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			base64.StdEncoding.EncodeToString(sum[:]))
		if err := brw.Flush(); err != nil {
			return
		}
		if n := brw.Reader.Buffered(); n > 0 {
			b, _ := brw.Reader.Peek(n)
			if _, err := up.Write(b); err != nil {
				return
			}
		}
		envd.Splice(conn, up)
	})
}
