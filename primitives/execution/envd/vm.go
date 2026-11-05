package envd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A task VM has no network card. envd is its init (see vm_linux.go): it
// sends each TCP connection of the guest through vsock to the runner,
// which sends it to the egress proxy. The guest gets names from envd:
// each name gets an address in FakeNet, so that the proxy gets the name
// of each connection, not an address.

// VMConfig is the file that the host writes for envd in a task VM: the
// envd flags and the environment of the commands.
const VMConfig = "/run/magi/state/vm.json"

// VMConfigFile is the content of VMConfig.
type VMConfigFile struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

// MacShares is where a macOS guest sees the shares of magi-vz.
const MacShares = "/Volumes/My Shared Files"

// FakeNet holds the addresses that the guest DNS gives to names.
var FakeNet = netip.MustParsePrefix("198.18.0.0/15")

// listenSpec listens on "unix:PATH", "vsock:PORT", or a TCP address. It
// returns the listener and the address for the addr file.
func listenSpec(spec string) (net.Listener, string, error) {
	if path, ok := strings.CutPrefix(spec, "unix:"); ok {
		os.Remove(path)
		ln, err := net.Listen("unix", path)
		return ln, spec, err
	}
	if p, ok := strings.CutPrefix(spec, "vsock:"); ok {
		port, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, "", fmt.Errorf("listen %q: %w", spec, err)
		}
		ln, err := vsockListen(uint32(port))
		return ln, spec, err
	}
	ln, err := net.Listen("tcp", spec)
	if err != nil {
		return nil, "", err
	}
	return ln, ln.Addr().String(), nil
}

// VMTransparentPort is the guest port that the firewall sends TCP
// connections to.
const VMTransparentPort = 15001

// PodDNSPort is the port of the DNS of envd in a Pod (envd podnet).
const PodDNSPort = 15053

// dialSpec returns a dial function for "vsock:CID:PORT", "tcp:HOST:PORT",
// or a unix socket path.
func dialSpec(spec string) (func() (net.Conn, error), error) {
	if v, ok := strings.CutPrefix(spec, "vsock:"); ok {
		c, p, ok := strings.Cut(v, ":")
		cid, err1 := strconv.ParseUint(c, 10, 32)
		port, err2 := strconv.ParseUint(p, 10, 32)
		if !ok || err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%q: expected vsock:CID:PORT", spec)
		}
		return func() (net.Conn, error) { return vsockDial(uint32(cid), uint32(port)) }, nil
	}
	if addr, ok := strings.CutPrefix(spec, "tcp:"); ok {
		return func() (net.Conn, error) { return net.DialTimeout("tcp", addr, 20*time.Second) }, nil
	}
	return func() (net.Conn, error) { return net.Dial("unix", spec) }, nil
}

// fakeDNS answers A queries with addresses in FakeNet and remembers the
// name of each address. It answers AAAA with no address, so that clients
// use IPv4. The egress proxy resolves the names of connections. Other
// query types go to the proxy (forward).
type fakeDNS struct {
	// forward sends a query of another type (TXT, SRV, ...) to the egress
	// proxy, which answers it by its policy. nil refuses such queries.
	forward func(q []byte) ([]byte, error)
	// search are the search domains of the resolver (in a Pod, the
	// cluster domains). A name below one is the resolver trying a search
	// domain, not a destination: it gets "no such name".
	search []string

	mu     sync.Mutex
	next   netip.Addr
	byName map[string]netip.Addr
	byAddr map[netip.Addr]string
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{next: FakeNet.Addr().Next(), byName: map[string]netip.Addr{}, byAddr: map[netip.Addr]string{}}
}

func (f *fakeDNS) addr(name string) (netip.Addr, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.byName[name]; ok {
		return a, true
	}
	if !FakeNet.Contains(f.next) {
		return netip.Addr{}, false
	}
	a := f.next
	f.next = a.Next()
	f.byName[name], f.byAddr[a] = a, name
	return a, true
}

// Name returns the name of a fake address.
func (f *fakeDNS) Name(a netip.Addr) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.byAddr[a.Unmap()]
	return n, ok
}

// answer returns the answer to one DNS message.
func (f *fakeDNS) answer(q []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return nil, err
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionDesired: h.RecursionDesired,
		RecursionAvailable: true, OpCode: h.OpCode})
	b.EnableCompression()
	if len(qs) != 1 || h.OpCode != 0 {
		b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeNotImplemented})
		return b.Finish()
	}
	qq := qs[0]
	b.StartQuestions()
	b.Question(qq)
	b.StartAnswers()
	name := strings.ToLower(strings.TrimSuffix(qq.Name.String(), "."))
	for _, d := range f.search {
		if strings.HasSuffix(name, "."+d) {
			b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RecursionAvailable: true, RCode: dnsmessage.RCodeNameError})
			b.StartQuestions()
			b.Question(qq)
			return b.Finish()
		}
	}
	// "localhost" and names below it are this machine (RFC 6761, 6.3):
	// loopback, never a destination through the proxy.
	local := name == "localhost" || strings.HasSuffix(name, ".localhost")
	switch {
	case local && qq.Type == dnsmessage.TypeA:
		b.AResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
			dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}})
		return b.Finish()
	case local && qq.Type == dnsmessage.TypeAAAA:
		b.AAAAResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
			dnsmessage.AAAAResource{AAAA: netip.IPv6Loopback().As16()})
		return b.Finish()
	case local:
		return b.Finish() // no other records
	}
	switch qq.Type {
	case dnsmessage.TypeA:
		if a, ok := f.addr(name); ok {
			b.AResource(dnsmessage.ResourceHeader{Name: qq.Name, Class: dnsmessage.ClassINET, TTL: 600},
				dnsmessage.AResource{A: a.As4()})
		}
	case dnsmessage.TypeAAAA:
		// No IPv6 address: the client uses the A answer.
	default:
		if f.forward != nil {
			if out, err := f.forward(q); err == nil {
				return out, nil
			}
		}
		b = dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeRefused})
		b.StartQuestions()
		b.Question(qq)
	}
	return b.Finish()
}

