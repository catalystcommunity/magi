package envd

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"unsafe"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// vmChild marks the envd process that the init of a task VM starts.
const vmChild = "MAGI_ENVD_VM_CHILD"

// vmInit runs envd as the init (process 1) of a task VM. Process 1 mounts
// the kernel file systems, sets up the network, starts envd again as its
// child, reaps orphaned processes, and stops the VM when the child exits.
// The child returns the flags of VMConfig.
func vmInit(log *slog.Logger) ([]string, error) {
	if os.Getenv(vmChild) == "1" {
		return readVMConfig()
	}
	if os.Getpid() != 1 {
		return nil, errors.New("-vm: envd must be process 1 of the VM")
	}
	if err := vmMounts(); err != nil {
		return nil, err
	}
	if err := vmNetwork(); err != nil {
		return nil, err
	}
	unix.Sethostname([]byte("magi"))
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Env = []string{vmChild + "=1"}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, 0, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil || pid == cmd.Process.Pid {
			log.Info("envd stopped; the VM stops", "status", ws.ExitStatus())
			unix.Sync()
			unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
			os.Exit(1)
		}
	}
}

func vmMounts() error {
	type m struct{ src, dst, fs, data string }
	for _, x := range []m{
		{"proc", "/proc", "proc", ""},
		{"sysfs", "/sys", "sysfs", ""},
		{"devtmpfs", "/dev", "devtmpfs", "mode=0755"},
		{"devpts", "/dev/pts", "devpts", "newinstance,ptmxmode=0666,mode=0620"},
		{"tmpfs", "/dev/shm", "tmpfs", "mode=1777"},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", ""},
	} {
		os.MkdirAll(x.dst, 0o755)
		if err := unix.Mount(x.src, x.dst, x.fs, unix.MS_NOSUID, x.data); err != nil && err != unix.EBUSY {
			return fmt.Errorf("mount %s: %w", x.dst, err)
		}
	}
	os.Remove("/dev/ptmx")
	return os.Symlink("pts/ptmx", "/dev/ptmx")
}

// vmNetwork brings up loopback and a dummy interface with the default
// routes, so that a connection to any address reaches the firewall. The
// firewall sends each TCP connection that does not go to loopback to the
// transparent port (VMTransparentPort). Other traffic goes to the dummy
// interface, which drops it.
func vmNetwork() error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return err
	}
	d := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "magi0"}}
	if err := netlink.LinkAdd(d); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(d); err != nil {
		return err
	}
	for _, a := range []string{"10.255.0.2/24", "fd6d:6167:6900::2/64"} {
		addr, _ := netlink.ParseAddr(a)
		addr.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(d, addr); err != nil {
			return fmt.Errorf("address %s: %w", a, err)
		}
	}
	for _, def := range []string{"0.0.0.0/0", "::/0"} {
		_, dst, _ := net.ParseCIDR(def)
		if err := netlink.RouteAdd(&netlink.Route{LinkIndex: d.Attrs().Index, Dst: dst}); err != nil {
			return fmt.Errorf("route %s: %w", def, err)
		}
	}
	c := &nftables.Conn{}
	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: "magi"})
	ch := c.AddChain(&nftables.Chain{Name: "out", Table: t, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest})
	lon := make([]byte, 16)
	copy(lon, "lo")
	c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: lon},
		&expr.Verdict{Kind: expr.VerdictReturn},
	}})
	c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(VMTransparentPort)},
		&expr.Redir{RegisterProtoMin: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED},
	}})
	if err := c.Flush(); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	return nil
}

// VMTransparentPort is the guest port that the firewall sends TCP
// connections to.
const VMTransparentPort = 15001

// startTransparent serves the transparent port and the guest DNS at each
// address in dns. The connections go through the proxy at upstream (a dial
// spec); proxyURL has the proxy user and credential.
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
	names.search = searchDomains("/etc/resolv.conf")
	names.forward = func(q []byte) ([]byte, error) { return dnsThroughProxy(dial, pu, q) }
	for _, a := range dns {
		if err := names.serve(a, log); err != nil {
			return err
		}
	}
	for _, a := range []string{"127.0.0.1", "[::1]"} {
		ln, err := net.Listen("tcp", a+":"+strconv.Itoa(VMTransparentPort))
		if err != nil && a == "[::1]" {
			// A Pod can have no IPv6.
			log.Warn("transparent: no IPv6 loopback", "err", err)
			continue
		}
		if err != nil {
			return err
		}
		go transparent(ln, origDst, names, dial, pu, log)
	}
	return nil
}

