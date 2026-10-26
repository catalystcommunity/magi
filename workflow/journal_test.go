package workflow

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	wire "github.com/catalystcommunity/csilgen/transports/go"
	p "github.com/catalystcommunity/magi/protocol"
)

func TestIntentValidation(t *testing.T) {
	r := api.SubmitKeyedTaskRequest{Guarded: true, Queue: "q", CurrentState: "ready", AutoTargetState: "ready", Timeout: -1, Payload: []byte("x")}
	a, e := normalize("installation-a", "step", "SubmitKeyedTask", api.EncodeSubmitKeyedTaskRequest(r))
	if e != nil {
		t.Fatal(e)
	}
	b, e := normalize("installation-a", "step", "SubmitKeyedTask", a.request)
	if e != nil || !bytes.Equal(a.request, b.request) {
		t.Fatal("unstable key")
	}
	c, e := normalize("installation-b", "step", "SubmitKeyedTask", api.EncodeSubmitKeyedTaskRequest(r))
	if e != nil || c.key == a.key {
		t.Fatal("installation collision")
	}
	for _, name := range []string{"legacy", "unguarded", "zero-timeout", "large-payload", "caller-key"} {
		t.Run(name, func(t *testing.T) {
			v := r
			op := "SubmitKeyedTask"
			switch name {
			case "legacy":
				op = "SubmitTask"
			case "unguarded":
				v.Guarded = false
			case "zero-timeout":
				v.Timeout = 0
			case "large-payload":
				v.Payload = make([]byte, 16385)
			case "caller-key":
				v.SubmissionKey = "foreign"
			}
			if _, e := normalize("a", "b", op, api.EncodeSubmitKeyedTaskRequest(v)); !errors.Is(e, ErrInvalid) {
				t.Fatal(e)
			}
		})
	}
	if _, e := normalize("a", "b", "CompleteGuardedTask", api.EncodeCompleteGuardedTaskRequest(api.CompleteGuardedTaskRequest{Uuid: "task", Queue: "q", ExpectedRevision: 1})); !errors.Is(e, ErrInvalid) {
		t.Fatal("missing state guard accepted")
	}
}

func TestForeignIntegerProfileDoesNotWeakenNative(t *testing.T) {
	raw := api.EncodeSubmissionReceipt(api.SubmissionReceipt{Queue: "q", SubmissionKey: "k", TaskUuid: "id", AcceptedAt: time.Now().UnixNano(), ExpiresAt: time.Now().Add(time.Hour).UnixNano(), Guarded: true})
	if p.CheckFrame(raw) == nil {
		t.Fatal("native precision bound was weakened")
	}
	if _, e := decode(raw, api.DecodeSubmissionReceipt, api.EncodeSubmissionReceipt); e != nil {
		t.Fatal(e)
	}
	for _, raw := range [][]byte{{0x9f, 0xff}, append(bytes.Repeat([]byte{0x81}, 26), 0x01), {0xa2, 0x61, 0x61, 0x01, 0x61, 0x61, 0x02}} {
		if p.CheckInteger64Frame(raw) == nil {
			t.Fatal("foreign bound missing")
		}
	}
}

func TestTransportDeadlinesAndGuards(t *testing.T) {
	for _, mode := range []string{"no-response", "oversize", "wrong-id", "wrong-variant", "noncanonical", "unsupported", "redirect-outside-seeds"} {
		t.Run(mode, func(t *testing.T) {
			listener, e := net.Listen("tcp4", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				var h [4]byte
				if _, e = io.ReadFull(conn, h[:]); e != nil {
					return
				}
				raw := make([]byte, binary.BigEndian.Uint32(h[:]))
				if _, e = io.ReadFull(conn, raw); e != nil {
					return
				}
				if mode == "no-response" {
					io.Copy(io.Discard, conn)
					return
				}
				if mode == "oversize" {
					binary.BigEndian.PutUint32(h[:], p.MaxFrame+1)
					conn.Write(h[:])
					return
				}
				r := wire.NewRpcResponseOk("GetServerInfoResponse", api.EncodeGetServerInfoResponse(api.GetServerInfoResponse{})).WithID(new(uint64(1)))
				switch mode {
				case "wrong-id":
					r.ID = new(uint64(2))
				case "wrong-variant":
					r.Variant = new("Unexpected")
				case "noncanonical":
					r.Payload = append(r.Payload, 0)
				case "unsupported":
					r = wire.NewRpcResponseTransportError(wire.StatusUnknownServiceOrOp, "unknown").WithID(new(uint64(1)))
				case "redirect-outside-seeds":
					r = wire.NewRpcResponseTransportError(wire.StatusInternal, "not-leader leader=untrusted.invalid:5080").WithID(new(uint64(1)))
				}
				raw, _ = r.Encode()
				binary.BigEndian.PutUint32(h[:], uint32(len(raw)))
				conn.Write(h[:])
				conn.Write(raw)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, e = (&Transport{Seeds: []string{listener.Addr().String()}}).Call(ctx, "CorndogsService", "GetServerInfo", api.EncodeGetServerInfoRequest(api.GetServerInfoRequest{}))
			if e == nil {
				t.Fatal("invalid reply accepted")
			}
			if time.Since(start) > time.Second {
				t.Fatal("unbounded call")
			}
			<-done
			if mode == "unsupported" && !errors.Is(e, api.ErrUnsupported) {
				t.Fatal(e)
			}
		})
	}
	_, e := (&Transport{Seeds: []string{"external.invalid:5080"}}).Call(context.Background(), "CorndogsService", "GetServerInfo", api.EncodeGetServerInfoRequest(api.GetServerInfoRequest{}))
	if !errors.Is(e, ErrInvalid) {
		t.Fatal("plaintext non-loopback permitted")
	}
}

func TestBlockedWriteCannotOutliveDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	tr := &Transport{Seeds: []string{"127.0.0.1:5080"}, dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := tr.Call(ctx, "CorndogsService", "GetServerInfo", api.EncodeGetServerInfoRequest(api.GetServerInfoRequest{}))
	if !errors.Is(e, api.ErrOutcomeUncertain) || time.Since(start) > time.Second {
		t.Fatalf("write deadline: %v", e)
	}
	server.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	n, e := server.Read(b[:])
	if n != 0 || e != io.EOF {
		t.Fatal("socket remained writable after return")
	}
}
