package question

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/protocol"
)

type waitingTransport struct {
	calls   int
	failure error
}

func (t *waitingTransport) Call(_ context.Context, service, operation string, _ []byte) ([]byte, error) {
	if service != "Workflows" || operation != "read-run" {
		return nil, errors.New("wait must only read")
	}
	t.calls++
	if t.calls == 1 {
		return nil, t.failure
	}
	return w.EncodeWorkflowRun(w.WorkflowRun{Result: &w.WorkflowValues{Values: []w.WorkflowDatum{{Name: "answer", Value: "4"}}}}), nil
}
func TestWaitRetriesOnlyExplicitTransientReads(t *testing.T) {
	for _, retryable := range []bool{true, false} {
		transport := &waitingTransport{failure: &protocol.RemoteError{Detail: w.ServiceError{Code: 1500, Message: "unavailable", Retryable: retryable}}}
		client := testWaitClient(t, transport)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		answer, err := client.Wait(ctx, "test")
		cancel()
		if retryable && (err != nil || answer != "4" || transport.calls != 2) {
			t.Fatalf("read recovery: %q %v calls=%d", answer, err, transport.calls)
		}
		if !retryable && (err == nil || transport.calls != 1) {
			t.Fatal("permanent refusal was retried")
		}
	}
}
func testWaitClient(t *testing.T, transport *waitingTransport) *Client {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	actor := w.ApplicationRef{Identity: w.IdentityRef{Domain: "local.opal.test", AccountId: "test-human"}, ApplicationId: "cli", InstanceId: "test"}
	profile := Profile{Endpoint: "http://127.0.0.1:8093/csil/v1/rpc", Actor: actor, Recipient: actor, Owner: w.ScopeRef{Authority: actor.Identity, ScopeId: "test"}, KeyID: "test-key", PrivateKey: key}
	client, err := NewClient(profile)
	if err != nil {
		t.Fatal(err)
	}
	client.rpc = w.NewWorkflowsClient(transport)
	return client
}
func TestPublicPollingBudget(t *testing.T) {
	if pollInterval("https://workflows.catalystsquad.local/csil/v1/rpc") != 5*time.Second {
		t.Fatal("public reads exceed the handshake budget")
	}
	if pollInterval("http://127.0.0.1:8093/csil/v1/rpc") != 250*time.Millisecond {
		t.Fatal("local fixture polling changed")
	}
}
