package envd

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func query(t *testing.T, f *fakeDNS, name string, typ dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET})
	q, _ := b.Finish()
	out, err := f.answer(q)
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(out); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFakeDNS(t *testing.T) {
	f := newFakeDNS()
	m := query(t, f, "Example.org.", dnsmessage.TypeA)
	if m.Header.ID != 7 || len(m.Answers) != 1 {
		t.Fatalf("A answer: %+v", m)
	}
	a := m.Answers[0].Body.(*dnsmessage.AResource).A
	if a != [4]byte{198, 18, 0, 1} {
		t.Fatalf("address %v", a)
	}
	if m := query(t, f, "example.org.", dnsmessage.TypeA); m.Answers[0].Body.(*dnsmessage.AResource).A != a {
		t.Fatal("a name must keep its address")
	}
	if m := query(t, f, "other.example.", dnsmessage.TypeA); m.Answers[0].Body.(*dnsmessage.AResource).A == a {
		t.Fatal("two names got one address")
	}
	if n, ok := f.Name(netipFrom4(a)); !ok || n != "example.org" {
		t.Fatalf("name of %v: %q", a, n)
	}
	if m := query(t, f, "example.org.", dnsmessage.TypeAAAA); m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
		t.Fatalf("AAAA: %+v", m)
	}
	if m := query(t, f, "_linkkeys.example.org.", dnsmessage.TypeTXT); m.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("TXT: %+v", m)
	}
	// localhost is loopback, not a destination (a test server in the task).
	for _, n := range []string{"localhost.", "api.localhost."} {
		m := query(t, f, n, dnsmessage.TypeA)
		if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{127, 0, 0, 1} {
			t.Fatalf("%s A: %+v", n, m)
		}
		m = query(t, f, n, dnsmessage.TypeAAAA)
		if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AAAAResource).AAAA != netip.IPv6Loopback().As16() {
			t.Fatalf("%s AAAA: %+v", n, m)
		}
		if m := query(t, f, n, dnsmessage.TypeTXT); m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
			t.Fatalf("%s TXT: %+v", n, m)
		}
	}
	// In a Pod the resolver tries the cluster search domains first.
	dir := t.TempDir()
	rc := filepath.Join(dir, "resolv.conf")
	os.WriteFile(rc, []byte("search magi-envs.svc.cluster.local svc.cluster.local cluster.local.\nnameserver 10.43.0.10\noptions ndots:5\n"), 0o644)
	f.search = searchDomains(rc)
	if m := query(t, f, "pypi.org.magi-envs.svc.cluster.local.", dnsmessage.TypeA); m.RCode != dnsmessage.RCodeNameError || len(m.Answers) != 0 {
		t.Fatalf("search domain: %+v", m)
	}
	if m := query(t, f, "pypi.org.", dnsmessage.TypeA); len(m.Answers) != 1 {
		t.Fatalf("after the search domains: %+v", m)
	}
}

func TestProxyConnect(t *testing.T) {
	a, b := net.Pipe()
	go func() {
		defer b.Close()
		req, err := http.ReadRequest(bufio.NewReader(b))
		if err != nil || req.Method != http.MethodConnect || req.RequestURI != "example.org:22" ||
			req.Header.Get("Proxy-Authorization") != "Basic ZW52OmNyZWQ=" {
			b.Write([]byte("HTTP/1.1 400 Bad\r\n\r\n"))
			return
		}
		b.Write([]byte("HTTP/1.1 200 OK\r\n\r\nSSH-2.0-test\r\n"))
	}()
	u, _ := url.Parse("http://env:cred@127.0.0.1:3128")
	c, err := proxyConnect(func() (net.Conn, error) { return a, nil }, u, "example.org:22")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(c)
	if string(got) != "SSH-2.0-test\r\n" {
		t.Fatalf("stream %q", got)
	}
	a2, b2 := net.Pipe()
	go func() {
		http.ReadRequest(bufio.NewReader(b2))
		b2.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
	}()
	if _, err := proxyConnect(func() (net.Conn, error) { return a2, nil }, u, "example.org:22"); err == nil {
		t.Fatal("a refused CONNECT must fail")
	}
}

func netipFrom4(a [4]byte) netip.Addr { return netip.AddrFrom4(a) }

func TestDNSThroughProxy(t *testing.T) {
	a, b := net.Pipe()
	go func() {
		defer b.Close()
		br := bufio.NewReader(b)
		req, err := http.ReadRequest(br)
		if err != nil || req.RequestURI != ProxyDNS {
			return
		}
		b.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
		var n [2]byte
		io.ReadFull(br, n[:])
		q := make([]byte, int(n[0])<<8|int(n[1]))
		io.ReadFull(br, q)
		q[2] |= 0x80
		b.Write(append([]byte{n[0], n[1]}, q...))
	}()
	u, _ := url.Parse("http://env:cred@127.0.0.1:3128")
	f := newFakeDNS()
	f.forward = func(q []byte) ([]byte, error) {
		return dnsThroughProxy(func() (net.Conn, error) { return a, nil }, u, q)
	}
	m := query(t, f, "_linkkeys.example.org.", dnsmessage.TypeTXT)
	if !m.Header.Response || m.Header.ID != 7 || m.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("TXT through the proxy: %+v", m)
	}
}

func TestParsePfState(t *testing.T) {
	out := []byte("No ALTQ support in kernel\nALL tcp 127.0.0.1:15001 <- 93.184.216.34:8080 <- 127.0.0.1:49152       ESTABLISHED:ESTABLISHED\n" +
		"ALL tcp 127.0.0.1:15001 <- 203.0.113.7:443 <- 127.0.0.1:49153       FIN_WAIT_2:FIN_WAIT_2\n")
	got, err := parsePfState(out, netip.MustParseAddrPort("127.0.0.1:49153"))
	if err != nil || got != netip.MustParseAddrPort("203.0.113.7:443") {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parsePfState(out, netip.MustParseAddrPort("127.0.0.1:1")); err == nil {
		t.Fatal("no state")
	}
}
