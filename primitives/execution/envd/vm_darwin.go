package envd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// vmInit runs in a macOS task VM: launchd starts envd at boot (the boot
// script of the template). The shares come up after boot, so envd waits
// for its configuration.
func vmInit(log *slog.Logger) ([]string, error) {
	path := MacShares + "/state/vm.json"
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("-vm: %s did not appear", path)
		}
		time.Sleep(time.Second)
	}
	log.Info("macOS task VM", "config", path)
	return readVMConfigAt(path)
}

// The transparent route of a macOS guest (no network card). envd runs as
// root (a launchd daemon), and sets:
//   - routes 0/1 and 128/1 on lo0, so that a connection to any address
//     leaves the program;
//   - a pf rule (anchor com.apple/250.magi) that sends each TCP
//     connection that does not go to 127/8 to VMTransparentPort;
//   - a resolver for all domains at the DNS of envd (scutil: a service
//     "magi" with SupplementalMatchDomains ""), because programs on macOS
//     ask mDNSResponder, not /etc/resolv.conf.
//
// envd finds the destination before the redirect in the pf state.
const macAnchor = "com.apple/250.magi"

func startTransparent(upstream, proxyURL string, dns []string, log *slog.Logger) error {
	dial, err := dialSpec(upstream)
	if err != nil {
		return err
	}
	pu, err := url.Parse(proxyURL)
	if err != nil || pu.Host == "" {
		return fmt.Errorf("-transparent: the proxy URL (HTTPS_PROXY) is not valid")
	}
	names := newFakeDNS()
	names.forward = func(q []byte) ([]byte, error) { return dnsThroughProxy(dial, pu, q) }
	var servers []string
	for _, a := range dns {
		if err := names.serve(a, log); err != nil {
			return err
		}
		if h, _, err := net.SplitHostPort(a); err == nil {
			servers = append(servers, h)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(VMTransparentPort))
	if err != nil {
		return err
	}
	if err := macNetwork(servers); err != nil {
		ln.Close()
		return err
	}
	// The SSH host key of the proxy, for the ssh_hosts of the proxy.
	if kh, err := os.ReadFile(MacShares + "/magi/egress/ssh_known_hosts"); err == nil {
		if err := os.WriteFile("/etc/ssh/ssh_known_hosts", kh, 0o644); err != nil {
			log.Warn("ssh_known_hosts", "err", err)
		}
	}
	go transparent(ln, macOrigDst, names, dial, pu, log)
	return nil
}

// macNetwork sets the routes, the pf rule, and the resolver.
func macNetwork(dnsServers []string) error {
	run := func(stdin string, name string, args ...string) error {
		c := exec.Command(name, args...)
		c.Stdin = strings.NewReader(stdin)
		out, err := c.CombinedOutput()
		if err != nil && !bytes.Contains(out, []byte("File exists")) {
			return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
		}
		return nil
	}
	for _, dst := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := run("", "/sbin/route", "-n", "add", "-net", dst, "-interface", "lo0"); err != nil {
			return err
		}
	}
	rule := "rdr pass on lo0 inet proto tcp from any to ! 127.0.0.0/8 -> 127.0.0.1 port " + strconv.Itoa(VMTransparentPort) + "\n"
	if err := run(rule, "/sbin/pfctl", "-q", "-a", macAnchor, "-f", "-"); err != nil {
		return err
	}
	if err := run("", "/sbin/pfctl", "-q", "-E"); err != nil {
		return err
	}
	if len(dnsServers) == 0 {
		return nil
	}
	sc := "d.init\nd.add ServerAddresses * " + strings.Join(dnsServers, " ") +
		"\nd.add SupplementalMatchDomains * \"\"\nset State:/Network/Service/magi/DNS\nquit\n"
	return run(sc, "/usr/sbin/scutil")
}

// pfioc_natlook of xnu (bsd/net/pfvar.h): addresses, then the ports
// (union pf_state_xport, 4 bytes, in network order), then af, proto,
// proto_variant, and direction.
type pfNatLook struct {
	saddr, daddr, rsaddr, rdaddr     [16]byte
	sxport, dxport, rsxport, rdxport [4]byte
	af, proto, protoVariant, dir     uint8
}

// diocNatLook is _IOWR('D', 23, struct pfioc_natlook).
const diocNatLook = 0xc0000000 | uintptr(unsafe.Sizeof(pfNatLook{}))<<16 | 'D'<<8 | 23

var pfDev struct {
	once sync.Once
	fd   int
	err  error
}

// macOrigDst returns the destination of a connection before the pf
// redirect: with DIOCNATLOOK, or else from the state list of pfctl.
func macOrigDst(c net.Conn) (netip.AddrPort, error) {
	peer, err1 := netip.ParseAddrPort(c.RemoteAddr().String())
	local, err2 := netip.ParseAddrPort(c.LocalAddr().String())
	if err1 != nil || err2 != nil || !peer.Addr().Unmap().Is4() {
		return netip.AddrPort{}, errors.New("not an IPv4 TCP connection")
	}
	a, err := natLook(peer, local)
	method := "DIOCNATLOOK"
	if err != nil {
		method = "pfctl (DIOCNATLOOK: " + err.Error() + ")"
		a, err = pfctlOrigDst(peer)
	}
	if err == nil {
		lookupOnce.Do(func() { slog.Info("transparent: the first destination", "dest", a, "lookup", method) })
	}
	return a, err
}

var lookupOnce sync.Once

func natLook(peer, local netip.AddrPort) (netip.AddrPort, error) {
	pfDev.once.Do(func() { pfDev.fd, pfDev.err = unix.Open("/dev/pf", unix.O_RDWR, 0) })
	if pfDev.err != nil {
		return netip.AddrPort{}, pfDev.err
	}
	for _, dir := range []uint8{2, 1} { // PF_OUT, PF_IN
		nl := pfNatLook{af: unix.AF_INET, proto: unix.IPPROTO_TCP, dir: dir}
		s, d := peer.Addr().Unmap().As4(), local.Addr().Unmap().As4()
		copy(nl.saddr[:], s[:])
		copy(nl.daddr[:], d[:])
		binary.BigEndian.PutUint16(nl.sxport[:], peer.Port())
		binary.BigEndian.PutUint16(nl.dxport[:], local.Port())
		if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(pfDev.fd), diocNatLook, uintptr(unsafe.Pointer(&nl))); e != 0 {
			continue
		}
		a := netip.AddrFrom4([4]byte(nl.rdaddr[:4]))
		return netip.AddrPortFrom(a, binary.BigEndian.Uint16(nl.rdxport[:])), nil
	}
	return netip.AddrPort{}, errors.New("DIOCNATLOOK found no state")
}

// pfctlOrigDst finds the line "ALL tcp 127.0.0.1:15001 <- DEST <- PEER"
// in the state list of pfctl.
func pfctlOrigDst(peer netip.AddrPort) (netip.AddrPort, error) {
	out, err := exec.Command("/sbin/pfctl", "-q", "-s", "state").Output()
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("pfctl -s state: %w", err)
	}
	return parsePfState(out, peer)
}

func netnsExec([]string) error { return errors.New("network namespaces are on Linux only") }

func podnetExec([]string) error { return errors.New("Pods run Linux") }
