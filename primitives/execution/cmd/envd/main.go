// Command envd is the in-environment agent. A container entrypoint, a VM
// init system, or a host service starts it. Build it static:
//
//	CGO_ENABLED=0 go build ./cmd/envd
package main

import (
	"os"

	"github.com/catalystcommunity/magi/primitives/execution/envd"
)

func main() { envd.Main(os.Args[1:]) }
