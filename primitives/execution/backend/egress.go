package backend

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/catalystcommunity/magi/primitives/execution/egressenv"
)

// EgressProxy sends the traffic of an environment with network "proxy"
// through the Magi egress proxy (primitives/egress). The proxy is the only
// route out: a NetworkPolicy permits DNS and the proxy port only.
type EgressProxy struct {
	Host      string // the proxy host as Pods see it, such as egress.magi.svc
	Port      int32
	Namespace string // the namespace of the proxy
	Key       []byte // the environment key of the proxy (MAGI_EGRESS_ENV_KEY)
	CA        []byte // the CA certificate of the proxy, PEM
	// SSHPub is the OpenSSH public key of the SSH host key of the proxy;
	// empty when the proxy does not end SSH.
	SSHPub string
	// Lookup resolves Host; nil means net.LookupHost.
	Lookup func(host string) ([]string, error)
}

// LoadEgressProxy reads the key and CA files, and the SSH public key file
// when sshPubFile is set. addr is host:port.
func LoadEgressProxy(addr, namespace, keyFile, caFile, sshPubFile string) (*EgressProxy, error) {
	host, port, ok := strings.Cut(addr, ":")
	if !ok || host == "" {
		return nil, fmt.Errorf("egress proxy %q: expected host:port", addr)
	}
	var p int32
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil || p <= 0 {
		return nil, fmt.Errorf("egress proxy %q: bad port", addr)
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	k := []byte(strings.TrimSpace(string(key)))
	if len(k) < 32 {
		return nil, fmt.Errorf("%s: the key must have at least 32 bytes", keyFile)
	}
	e := &EgressProxy{Host: host, Port: p, Namespace: namespace, Key: k, CA: ca}
	if sshPubFile != "" {
		pub, err := os.ReadFile(sshPubFile)
		if err != nil {
			return nil, err
		}
		e.SSHPub = strings.TrimSpace(string(pub))
	}
	return e, nil
}

// Addr is the IP address and port of the proxy. A Pod with the transparent
// route cannot resolve names itself (envd answers DNS with fake
// addresses), so it gets the address.
func (e *EgressProxy) Addr() (string, error) {
	if net.ParseIP(e.Host) != nil {
		return net.JoinHostPort(e.Host, fmt.Sprint(e.Port)), nil
	}
	look := e.Lookup
	if look == nil {
		look = net.LookupHost
	}
	ips, err := look(e.Host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("egress proxy %s: %v", e.Host, err)
	}
	return net.JoinHostPort(ips[0], fmt.Sprint(e.Port)), nil
}

// Credential is the proxy password of environment env.
func (e *EgressProxy) Credential(env string) string { return egressenv.Credential(e.Key, env) }

// URL is the proxy URL with the credential of env.
func (e *EgressProxy) URL(env string) string {
	return egressenv.URL(fmt.Sprintf("%s:%d", e.Host, e.Port), env, e.Credential(env))
}
