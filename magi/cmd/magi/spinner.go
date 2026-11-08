package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// spinner shows on standard error that Magi builds a reply. It shows only
// on a terminal, so a script that reads the output gets only the messages.
type spinner struct {
	mu   sync.Mutex
	on   bool
	stop chan struct{}
	text string
}

func startSpinner(text string) *spinner {
	s := &spinner{text: text}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return s
	}
	s.on, s.stop = true, make(chan struct{})
	go func() {
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		start := time.Now()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			s.mu.Lock()
			if s.on {
				fmt.Fprintf(os.Stderr, "\r%c %s %ds ", frames[i%len(frames)], s.text, int(time.Since(start).Seconds()))
			}
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			case <-t.C:
			}
		}
	}()
	return s
}

// clear removes the spinner line; run f, such as a print, while it is gone.
func (s *spinner) clear(f func()) {
	if s == nil || s.stop == nil {
		f()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.on {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	f()
}

// done stops the spinner and removes its line.
func (s *spinner) done() {
	if s == nil || s.stop == nil {
		return
	}
	s.mu.Lock()
	if s.on {
		s.on = false
		fmt.Fprint(os.Stderr, "\r\033[K")
		close(s.stop)
	}
	s.mu.Unlock()
}
