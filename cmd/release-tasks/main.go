// release-tasks is a bounded, trusted local helper, not a network API or tool
// for agents. Its private profile has queue/database access, not release keys.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/releasework"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

type config struct {
	Database     string           `json:"database"`
	Installation string           `json:"installation"`
	Seeds        []string         `json:"corndogs_seeds"`
	CAFile       string           `json:"corndogs_ca_file"`
	ServerName   string           `json:"corndogs_server_name"`
	Controller   w.ApplicationRef `json:"controller"`
	Recorder     w.ApplicationRef `json:"recorder"`
	Target       string           `json:"target"`
}
type request struct {
	Operation string               `json:"operation"`
	Binding   releasework.Binding  `json:"binding"`
	Outcome   *releasework.Outcome `json:"outcome,omitempty"`
}

func decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return workflow.ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return workflow.ErrInvalid
	}
	return nil
}
func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) != 2 || (args[0] != "--migrate" && args[0] != "--request" && args[0] != "--check") {
		return workflow.ErrInvalid
	}
	stat, e := os.Lstat(args[1])
	if e != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() > 16384 {
		return workflow.ErrInvalid
	}
	raw, e := os.ReadFile(args[1])
	if e != nil {
		return e
	}
	var c config
	if decode(raw, &c) != nil || c.Database == "" || c.Target == "" || c.Controller == c.Recorder {
		return workflow.ErrInvalid
	}
	transport := &workflow.Transport{Seeds: c.Seeds}
	if c.ServerName != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return err
		}
		if c.CAFile != "" {
			pem, err := os.ReadFile(c.CAFile)
			if err != nil || !roots.AppendCertsFromPEM(pem) {
				return workflow.ErrInvalid
			}
		}
		transport.TLS = &tls.Config{RootCAs: roots, ServerName: c.ServerName, MinVersion: tls.VersionTLS12}
	} else if c.CAFile != "" {
		return workflow.ErrInvalid
	}
	pc, e := pgxpool.ParseConfig(c.Database)
	if e != nil {
		return e
	}
	pc.MaxConns = 2
	pc.ConnConfig.ConnectTimeout = 3 * time.Second
	pc.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	pc.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	pool, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		return e
	}
	defer pool.Close()
	j := &workflow.Journal{Pool: pool, Installation: c.Installation, Transport: transport}
	if args[0] == "--check" {
		if _, e = j.Check(ctx); e != nil {
			return e
		}
		_, e = fmt.Fprintln(output, `{"ready":true}`)
		return e
	}
	if args[0] == "--migrate" {
		if e = j.Initialize(ctx); e != nil {
			return e
		}
		_, e = j.Check(ctx)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(output, `{"initialized":true}`)
		return e
	}
	raw, e = io.ReadAll(io.LimitReader(input, 8193))
	if e != nil || len(raw) > 8192 {
		return workflow.ErrInvalid
	}
	var r request
	if decode(raw, &r) != nil || (r.Operation != "start" && r.Operation != "finish") || (r.Operation == "start" && r.Outcome != nil) || (r.Operation == "finish" && r.Outcome == nil) {
		return workflow.ErrInvalid
	}
	tasks := &releasework.Tasks{Journal: j, Controller: c.Controller, Recorder: c.Recorder, Target: c.Target}
	var receipt releasework.Receipt
	if r.Operation == "start" {
		receipt, e = tasks.Start(ctx, r.Binding)
	} else {
		receipt, e = tasks.Finish(ctx, r.Binding, *r.Outcome)
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(output).Encode(receipt)
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdin, os.Stdout); e != nil {
		code := "unavailable"
		if errors.Is(e, workflow.ErrReconcile) || errors.Is(e, workflow.ErrStale) || errors.Is(e, workflow.ErrConflict) || errors.Is(e, workflow.ErrRejected) {
			code = "reconcile_required"
		}
		if _, final := api.ServiceErrorCode(e); final {
			code = "reconcile_required"
		}
		if errors.Is(e, workflow.ErrInvalid) {
			code = "invalid_request"
		}
		fmt.Fprintln(os.Stderr, "Release task operation failed: "+code)
		os.Exit(1)
	}
}
