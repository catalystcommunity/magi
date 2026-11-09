package runner

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
)

// Terminals: a person opens a terminal to the sandbox of a task on this
// runner (magi/compose/terminal.go). Magi gives the session in the
// terminals long poll. The runner connects out to GET /runner/terminal
// on Magi and copies that connection to the PTY route of envd in the
// sandbox. The shell runs in the sandbox, with the network of the
// sandbox (the egress proxy route); the runner adds no route.

// terminalLoop serves the terminal sessions that Magi gives this runner.
func (r *Runner) terminalLoop(ctx context.Context) {
	for ctx.Err() == nil {
		res, err := r.Magi.Terminals(ctx, magiapi.RunnerTerminalsRequest{WaitMs: ptr(uint64(20000))})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if rpc.CodeOf(err) == 404 {
				// An older Magi release has no terminals operation.
				sleep(ctx, 5*time.Minute)
				continue
			}
			r.Log.Warn("terminals failed", "err", err)
			sleep(ctx, 5*time.Second)
			continue
		}
		for _, t := range res.Terminals {
			go r.serveTerminal(ctx, t)
		}
	}
}

// serveTerminal connects one terminal session to the sandbox of its run.
func (r *Runner) serveTerminal(ctx context.Context, t magiapi.RunnerTerminal) {
	log := r.Log.With("run", t.Run, "session", t.Session)
	conn, err := r.dialMagi(ctx, "/runner/terminal?session="+url.QueryEscape(t.Session))
	if err != nil {
		log.Warn("terminal: connect to Magi", "err", err)
		return
	}
	defer conn.Close()
	fail := func(msg string) {
		log.Warn("terminal refused", "reason", msg)
		envd.WriteFrame(conn, envd.FrameError, []byte(msg))
	}
	env := t.Environment
	if env == "" {
		env = t.Run
	}
	if !r.Boxes.Exists(env) {
		fail(fmt.Sprintf("The sandbox of task %s is not on runner %s now.", t.Run, r.Cfg.Name))
		return
	}
	c, err := r.envd(env)
	if err != nil {
		fail(fmt.Sprintf("The sandbox of task %s does not answer: %v", t.Run, err))
		return
	}
	req := envd.PTYRequest{Cwd: deref(t.Cwd), Cols: uint16(t.Cols), Rows: uint16(t.Rows)}
	pty, err := c.PTY(ctx, req)
	if err != nil && req.Cwd != "" {
		// The work directory can be missing (an early step): use the
		// directory of envd.
		req.Cwd = ""
		pty, err = c.PTY(ctx, req)
	}
	if err != nil {
		fail(fmt.Sprintf("The shell did not start: %v", err))
		return
	}
	start := time.Now()
	log.Info("terminal opened", "environment", env)
	envd.Pipe(conn, pty)
	log.Info("terminal closed", "seconds", int(time.Since(start).Seconds()))
}
