package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/catalystcommunity/magi/config"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/workflow"
	"github.com/catalystcommunity/magi/workshop"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run(ctx context.Context) error {
	path := flag.String("config", ".local/dialogue/server.json", "Local test workflow configuration")
	flag.Parse()
	c, e := question.LoadDevConfig(*path)
	if e != nil {
		return e
	}
	if endpoint := config.Environment("MAGI_DEMO_MODEL_ENDPOINT"); endpoint != "" {
		c.Model.Endpoint = endpoint
	}
	if e = c.Validate(); e != nil {
		return e
	}
	poolConfig, e := pgxpool.ParseConfig(c.DatabaseURL)
	if e != nil {
		return e
	}
	poolConfig.MaxConns = 8
	poolConfig.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	pool, e := pgxpool.NewWithConfig(ctx, poolConfig)
	if e != nil {
		return e
	}
	defer pool.Close()
	store := &workflow.Store{Application: c.Application, Journal: &workflow.Journal{Pool: pool, Installation: "local-qa", Transport: &workflow.Transport{Seeds: []string{c.Corndogs}}}}
	if e = store.Check(ctx); e != nil {
		return e
	}
	worker, evidence, e := workshop.NewWorker(store, question.WorkerConfig{Actor: c.Actor, Owner: c.Owner, Model: c.Model, CallSeconds: c.CallSeconds})
	if e != nil {
		return e
	}
	if e = evidence.Initialize(ctx); e != nil {
		return e
	}
	if e = store.SetGrant(ctx, workflow.Grant{Actor: c.Actor, Owner: c.Owner, Action: "complete", Allowed: true}); e != nil {
		return e
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cleanDone := make(chan struct{})
	go func() {
		defer close(cleanDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				rows, e := pool.Query(workCtx, `SELECT owner,run FROM workshop_runs WHERE (expires_at<=clock_timestamp() OR state='closing') AND state<>'closed' LIMIT 8`)
				if e != nil {
					continue
				}
				type item struct {
					owner w.ScopeRef
					run   string
				}
				items := []item{}
				for rows.Next() {
					var owner []byte
					var run string
					if rows.Scan(&owner, &run) == nil {
						scope, e := w.DecodeScopeRef(owner)
						if e == nil {
							items = append(items, item{scope, run})
						}
					}
				}
				rows.Close()
				for _, v := range items {
					bound, done := context.WithTimeout(workCtx, 10*time.Second)
					evidence.Cleanup(bound, v.owner, v.run)
					done()
				}
			}
		}
	}()
	fmt.Fprintln(os.Stderr, "Offline task worker ready. Public fixture source only; no deployment authority.")
	e = worker.Run(workCtx, func(_ int, e error) {
		if e != nil && workCtx.Err() == nil {
			fmt.Fprintln(os.Stderr, "Task worker is delayed; inspect retained evidence.")
		}
	})
	cancel()
	<-cleanDone
	if ctx.Err() != nil {
		return nil
	}
	return e
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if e := run(ctx); e != nil {
		fmt.Fprintln(os.Stderr, "Task worker setup or execution failed.")
		os.Exit(1)
	}
}
