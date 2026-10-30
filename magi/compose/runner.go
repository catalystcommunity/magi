package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
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

// ModelProxy serves an OpenAI-compatible Chat Completions endpoint for
// runners at /runner/v1/chat/completions. It sends the request to the
// coding model with Magi's own provider key, so a runner machine holds no
// provider key. The model name in the request is replaced.
func ModelProxy(ver *authn.Verifier, c *model.Client) http.Handler {
	hc := &http.Client{Timeout: 10 * time.Minute}
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
