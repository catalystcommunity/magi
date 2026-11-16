package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/primitives/execution/egressenv"
	"github.com/catalystcommunity/magi/primitives/execution/envd"
	"github.com/catalystcommunity/magi/primitives/execution/sandbox"
)

// The egress proxy route of a sandbox: a command in the sandbox connects
// to sandbox.ProxyAddr, envd copies the connection to the proxy socket,
// and the runner sends it through GET /runner/egress on Magi to the egress
// proxy. The runner machine connects only to Magi.

// egressAccess is the proxy user of an environment. It is kept next to
// the environment, out of the sandbox, so that a restarted runner can
// serve the environment again.
type egressAccess struct {
	Environment string            `json:"environment"`
	Credential  string            `json:"credential"`
	Env         map[string]string `json:"env,omitempty"`
}

func (r *Runner) accessFile(id string) string {
	return filepath.Join(r.Boxes.Dir(id), "egress-access.json")
}

// ErrNoEgressRoute is returned when a sandbox would have no way out except
// the network of the runner machine. The runner fails closed: it does not
// start the sandbox, unless the runner configuration sets
// sandbox.allow_host_network (an explicit, per-runner decision).
var ErrNoEgressRoute = errors.New("no egress proxy route for the sandbox")

// egressProxy asks Magi how the sandbox of the step reaches the egress
// proxy. A nil proxy with a nil error means that the sandbox uses the host
// network, and that happens only when the runner configuration allows it
// (Requirement 6.3: no route around the proxy). Otherwise a missing proxy
// is an error and the step fails.
func (r *Runner) egressProxy(ctx context.Context, st magiapi.RunnerStep) (*magiapi.RunnerEgressProxy, error) {
	res, err := r.Magi.EgressAccess(ctx, st)
	return egressRoute(res, err, r.Boxes.CanProxy(), r.Cfg.Sandbox.AllowHostNetwork, r.Log)
}

// egressRoute decides the route from the answer of Magi. It is separate
// from egressProxy so that each case can be tested.
func egressRoute(res magiapi.RunnerEgressAccessResponse, err error, canProxy, allowHost bool, log *slog.Logger) (*magiapi.RunnerEgressProxy, error) {
	hostNet := func(why string) (*magiapi.RunnerEgressProxy, error) {
		if !allowHost {
			return nil, fmt.Errorf("%w: %s (set sandbox.allow_host_network in the runner configuration to use the network of the runner machine)", ErrNoEgressRoute, why)
		}
		log.Warn("sandbox.allow_host_network is set: the sandbox uses the host network, with no egress proxy", "reason", why)
		return nil, nil
	}
	if rpc.CodeOf(err) == 404 {
		return hostNet("Magi has no egress-access operation (an older release)")
	}
	if err != nil {
		return nil, fmt.Errorf("egress access: %w", err)
	}
	if res.Proxy == nil {
		return hostNet("Magi has no egress proxy for runners")
	}
	if !canProxy {
		return hostNet("this sandbox kind shares the host network and cannot use the egress proxy")
	}
	return res.Proxy, nil
}

// startBox starts the sandbox of env, with the egress proxy when p is set.
//
// What the sandbox holds (Requirement 4.2): no real secret. It holds the
// proxy credential (it only lets a command use the egress proxy, which
// applies the rules) and nothing else. The placeholder variables of the
// rules (GH_TOKEN) are NOT in the environment: a script of the repository or
// the apprentice would otherwise make requests that the proxy fills with the
// real token. They are kept in the access file and only the step that needs
// one (pr-merge) sets it for its own script.
func (r *Runner) startBox(env, token string, p *magiapi.RunnerEgressProxy) error {
	if p == nil {
		return r.Boxes.Start(env, token, r.sandboxEnv(), nil)
	}
	sp := &sandbox.Proxy{CA: p.Ca}
	if p.SshKnownHosts != nil {
		sp.SSHKnownHosts = []byte(*p.SshKnownHosts + "\n")
	}
	if err := r.Boxes.Start(env, token, r.proxyEnv(p.Environment, p.Credential, nil), sp); err != nil {
		return err
	}
	b, _ := json.Marshal(egressAccess{Environment: p.Environment, Credential: p.Credential, Env: p.Env})
	if err := os.WriteFile(r.accessFile(env), b, 0o600); err != nil {
		return err
	}
	return r.serveProxy(env)
}

// proxyEnv: extra are the placeholder variables of the run (the proxy
// puts the secret in place of a placeholder only for the permitted hosts).
func (r *Runner) proxyEnv(env, credential string, extra map[string]string) map[string]string {
	all := r.sandboxEnv()
	for k, v := range extra {
		all[k] = v
	}
	for k, v := range egressenv.Vars(egressenv.URL(sandbox.ProxyAddr, env, credential), r.Boxes.ProxyCA()) {
		all[k] = v
	}
	return all
}

// resumeBox starts envd again in an existing environment, and its proxy.
func (r *Runner) resumeBox(id string) error {
	if !r.Boxes.Proxied(id) {
		return r.Boxes.Resume(id, r.sandboxEnv())
	}
	b, err := os.ReadFile(r.accessFile(id))
	if err != nil {
		return err
	}
	var a egressAccess
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if err := r.serveProxy(id); err != nil {
		return err
	}
	return r.Boxes.Resume(id, r.proxyEnv(a.Environment, a.Credential, nil))
}

// serveProxy serves the proxy socket of an environment until removeBox.
func (r *Runner) serveProxy(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proxies[id] != nil {
		return nil
	}
	ln, err := r.Boxes.ListenProxy(id)
	if err != nil {
		return err
	}
	if r.proxies == nil {
		r.proxies = map[string]net.Listener{}
	}
	r.proxies[id] = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := r.dialEgress(context.Background())
				if err != nil {
					r.Log.Warn("egress tunnel", "env", id, "err", err)
					return
				}
				defer up.Close()
				envd.Splice(c, up)
			}()
		}
	}()
	return nil
}

// removeBox stops the proxy of an environment and removes it.
func (r *Runner) removeBox(id string) error {
	r.mu.Lock()
	if ln := r.proxies[id]; ln != nil {
		ln.Close()
		delete(r.proxies, id)
	}
	r.mu.Unlock()
	return r.Boxes.Remove(id)
}

// dialEgress opens a tunnel to the egress proxy: an HTTP/1.1 upgrade on
// GET /runner/egress with the runner token. The upgrade has the form of a
// WebSocket upgrade, so that HTTP gateways pass it; after the 101 answer
// the connection carries the proxy protocol, not WebSocket frames.
func (r *Runner) dialEgress(ctx context.Context) (net.Conn, error) {
	return r.dialMagi(ctx, "/runner/egress")
}

// dialMagi opens an HTTP upgrade to path on Magi with the runner token.
func (r *Runner) dialMagi(ctx context.Context, path string) (net.Conn, error) {
	tok, err := os.ReadFile(r.Cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	return envd.DialUpgrade(ctx, r.Cfg.Server, path, string(tok))
}
