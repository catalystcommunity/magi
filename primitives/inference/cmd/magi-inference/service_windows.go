//go:build windows

package main

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/windows/svc"
)

// serviceName is the name of the Windows service (README, "Windows").
const serviceName = "magi-inference"

func runService(run func(context.Context) error) error {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return run(ctx)
	}
	h := &handler{run: run}
	if err := svc.Run(serviceName, h); err != nil {
		return err
	}
	return h.err
}

type handler struct {
	run func(context.Context) error
	err error
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.err = h.run(ctx); close(done) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case <-done:
			// run stopped by itself: an error. Windows restarts the
			// service (sc failure, README).
			cancel()
			if h.err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}
