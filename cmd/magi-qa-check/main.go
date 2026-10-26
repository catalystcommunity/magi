// magi-qa-check exercises the installed API with real application credentials.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/config"

	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/workflow"
)

func check() error {
	path := flag.String("profile", config.ProfilePath("qa", "client.json"), "Private enrolled CLI profile")
	run := flag.String("run", "", "New live check ID")
	flag.Parse()
	profile, err := question.LoadProfile(*path)
	if err != nil {
		return err
	}
	if *run == "" {
		*run = "live-" + time.Now().UTC().Format("20060102-150405")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	client, err := question.NewClient(profile)
	if err != nil {
		return err
	}
	carrier, err := workflow.NewHTTPTransport(profile.Endpoint)
	if err != nil {
		return err
	}
	rpc := w.NewWorkflowsClient(carrier)
	deny := func(label string, err error) error {
		var remote *protocol.RemoteError
		if !errors.As(err, &remote) || remote.Detail.Code != 1403 {
			return fmt.Errorf("%s was not denied: %v", label, err)
		}
		fmt.Println("PASS " + label)
		time.Sleep(time.Second)
		return nil
	}
	_, err = rpc.ReadRun(ctx, w.SignedRecord{})
	// An absent signature can fail strict record decoding before authentication.
	var remote *protocol.RemoteError
	if !errors.As(err, &remote) || (remote.Detail.Code != 1400 && remote.Detail.Code != 1403) {
		return fmt.Errorf("unsigned read was not refused: %v", err)
	}
	fmt.Println("PASS unsigned read refused")
	bad := profile
	_, bad.PrivateKey, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	badClient, err := question.NewClient(bad)
	if err != nil {
		return err
	}
	_, err = badClient.Read(ctx, *run)
	if err = deny("invalid signature", err); err != nil {
		return err
	}
	bad = profile
	bad.Owner.ScopeId = "not-granted"
	badClient, err = question.NewClient(bad)
	if err != nil {
		return err
	}
	_, err = badClient.Read(ctx, *run)
	if err = deny("ungranted scope", err); err != nil {
		return err
	}
	bad = profile
	bad.Recipient.InstanceId = "wrong-recipient"
	badClient, err = question.NewClient(bad)
	if err != nil {
		return err
	}
	_, err = badClient.Read(ctx, *run)
	if err = deny("wrong recipient", err); err != nil {
		return err
	}
	packet, err := workflow.Sign("read-run", w.EncodeReadWorkflowRun(w.ReadWorkflowRun{Owner: profile.Owner, RunId: *run}), profile.Actor, profile.Recipient, profile.KeyID, profile.PrivateKey, time.Now().Add(-2*time.Minute).UTC().Truncate(time.Millisecond))
	if err != nil {
		return err
	}
	_, err = rpc.ReadRun(ctx, packet)
	if err = deny("stale proof", err); err != nil {
		return err
	}
	start := time.Now()
	if _, err = client.Submit(ctx, *run, "What is 2 + 2? Answer with just the number."); err != nil {
		return err
	}
	// Give admission and dispatch a chance to finish before the first RP read.
	time.Sleep(time.Second)
	answer, err := client.Wait(ctx, *run)
	if err != nil {
		return err
	}
	if strings.TrimSpace(answer) != "4" {
		return fmt.Errorf("unexpected arithmetic answer: %q", answer)
	}
	time.Sleep(time.Second)
	before, err := client.Evidence(ctx, *run)
	if err != nil {
		return err
	}
	if before.Outcome != "returned" || before.Response == nil || !json.Valid(before.Request) || !json.Valid(*before.Response) {
		return errors.New("returned evidence is incomplete")
	}
	time.Sleep(time.Second)
	if _, err = client.Submit(ctx, *run, "What is 2 + 2? Answer with just the number."); err != nil {
		return err
	}
	time.Sleep(time.Second)
	after, err := client.Evidence(ctx, *run)
	if err != nil {
		return err
	}
	if !bytes.Equal(w.EncodeWorkflowCallEvidence(before), w.EncodeWorkflowCallEvidence(after)) {
		return errors.New("identical retry changed call evidence")
	}
	fmt.Printf("PASS completed answer, retained evidence, and identical retry: run=%s elapsed=%s\n", *run, time.Since(start).Round(time.Millisecond))
	return nil
}
func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
