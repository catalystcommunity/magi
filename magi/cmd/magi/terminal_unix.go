//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// watchResize calls f with the new size of the terminal fd after each
// SIGWINCH, until the returned function is called.
func watchResize(fd int, f func(cols, rows int)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				if w, h, err := term.GetSize(fd); err == nil {
					f(w, h)
				}
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
