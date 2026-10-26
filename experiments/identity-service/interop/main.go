package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"time"

	wire "opal.local/experiments/identity-service/generated/go"
)

func main() {
	if len(os.Args) != 3 {
		panic("expected kind and hex")
	}
	b, err := hex.DecodeString(os.Args[2])
	if err != nil {
		panic(err)
	}
	var out []byte
	switch os.Args[1] {
	case "allowance":
		v, e := wire.DecodeAllowance(b)
		if e != nil {
			panic(e)
		}
		out = wire.EncodeAllowance(v)
	case "approval":
		v, e := wire.DecodeApproval(b)
		if e != nil {
			panic(e)
		}
		out = wire.EncodeApproval(v)
	case "signed":
		v, e := wire.DecodeSignedApproval(b)
		if e != nil {
			panic(e)
		}
		out = wire.EncodeSignedApproval(v)
	case "time":
		v, e := wire.DecodeAllowance(b)
		if e != nil {
			panic(e)
		}
		v.NotBefore = time.Date(2026, 1, 2, 3, 4, 5, 120000000, time.UTC)
		out = wire.EncodeAllowance(v)
	default:
		panic("unsupported kind")
	}
	if err != nil {
		panic(err)
	}
	fmt.Print(hex.EncodeToString(out))
}
