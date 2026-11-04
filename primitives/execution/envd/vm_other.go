//go:build !linux && !darwin

package envd

import (
	"errors"
	"log/slog"
)

func vmInit(*slog.Logger) ([]string, error) {
	return nil, errors.New("-vm: task VM guests run Linux")
}

func startTransparent(string, string, []string, *slog.Logger) error {
	return errors.New("-transparent: task VM guests run Linux")
}

func netnsExec([]string) error { return errors.New("network namespaces are on Linux only") }

func podnetExec([]string) error { return errors.New("Pods run Linux") }
