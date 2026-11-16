package linkkeys_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lk "github.com/catalystcommunity/magi/primitives/identity/linkkeys"
)

const pinTestKey = "pin-test-api-key"

// pinServer is a TLS server whose certificate can be swapped (a key rotation).
// It records the application bytes it receives after a completed handshake.
type pinServer struct {
	addr string
	cert atomic.Pointer[tls.Certificate]
	mu   sync.Mutex
	got  bytes.Buffer
}

func (s *pinServer) received() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.got.Bytes()...)
}

// newCert makes a self-signed Ed25519 certificate and returns it with its pin.
func newCert(t *testing.T, notBefore, notAfter time.Time) (tls.Certificate, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: notBefore, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, lk.Fingerprint(pub)
}

func validCert(t *testing.T) (tls.Certificate, string) {
	return newCert(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

func startPinServer(t *testing.T, c tls.Certificate) *pinServer {
	t.Helper()
	s := &pinServer{}
	s.cert.Store(&c)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.cert.Load(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s.addr = l.Addr().String()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if err := c.(*tls.Conn).Handshake(); err != nil {
					return
				}
				buf := make([]byte, 4096)
				c.SetReadDeadline(time.Now().Add(2 * time.Second))
				n, _ := c.Read(buf)
				s.mu.Lock()
				s.got.Write(buf[:n])
				s.mu.Unlock()
			}()
		}
	}()
	return s
}

func (s *pinServer) call(pins ...string) error {
	tr := &lk.PinnedTransport{Address: s.addr, Fingerprints: pins, APIKey: pinTestKey, Timeout: 5 * time.Second}
	_, err := tr.Call(context.Background(), "Rp", "sign-request", []byte{0xa0})
	return err
}

func wantRejected(t *testing.T, s *pinServer, pins ...string) {
	t.Helper()
	err := s.call(pins...)
	if err == nil || !strings.Contains(err.Error(), "linkkeys rp tls") {
		t.Fatalf("pins %q: err = %v, want TLS handshake failure", pins, err)
	}
	// Give the server a moment to (not) read; the key must never be sent.
	time.Sleep(50 * time.Millisecond)
	if got := s.received(); len(got) != 0 {
		t.Fatalf("server received %d bytes after a failed handshake", len(got))
	}
}

// wantAccepted: the handshake passes and the request (with the key) is written.
// The test server then closes without a reply, so a read error is expected.
func wantAccepted(t *testing.T, s *pinServer, pins ...string) {
	t.Helper()
	err := s.call(pins...)
	if err != nil && (strings.Contains(err.Error(), "linkkeys rp tls") || strings.Contains(err.Error(), "not pinned")) {
		t.Fatalf("pins %q: handshake rejected: %v", pins, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !bytes.Contains(s.received(), []byte(pinTestKey)) {
		if time.Now().After(deadline) {
			t.Fatalf("pins %q: server never received the request", pins)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPinEmptyList(t *testing.T) {
	c, _ := validCert(t)
	s := startPinServer(t, c)
	for _, pins := range [][]string{nil, {}} {
		err := s.call(pins...)
		if err == nil || !strings.Contains(err.Error(), "fingerprints") {
			t.Fatalf("pins %v: err = %v", pins, err)
		}
	}
	if got := s.received(); len(got) != 0 {
		t.Fatal("server received data with an empty pin list")
	}
}

func TestPinEmptyStringAndWrong(t *testing.T) {
	c, _ := validCert(t)
	s := startPinServer(t, c)
	wantRejected(t, s, "")
	wantRejected(t, s, strings.Repeat("0", 64))
	wantRejected(t, s, "zz", "not hex")
}

func TestPinCaseInsensitive(t *testing.T) {
	c, pin := validCert(t)
	s := startPinServer(t, c)
	wantAccepted(t, s, strings.ToUpper(pin))
}

func TestPinCertValidity(t *testing.T) {
	for name, tc := range map[string]struct{ from, to time.Time }{
		"expired": {time.Now().Add(-2 * time.Hour), time.Now().Add(-time.Hour)},
		"not yet": {time.Now().Add(time.Hour), time.Now().Add(2 * time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			c, pin := newCert(t, tc.from, tc.to)
			s := startPinServer(t, c)
			// The pin matches, but the certificate is outside its validity period.
			wantRejected(t, s, pin)
		})
	}
}

func TestPinRotation(t *testing.T) {
	oldCert, oldPin := validCert(t)
	newCert, newPin := validCert(t)
	s := startPinServer(t, oldCert)

	wantAccepted(t, s, oldPin) // before rotation

	s.cert.Store(&newCert) // server key rotates
	s.mu.Lock()
	s.got.Reset()
	s.mu.Unlock()

	wantRejected(t, s, oldPin)         // old pin alone fails after rotation
	wantAccepted(t, s, oldPin, newPin) // overlap: both listed
	s.mu.Lock()
	s.got.Reset()
	s.mu.Unlock()
	wantAccepted(t, s, newPin) // new pin only
}

func TestLoadRuntimePins(t *testing.T) {
	good := strings.Repeat("ab", 32)
	load := func(pin string) error {
		p := filepath.Join(t.TempDir(), "rt.json")
		body := `{"rp_domain":"example.local","tcp_address":"rp.example.local:4987","fingerprints":["` + pin + `"],"api_key":"k"}`
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := lk.LoadRuntime(p)
		return err
	}
	for _, pin := range []string{good, strings.ToUpper(good)} {
		if err := load(pin); err != nil {
			t.Errorf("valid pin %q rejected: %v", pin, err)
		}
	}
	for name, pin := range map[string]string{
		"empty":     "",
		"short":     "abcd",
		"non-hex":   strings.Repeat("zz", 32),
		"long":      good + "00",
		"odd-space": " " + good[1:],
	} {
		if err := load(pin); err == nil {
			t.Errorf("%s pin accepted", name)
		}
	}
}
