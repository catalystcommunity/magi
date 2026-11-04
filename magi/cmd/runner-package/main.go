// Command runner-package builds the runner packages of a repository:
// magi-runner-<os>-<arch>.tar.gz for each platform in runner.Platforms.
//
//	go run ./cmd/runner-package -src .. -out OUTDIR [-platforms linux/arm64,...]
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/catalystcommunity/magi/magi/runner"
)

func main() {
	src := flag.String("src", "..", "repository root")
	out := flag.String("out", "", "output directory")
	platforms := flag.String("platforms", strings.Join(runner.Platforms, ","), "comma-separated os/arch list")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "runner-package: -out is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "runner-package:", err)
		os.Exit(1)
	}
	for _, p := range strings.Split(*platforms, ",") {
		goos, arch, ok := strings.Cut(strings.TrimSpace(p), "/")
		if !ok {
			fmt.Fprintf(os.Stderr, "runner-package: bad platform %q\n", p)
			os.Exit(2)
		}
		b, err := runner.PackBytes(*src, goos, arch)
		if err != nil {
			fmt.Fprintln(os.Stderr, "runner-package:", err)
			os.Exit(1)
		}
		f := filepath.Join(*out, fmt.Sprintf("magi-runner-%s-%s.tar.gz", goos, arch))
		if err := os.WriteFile(f, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "runner-package:", err)
			os.Exit(1)
		}
		sum := sha256.Sum256(b)
		fmt.Printf("%s/%s sha256:%s %s\n", goos, arch, hex.EncodeToString(sum[:]), f)
	}
}