// searchDomains returns the search domains in the resolv.conf at path.
func searchDomains(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 && (f[0] == "search" || f[0] == "domain") {
			for _, d := range f[1:] {
				if d = strings.ToLower(strings.Trim(d, ".")); d != "" {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// serveDNS serves DNS on UDP and TCP at addr.
func (f *fakeDNS) serve(addr string, log *slog.Logger) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		pc.Close()
		return err
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				log.Error("dns", "err", err)
				return
			}
			if out, err := f.answer(buf[:n]); err == nil {
				pc.WriteTo(out, from)
			}
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				r := bufio.NewReader(c)
				for {
					var n uint16
					if binary.Read(r, binary.BigEndian, &n) != nil {
						return
					}
					q := make([]byte, n)
					if _, err := ioReadFull(r, q); err != nil {
						return
					}
					out, err := f.answer(q)
					if err != nil {
						return
					}
					c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(out))))
					c.Write(out)
				}
			}()
		}
	}()
	return nil
}

func ioReadFull(r *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// proxyConnect opens a tunnel to dest ("host:port") through the egress
// proxy with an HTTP CONNECT request. proxy has the user and the
// credential of the environment.
func proxyConnect(dial func() (net.Conn, error), proxy *url.URL, dest string) (net.Conn, error) {
	c, err := dial()
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	req := "CONNECT " + dest + " HTTP/1.1\r\nHost: " + dest + "\r\n"
	if u := proxy.User; u != nil {
		pw, _ := u.Password()
		req += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pw)) + "\r\n"
	}
	if _, err := c.Write([]byte(req + "\r\n")); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("proxy: %s", res.Status)
	}
	c.SetDeadline(time.Time{})
	return &bufferedConn{Conn: c, r: br}, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// ProxyDNS is the CONNECT destination of the DNS service of the egress
// proxy: DNS over TCP.
const ProxyDNS = "magi-dns.internal:53"

// dnsThroughProxy sends one DNS query to the egress proxy.
func dnsThroughProxy(dial func() (net.Conn, error), proxy *url.URL, q []byte) ([]byte, error) {
	c, err := proxyConnect(dial, proxy, ProxyDNS)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(q))), q...)); err != nil {
		return nil, err
	}
	var n uint16
	if err := binary.Read(c, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(c, out); err != nil {
		return nil, err
	}
	return out, nil
}

// transparent serves the connections that the guest firewall redirects to
// ln: it finds the destination, then copies the connection through the
// proxy. A destination in FakeNet becomes its name.
func transparent(ln net.Listener, origDst func(net.Conn) (netip.AddrPort, error), names *fakeDNS,
	dial func() (net.Conn, error), proxy *url.URL, log *slog.Logger) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Error("transparent", "err", err)
			return
		}
		go func() {
			defer c.Close()
			dst, err := origDst(c)
			if err != nil {
				log.Warn("transparent", "err", err)
				return
			}
			host := dst.Addr().Unmap().String()
			if n, ok := names.Name(dst.Addr()); ok {
				host = n
			} else if FakeNet.Contains(dst.Addr().Unmap()) {
				log.Warn("transparent: unknown fake address", "addr", dst)
				return
			}
			dest := net.JoinHostPort(host, strconv.Itoa(int(dst.Port())))
			up, err := proxyConnect(dial, proxy, dest)
			if err != nil {
				log.Warn("transparent", "dest", dest, "err", err)
				return
			}
			defer up.Close()
			Splice(c, up)
		}()
	}
}

// readVMConfig reads VMConfig and sets the environment that it has.
func readVMConfig() ([]string, error) { return readVMConfigAt(VMConfig) }

func readVMConfigAt(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c VMConfigFile
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	os.Clearenv()
	for k, v := range c.Env {
		os.Setenv(k, v)
	}
	if len(c.Args) == 0 {
		return nil, errors.New(path + ": no args")
	}
	return c.Args, nil
}

// parsePfState finds the destination of the connection from peer in the
// state list of pfctl on macOS: "ALL tcp 127.0.0.1:15001 <- DEST <- PEER".
func parsePfState(out []byte, peer netip.AddrPort) (netip.AddrPort, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 7 && f[1] == "tcp" && f[3] == "<-" && f[5] == "<-" && f[6] == peer.String() {
			return netip.ParseAddrPort(f[4])
		}
	}
	return netip.AddrPort{}, fmt.Errorf("no pf state for %s", peer)
}
