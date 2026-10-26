// Package workflow journals Corndogs operations. It does not own task state.
package workflow

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	api "github.com/CatalystCommunity/corndogs/clients/corndogs"
	wire "github.com/catalystcommunity/csilgen/transports/go"
	p "github.com/catalystcommunity/magi/protocol"
)

// Transport uses a new bounded connection per call. Only configured seeds are
// used, including after a leader redirect. There is no uncertain write replay.
type Transport struct {
	Seeds []string
	TLS   *tls.Config
	dial  func(context.Context, string, string) (net.Conn, error)
}

var responseShapes = map[string]func([]byte) error{
	"GetServerInfo":       shape(api.DecodeGetServerInfoResponse, api.EncodeGetServerInfoResponse),
	"SubmitKeyedTask":     shape(api.DecodeSubmitKeyedTaskResponse, api.EncodeSubmitKeyedTaskResponse),
	"LookupSubmission":    shape(api.DecodeLookupSubmissionResponse, api.EncodeLookupSubmissionResponse),
	"ClaimGuardedTask":    shape(api.DecodeClaimGuardedTaskResponse, api.EncodeClaimGuardedTaskResponse),
	"UpdateGuardedTask":   shape(api.DecodeUpdateGuardedTaskResponse, api.EncodeUpdateGuardedTaskResponse),
	"CompleteGuardedTask": shape(api.DecodeCompleteGuardedTaskResponse, api.EncodeCompleteGuardedTaskResponse),
	"CancelGuardedTask":   shape(api.DecodeCancelGuardedTaskResponse, api.EncodeCancelGuardedTaskResponse),
	"GetGuardedTask":      shape(api.DecodeGetGuardedTaskResponse, api.EncodeGetGuardedTaskResponse),
	"LookupOperation":     shape(api.DecodeLookupOperationResponse, api.EncodeLookupOperationResponse),
	"CleanUpTimedOut":     shape(api.DecodeCleanUpTimedOutResponse, api.EncodeCleanUpTimedOutResponse),
}

func decode[T any](b []byte, read func([]byte) (T, error), write func(T) []byte) (T, error) {
	var zero T
	if p.CheckInteger64Frame(b) != nil {
		return zero, p.ErrWire
	}
	v, e := read(b)
	if e != nil || !bytes.Equal(b, write(v)) {
		return zero, p.ErrWire
	}
	return v, nil
}
func shape[T any](read func([]byte) (T, error), write func(T) []byte) func([]byte) error {
	return func(b []byte) error { _, e := decode(b, read, write); return e }
}

func (t *Transport) Call(ctx context.Context, service, op string, request []byte) ([]byte, error) {
	validate, ok := responseShapes[op]
	if !ok || service != "CorndogsService" || len(t.Seeds) < 1 || len(t.Seeds) > 8 || p.CheckInteger64Frame(request) != nil {
		return nil, ErrInvalid
	}
	for _, seed := range t.Seeds {
		host, _, e := net.SplitHostPort(seed)
		if e != nil || t.TLS == nil && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
			return nil, ErrInvalid
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var last error
	for _, seed := range t.Seeds {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		raw, e := t.call(ctx, seed, service, op, request, validate)
		if e == nil {
			return raw, nil
		}
		last = e
		if !errors.Is(e, api.ErrNotApplied) || errors.Is(e, api.ErrUnsupported) {
			return nil, e
		}
	}
	return nil, last
}
func (t *Transport) call(ctx context.Context, seed, service, op string, request []byte, validate func([]byte) error) ([]byte, error) {
	dial := t.dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, e := dial(ctx, "tcp", seed)
	if e != nil {
		return nil, fmt.Errorf("%w: connection unavailable", api.ErrNotApplied)
	}
	defer conn.Close()
	// Capture the raw socket before wrapping it. Cancellation must not race
	// with reassignment of conn during a TLS handshake.
	socket := conn
	stop := context.AfterFunc(ctx, func() { socket.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if e = conn.SetDeadline(deadline); e != nil {
		return nil, e
	}
	if t.TLS != nil {
		cfg := t.TLS.Clone()
		if cfg.InsecureSkipVerify {
			return nil, ErrInvalid
		}
		if cfg.ServerName == "" {
			cfg.ServerName, _, _ = net.SplitHostPort(seed)
		}
		if cfg.MinVersion < tls.VersionTLS12 {
			cfg.MinVersion = tls.VersionTLS12
		}
		secure := tls.Client(conn, cfg)
		if e = secure.HandshakeContext(ctx); e != nil {
			return nil, fmt.Errorf("%w: TLS verification failed", api.ErrNotApplied)
		}
		conn = secure
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	raw, e := wire.NewRpcRequest(service, op, request).WithID(1).Encode()
	if e != nil || len(raw) > p.MaxFrame {
		return nil, ErrInvalid
	}
	frame := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(frame, uint32(len(raw)))
	copy(frame[4:], raw)
	if _, e = io.Copy(conn, bytes.NewReader(frame)); e != nil {
		return nil, api.ErrOutcomeUncertain
	}
	var header [4]byte
	if _, e = io.ReadFull(conn, header[:]); e != nil {
		return nil, api.ErrOutcomeUncertain
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > p.MaxFrame {
		return nil, api.ErrOutcomeUncertain
	}
	reply := make([]byte, n)
	if _, e = io.ReadFull(conn, reply); e != nil {
		return nil, api.ErrOutcomeUncertain
	}
	r, e := p.Decode(reply, wire.DecodeRpcResponse, func(v wire.RpcResponse) []byte { b, _ := v.Encode(); return b })
	if e != nil || r.ID == nil || *r.ID != 1 {
		return nil, api.ErrOutcomeUncertain
	}
	if !r.Status.IsOk() {
		msg := ""
		if r.Error != nil && len(*r.Error) <= 512 {
			msg = *r.Error
		}
		return nil, &api.TransportStatusError{Status: r.Status.Code(), Message: msg}
	}
	if r.Variant == nil {
		return nil, api.ErrOutcomeUncertain
	}
	if *r.Variant == "ServiceError" {
		v, e := decode(r.Payload, api.DecodeServiceError, api.EncodeServiceError)
		if e != nil {
			return nil, api.ErrOutcomeUncertain
		}
		return nil, &v
	}
	if *r.Variant != op+"Response" || validate(r.Payload) != nil {
		return nil, api.ErrOutcomeUncertain
	}
	return bytes.Clone(r.Payload), nil
}
