package envd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// vmInit runs in a macOS task VM: launchd starts envd at boot (the boot
// script of the template). The shares come up after boot, so envd waits
// for its configuration. A macOS guest has no transparent route: tools use
// the proxy port.
func vmInit(log *slog.Logger) ([]string, error) {
	path := MacShares + "/state/vm.json"
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("-vm: %s did not appear", path)
		}
		time.Sleep(time.Second)
	}
	log.Info("macOS task VM", "config", path)
	return readVMConfigAt(path)
}

func startTransparent(string, string, []string, *slog.Logger) error {
	return errors.New("-transparent: macOS guests have no transparent route; use the proxy port")
}

func netnsExec([]string) error { return errors.New("network namespaces are on Linux only") }

func podnetExec([]string) error { return errors.New("Pods run Linux") }
