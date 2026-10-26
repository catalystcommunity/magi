// magi-source-stage is an operator command, not an agent tool or release service.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/catalystcommunity/magi/workshop"
	"io"
	"os"
)

func run() error {
	checkout := flag.String("checkout", ".", "source checkout to compare; never modified")
	parent := flag.String("output-parent", os.TempDir(), "existing directory for a new staged artifact")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("read the source bundle from stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 256*1024+1))
	if err != nil || len(raw) > 256*1024 {
		return fmt.Errorf("source bundle input limit")
	}
	var bundle workshop.SourceBundle
	if err = json.Unmarshal(raw, &bundle); err != nil {
		return fmt.Errorf("invalid source bundle")
	}
	path, err := workshop.StageSource(*checkout, *parent, bundle)
	if err != nil {
		if path != "" {
			fmt.Fprintln(os.Stderr, "Incomplete artifact retained:", path)
		}
		return fmt.Errorf("source staging failed: %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"path": path, "candidate_digest": bundle.CandidateDigest, "state": "staged", "build_performed": false, "deployment_authorized": false})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
