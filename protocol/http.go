package protocol

import (
	"bytes"
	"context"
	"fmt"
	transport "github.com/catalystcommunity/csilgen/transports/go"
	wire "github.com/catalystcommunity/magi/generated/go"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

type HTTPTransport struct {
	endpoint string
	client   *http.Client
	types    map[string]ResponseType
}

type ResponseType struct {
	Variant  string
	Validate func([]byte) error
}

// RemoteError retains retry policy and remains compatible with generated clients.
type RemoteError struct{ Detail wire.ServiceError }

func (e *RemoteError) Error() string { return e.Detail.Message }
func (e *RemoteError) Unwrap() error {
	return &wire.ClientError{Code: e.Detail.Code, Message: e.Detail.Message}
}

// NewComponentTransport binds the carrier to a component's response contract.
func NewComponentTransport(endpoint string, types map[string]ResponseType) (*HTTPTransport, error) {
	t, e := NewHTTPTransport(endpoint)
	if e != nil {
		return nil, e
	}
	t.types = make(map[string]ResponseType, len(types))
	for k, v := range types {
		if v.Variant == "" || v.Validate == nil {
			return nil, ErrWire
		}
		t.types[k] = v
	}
	return t, nil
}

func NewHTTPTransport(endpoint string) (*HTTPTransport, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrWire
	}
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1")) {
		return nil, ErrWire
	}
	return &HTTPTransport{endpoint: endpoint, client: &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (t *HTTPTransport) Call(ctx context.Context, service, op string, payload []byte) ([]byte, error) {
	if err := CheckFrame(payload); err != nil {
		return nil, err
	}
	raw, e := transport.NewRpcRequest(service, op, payload).WithID(1).Encode()
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", t.endpoint, bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/cbor")
	resp, e := t.client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("RPC connection failed")
	}
	defer resp.Body.Close()
	media, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != 200 || e != nil || media != "application/cbor" {
		return nil, fmt.Errorf("RPC carrier refused response")
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, MaxFrame+1))
	if e != nil {
		return nil, ErrWire
	}
	r, e := Decode(b, transport.DecodeRpcResponse, func(v transport.RpcResponse) []byte { b, _ := v.Encode(); return b })
	if e != nil {
		return nil, e
	}
	if r.ID == nil || *r.ID != 1 {
		return nil, ErrWire
	}
	if !r.Status.IsOk() {
		return nil, fmt.Errorf("RPC transport status %d", r.Status.Code())
	}
	if r.Variant == nil {
		return nil, ErrWire
	}
	if *r.Variant == "ServiceError" {
		v, e := Decode(r.Payload, wire.DecodeServiceError, wire.EncodeServiceError)
		if e != nil {
			return nil, e
		}
		if len(v.Message) > 256 {
			return nil, ErrWire
		}
		return nil, &RemoteError{Detail: v}
	}
	// Discovery is the only implemented service. Do not guess output variants.
	if t.types != nil {
		shape, ok := t.types[service+"/"+op]
		if !ok || *r.Variant != shape.Variant {
			return nil, ErrWire
		}
		if e := shape.Validate(r.Payload); e != nil {
			return nil, e
		}
		return bytes.Clone(r.Payload), nil
	}
	expected := map[string]string{"Discovery/describe": "ServiceDescription", "Discovery/read-schema": "SchemaDocument"}[service+"/"+op]
	if expected == "" || *r.Variant != expected {
		return nil, ErrWire
	}
	if e := CheckFrame(r.Payload); e != nil {
		return nil, e
	}
	if expected == "ServiceDescription" {
		if _, e := Decode(r.Payload, wire.DecodeServiceDescription, wire.EncodeServiceDescription); e != nil {
			return nil, e
		}
	} else {
		if _, e := Decode(r.Payload, wire.DecodeSchemaDocument, wire.EncodeSchemaDocument); e != nil {
			return nil, e
		}
	}
	return bytes.Clone(r.Payload), nil
}
