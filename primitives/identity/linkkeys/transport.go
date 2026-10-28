package linkkeys

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// CSIL-RPC over TLS to one configured RP server. The server certificate is
// self-signed; the SHA-256 fingerprint of its raw Ed25519 public key must be
// in the pin set. The API key goes in the envelope "auth" field. Ported from
// linkkeys/sdks/regular-rp/go/transport.go. The envelope is the csilgen
// transport v1 layout: a canonical CBOR map {v, service, op, payload: tag 24,
// auth} with a 4-byte big-endian length prefix per frame.

const (
	envelopeVersion = 1
	maxFrame        = 1 << 20
)

// ServerError is a non-zero status in an RP response.
type ServerError struct {
	Status  int64
	Message string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("linkkeys rp status %d: %s", e.Status, e.Message)
}

// PinnedTransport implements Transport for one RP server.
type PinnedTransport struct {
	Address      string   // host:port of the RP CSIL-RPC TCP endpoint
	Fingerprints []string // pinned lowercase hex fingerprints
	APIKey       string
	Timeout      time.Duration // one deadline for connect, TLS, write, read
	Dial         func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Call sends one request on a fresh pinned TLS connection.
func (t *PinnedTransport) Call(ctx context.Context, service, op string, payload []byte) ([]byte, error) {
	if t.APIKey == "" || t.Address == "" || len(t.Fingerprints) == 0 {
		return nil, errors.New("linkkeys rp: address, fingerprints, and api key are required")
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dial := t.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	raw, err := dial(ctx, "tcp", t.Address)
	if err != nil {
		return nil, fmt.Errorf("linkkeys rp dial: %w", err)
	}
	defer raw.Close()
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	}
	host, _, err := net.SplitHostPort(t.Address)
	if err != nil {
		host = t.Address
	}
	conn := tls.Client(raw, &tls.Config{
		ServerName:            host,
		MinVersion:            tls.VersionTLS12,
		InsecureSkipVerify:    true, // VerifyPeerCertificate pins the key instead
		VerifyPeerCertificate: pinVerifier(t.Fingerprints),
	})
	if err := conn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("linkkeys rp tls: %w", err)
	}
	if err := writeFrame(conn, encodeRequest(service, op, payload, t.APIKey)); err != nil {
		return nil, fmt.Errorf("linkkeys rp write: %w", err)
	}
	frame, err := readFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("linkkeys rp read: %w", err)
	}
	return decodeResponse(frame)
}

func pinVerifier(pins []string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("no peer certificate")
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return err
		}
		if now := time.Now(); now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return errors.New("peer certificate is outside its validity period")
		}
		pub, ok := cert.PublicKey.(ed25519.PublicKey)
		if !ok {
			return errors.New("peer certificate is not Ed25519")
		}
		fp := Fingerprint(pub)
		for _, want := range pins {
			if strings.EqualFold(fp, want) {
				return nil
			}
		}
		return fmt.Errorf("peer fingerprint %s is not pinned", fp)
	}
}

// Fingerprint is lowercase hex SHA-256 of raw public key bytes.
func Fingerprint(pub []byte) string {
	s := sha256.Sum256(pub)
	return hex.EncodeToString(s[:])
}

// canonical sorts map entries by their encoded key bytes (RFC 8949 4.2.1).
func canonical(entries ...cborEntry) cborMap {
	m := cborMap(entries)
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && string(cborEncode(m[j].key)) < string(cborEncode(m[j-1].key)); j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
	return m
}

func encodeRequest(service, op string, payload []byte, auth string) []byte {
	e := []cborEntry{
		{cborText("v"), cborUint(envelopeVersion)},
		{cborText("service"), cborText(service)},
		{cborText("op"), cborText(op)},
		{cborText("payload"), cborTag{num: 24, inner: cborBytes(payload)}},
	}
	if auth != "" {
		e = append(e, cborEntry{cborText("auth"), cborText(auth)})
	}
	return cborEncode(canonical(e...))
}

func untag24(v cborValue, ok bool) ([]byte, error) {
	if !ok {
		return nil, errors.New("envelope: missing payload")
	}
	t, isTag := v.(cborTag)
	b, isBytes := t.inner.(cborBytes)
	if !isTag || t.num != 24 || !isBytes {
		return nil, errors.New("envelope: payload is not tag 24 bytes")
	}
	return []byte(b), nil
}

func envelopeVersionOK(v cborValue) error {
	ver, err := cborRequire(v, "v")
	if err != nil {
		return err
	}
	if n, err := cborAsU64(ver); err != nil || n != envelopeVersion {
		return errors.New("envelope: unsupported version")
	}
	return nil
}

func decodeResponse(frame []byte) ([]byte, error) {
	v, err := cborDecode(frame)
	if err != nil {
		return nil, err
	}
	if err := envelopeVersionOK(v); err != nil {
		return nil, err
	}
	sv, err := cborRequire(v, "status")
	if err != nil {
		return nil, err
	}
	status, err := cborAsI64(sv)
	if err != nil {
		return nil, err
	}
	if status != 0 {
		msg := ""
		if ev, ok := cborMapGet(v, "error"); ok {
			msg, _ = cborAsText(ev)
		}
		return nil, &ServerError{Status: status, Message: msg}
	}
	return untag24(cborMapGet(v, "payload"))
}

func writeFrame(w io.Writer, b []byte) error {
	if len(b) > maxFrame {
		return errors.New("frame too large")
	}
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], uint32(len(b)))
	if _, err := w.Write(append(p[:], b...)); err != nil {
		return err
	}
	return nil
}

func readFrame(r io.Reader) ([]byte, error) {
	var p [4]byte
	if _, err := io.ReadFull(r, p[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(p[:])
	if n > maxFrame {
		return nil, errors.New("frame too large")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
