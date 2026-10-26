package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/catalystcommunity/magi"
	wire "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/protocol"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: magi-probe https://host/csil/v1/rpc")
		os.Exit(2)
	}
	t, e := protocol.NewHTTPTransport(os.Args[1])
	if e != nil {
		panic(e)
	}
	client := wire.NewDiscoveryClient(t)
	d, e := client.Describe(context.Background(), wire.DescribeRequest{Versions: []string{magi.ProtocolVersion}, RequiredFeatures: []string{"strict-cbor"}})
	if e != nil {
		panic(e)
	}
	s, e := client.ReadSchema(context.Background(), wire.SchemaRequest{Schema: d.Schema})
	if e != nil {
		panic(e)
	}
	if !bytes.Equal(s.Descriptor, magi.Descriptor) {
		panic("deployed descriptor differs from this client")
	}
	fmt.Printf("protocol=%s operations=%v descriptor=matched\n", d.Protocol, d.ImplementedOperations)
}
