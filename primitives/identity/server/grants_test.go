package server_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
)

const person = "user:019d6ac9-58bf-7303-8a5a-e60fba633239@example.local"

func ptr[T any](v T) *T { return &v }

func code(err error) int {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

// Credentials: encrypted at rest, owner rules, revision check, and the
// present scope for values.
func TestCredentials(t *testing.T) {
	e := setup(t)
	key := make([]byte, 32)
	rand.Read(key)
	e.svc.CredentialKey = key
	ctx := context.Background()
	user := e.client(e.keys.Token(t, person))
	other := e.client(e.keys.Token(t, "user:01a083dc-b31f-7c53-a689-56ea92268809@example.local"))
	magi := e.client(e.keys.Token(t, "service:magi", "identity:act-as", "identity:present"))

	c, err := user.PutCredential(ctx, api.PutCredentialRequest{Owner: person, System: "longhouse", Value: "refresh-1", Label: ptr("CLI session"), ExpectedRevision: ptr(uint64(0))})
	if err != nil || c.Revision != 1 || c.Value != nil {
		t.Fatalf("put: %+v %v", c, err)
	}
	if _, err := other.PutCredential(ctx, api.PutCredentialRequest{Owner: person, System: "longhouse", Value: "x"}); code(err) != 403 {
		t.Fatalf("another user put: %v", err)
	}
	if _, err := user.GetCredential(ctx, api.CredentialRef{Owner: person, System: "longhouse"}); code(err) != 403 {
		t.Fatalf("a user cannot read values: %v", err)
	}
	got, err := magi.GetCredential(ctx, api.CredentialRef{Owner: person, System: "longhouse"})
	if err != nil || got.Value == nil || *got.Value != "refresh-1" || got.Label != "CLI session" {
		t.Fatalf("get: %+v %v", got, err)
	}
	// A rotation with the old revision after another rotation conflicts.
	if c, err = magi.PutCredential(ctx, api.PutCredentialRequest{Owner: person, System: "longhouse", Value: "refresh-2", ExpectedRevision: ptr(uint64(1))}); err != nil || c.Revision != 2 {
		t.Fatalf("rotate: %+v %v", c, err)
	}
	if _, err = magi.PutCredential(ctx, api.PutCredentialRequest{Owner: person, System: "longhouse", Value: "refresh-x", ExpectedRevision: ptr(uint64(1))}); code(err) != 409 {
		t.Fatalf("stale rotate: %v", err)
	}
	if _, err = magi.PutCredential(ctx, api.PutCredentialRequest{Owner: person, System: "longhouse", Value: "refresh-x", ExpectedRevision: ptr(uint64(0))}); code(err) != 409 {
		t.Fatalf("create over a row: %v", err)
	}
	got, _ = magi.GetCredential(ctx, api.CredentialRef{Owner: person, System: "longhouse"})
	if *got.Value != "refresh-2" {
		t.Fatalf("value after conflict: %q", *got.Value)
	}
	list, err := user.ListCredentials(ctx, api.ListCredentialsRequest{})
	if err != nil || len(list.Credentials) != 1 || list.Credentials[0].Value != nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := other.ForgetCredential(ctx, api.CredentialRef{Owner: person, System: "longhouse"}); code(err) != 403 {
		t.Fatalf("another user forget: %v", err)
	}
	if _, err := user.ForgetCredential(ctx, api.CredentialRef{Owner: person, System: "longhouse"}); err != nil {
		t.Fatal(err)
	}
	if _, err := magi.GetCredential(ctx, api.CredentialRef{Owner: person, System: "longhouse"}); code(err) != 404 {
		t.Fatalf("after forget: %v", err)
	}
}

// Without application keys, grant operations say so; present needs its scope.
func TestGrantsNeedConfiguration(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	user := e.client(e.keys.Token(t, person))
	if _, err := user.Application(ctx, api.ApplicationRequest{}); code(err) != 503 {
		t.Fatalf("application: %v", err)
	}
	if _, err := user.PresentGrant(ctx, api.PresentGrantRequest{Owner: person, Audience: api.AppRef{Domain: "example.local", UserId: "u", AppId: "cal"}, RequestDigest: make([]byte, 32)}); code(err) != 403 {
		t.Fatalf("present without scope: %v", err)
	}
	list, err := user.ListGrants(ctx, api.ListGrantsRequest{})
	if err != nil || len(list.Grants) != 0 {
		t.Fatalf("list: %+v %v", list, err)
	}
}
