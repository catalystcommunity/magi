package linkkeys

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
)

// Serve answers CSIL-RPC Rp calls on l with impl. It exists for tests and
// local fakes: it lets them use the real pinned transport. Calls without
// apiKey in the envelope get status 16 (unauthenticated).
func Serve(ctx context.Context, l net.Listener, cert tls.Certificate, apiKey string, impl Rp) {
	tl := tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}})
	for {
		c, err := tl.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			frame, err := readFrame(c)
			if err != nil {
				return
			}
			writeFrame(c, serveOne(ctx, frame, apiKey, impl))
		}()
	}
}

// Fault is an error of an Rp implementation that the server sends as the
// RpError arm of the reply (status 0), as a LinkKeys server does: for
// example "the account is not an application account".
type Fault struct{ RpError }

func (f *Fault) Error() string { return "linkkeys rp: " + f.Message }

func serveOne(ctx context.Context, frame []byte, apiKey string, impl Rp) []byte {
	replyArm := func(status int64, msg, variant string, payload []byte) []byte {
		e := []cborEntry{
			{cborText("v"), cborUint(envelopeVersion)},
			{cborText("status"), cborInt(status)},
			{cborText("payload"), cborTag{num: 24, inner: cborBytes(payload)}},
		}
		if variant != "" {
			e = append(e, cborEntry{cborText("variant"), cborText(variant)})
		}
		if msg != "" {
			e = append(e, cborEntry{cborText("error"), cborText(msg)})
		}
		return cborEncode(canonical(e...))
	}
	reply := func(status int64, msg string, payload []byte) []byte { return replyArm(status, msg, "", payload) }
	v, err := cborDecode(frame)
	if err == nil {
		err = envelopeVersionOK(v)
	}
	if err != nil {
		return reply(1, err.Error(), nil)
	}
	text := func(k string) string {
		x, _ := cborMapGet(v, k)
		s, _ := cborAsText(x)
		return s
	}
	if text("auth") != apiKey {
		return reply(16, "unauthenticated", nil)
	}
	req, err := untag24(cborMapGet(v, "payload"))
	if err != nil {
		return reply(1, err.Error(), nil)
	}
	name := text("service") + "/" + text("op")
	out, err := dispatch(ctx, impl, name, req)
	var fault *Fault
	if errors.As(err, &fault) && arms[name] != "" {
		b, encErr := EncodeRpError(fault.RpError)
		if encErr != nil {
			return reply(13, encErr.Error(), nil)
		}
		return replyArm(0, "", "RpError", b)
	}
	if err != nil {
		return reply(13, err.Error(), nil)
	}
	return replyArm(0, "", arms[name], out)
}

// arms has the name of the success arm of each operation that also has an
// RpError arm. A reply of such an operation names its arm.
var arms = map[string]string{
	"Rp/sign-request":     "RpSignResponse",
	"Rp/decrypt-token":    "RpDecryptResponse",
	"Rp/verify-assertion": "RpVerifyResponse",
	"Rp/userinfo-fetch":   "UserInfo",
}

func dispatch(ctx context.Context, impl Rp, name string, b []byte) ([]byte, error) {
	switch name {
	case "Rp/sign-request":
		return call(ctx, b, DecodeRpSignRequest, impl.SignRequest, EncodeRpSignResponse)
	case "Rp/decrypt-token":
		return call(ctx, b, DecodeRpDecryptRequest, impl.DecryptToken, EncodeRpDecryptResponse)
	case "Rp/verify-assertion":
		return call(ctx, b, DecodeRpVerifyRequest, impl.VerifyAssertion, EncodeRpVerifyResponse)
	case "Rp/userinfo-fetch":
		return call(ctx, b, DecodeRpUserInfoRequest, impl.UserinfoFetch, EncodeUserInfo)
	case "Rp/resolve-domain-keys":
		return call(ctx, b, DecodeRpResolveDomainKeysRequest, impl.ResolveDomainKeys, EncodeRpResolveDomainKeysResponse)
	}
	return nil, errors.New("unknown operation " + name)
}

func call[Q, R any](ctx context.Context, b []byte, dec func([]byte) (Q, error), f func(context.Context, Q) (R, error), enc func(R) ([]byte, error)) ([]byte, error) {
	q, err := dec(b)
	if err != nil {
		return nil, err
	}
	r, err := f(ctx, q)
	if err != nil {
		return nil, err
	}
	return enc(r)
}
