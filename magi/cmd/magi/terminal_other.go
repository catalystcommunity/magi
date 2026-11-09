//go:build !unix

package main

// watchResize: this system has no SIGWINCH; the size stays as at the start.
func watchResize(int, func(cols, rows int)) func() { return func() {} }