// origDst returns the destination of a redirected connection before the
// redirect (SO_ORIGINAL_DST).
func origDst(c net.Conn) (netip.AddrPort, error) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return netip.AddrPort{}, errors.New("not a TCP connection")
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	v6 := tc.LocalAddr().(*net.TCPAddr).IP.To4() == nil
	var out netip.AddrPort
	var serr error
	err = rc.Control(func(fd uintptr) {
		if v6 {
			var mi *unix.IPv6MTUInfo
			mi, serr = unix.GetsockoptIPv6MTUInfo(int(fd), unix.IPPROTO_IPV6, ip6tSoOriginalDst)
			if serr == nil {
				port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&mi.Addr.Port))[:])
				out = netip.AddrPortFrom(netip.AddrFrom16(mi.Addr.Addr), port)
			}
			return
		}
		var mr *unix.IPv6Mreq
		mr, serr = unix.GetsockoptIPv6Mreq(int(fd), unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
		if serr == nil {
			// The value is a sockaddr_in: family, port, address.
			b := mr.Multiaddr
			out = netip.AddrPortFrom(netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]}), binary.BigEndian.Uint16(b[2:4]))
		}
	})
	if err == nil {
		err = serr
	}
	return out, err
}

// ip6tSoOriginalDst is IP6T_SO_ORIGINAL_DST (linux/netfilter_ipv6/ip6_tables.h).
const ip6tSoOriginalDst = 80

// netnsExec sets up the network of a new network namespace as in a task VM
// (vmNetwork), and then runs args[1:] (args[0] is "--"). It must run as
// root of a new user namespace that owns the network namespace. Commands
// can then bind port 53, so that envd answers DNS at 127.0.0.53.
func netnsExec(args []string) error {
	if len(args) < 2 || args[0] != "--" {
		return errors.New("usage: envd netns -- PROGRAM [ARGS]")
	}
	if err := vmNetwork(); err != nil {
		return err
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_unprivileged_port_start", []byte("0"), 0); err != nil {
		return err
	}
	return unix.Exec(args[1], args[1:], os.Environ())
}

// podnetExec sets up the firewall of a Pod with the transparent route
// (args: -exclude IP:PORT, the proxy). It runs in an init container with
// CAP_NET_ADMIN, so that the main container needs no capability. The
// firewall sends DNS (port 53) to PodDNSPort and each other TCP
// connection to VMTransparentPort, except a connection to loopback or to
// the proxy.
func podnetExec(args []string) error {
	fs := flag.NewFlagSet("podnet", flag.ContinueOnError)
	exclude := fs.String("exclude", "", "IP:PORT of the egress proxy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	px, err := netip.ParseAddrPort(*exclude)
	if err != nil {
		return fmt.Errorf("-exclude: %w", err)
	}
	c := &nftables.Conn{}
	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: "magi"})
	ch := c.AddChain(&nftables.Chain{Name: "out", Table: t, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest})
	lon := make([]byte, 16)
	copy(lon, "lo")
	tcp := []expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}}}
	dport := func(p uint16) []expr.Any {
		return []expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(p)}}
	}
	redir := func(p uint16) []expr.Any {
		return []expr.Any{&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(p)},
			&expr.Redir{RegisterProtoMin: 1, Flags: unix.NF_NAT_RANGE_PROTO_SPECIFIED}}
	}
	fam, off, ip := byte(unix.NFPROTO_IPV4), uint32(16), px.Addr().Unmap().AsSlice()
	if px.Addr().Unmap().Is6() {
		fam, off = unix.NFPROTO_IPV6, 24
	}
	rules := [][]expr.Any{
		{&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: lon}, &expr.Verdict{Kind: expr.VerdictReturn}},
		append(append([]expr.Any{&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{fam}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: uint32(len(ip))},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ip}}, append(tcp, dport(px.Port())...)...), &expr.Verdict{Kind: expr.VerdictReturn}),
		append(append([]expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_UDP}}},
			dport(53)...), redir(PodDNSPort)...),
		append(append(append([]expr.Any{}, tcp...), dport(53)...), redir(PodDNSPort)...),
		append(append([]expr.Any{}, tcp...), redir(VMTransparentPort)...),
	}
	for _, r := range rules {
		c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: r})
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	return nil
}
