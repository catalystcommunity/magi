package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/egressenv"
	corev1 "k8s.io/api/core/v1"
)

func TestObjects(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "envd-linux-amd64"), []byte("bin"), 0o755)
	k := &Kubernetes{Namespace: "magi-envs", Delivery: DeliverFetch, PublicURL: "http://execution.magi.svc:8080",
		FetchImage: "busybox:1.37", PullSecret: "pull", ControlNamespace: "magi", Envd: &Binaries{Dir: dir}}
	none, disk, cpu := "none", uint64(4096), uint64(500)
	spec := api.EnvironmentSpec{Image: "golang:1.26-bookworm", Network: &none, DiskMb: &disk, CpuMillis: &cpu,
		Env: map[string]string{"GH_TOKEN": "s3cret"}}
	sec, pod, np, err := k.Objects("env_abc", spec, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if pod.Name != "magi-env-abc" || pod.Labels["magi.environment"] != "env_abc" || pod.Labels["app.kubernetes.io/part-of"] != "magi" {
		t.Fatalf("meta %+v", pod.ObjectMeta)
	}
	if string(sec.Data["token"]) != "tok" || string(sec.Data["env.GH_TOKEN"]) != "s3cret" {
		t.Fatalf("secret data keys wrong")
	}
	s := pod.Spec
	if *s.AutomountServiceAccountToken || s.ImagePullSecrets[0].Name != "pull" || s.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("pod spec %+v", s)
	}
	m := s.Containers[0]
	if m.Command[0] != "/magi/envd" || *m.SecurityContext.AllowPrivilegeEscalation || m.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("main %+v", m)
	}
	if m.Env[0].ValueFrom.SecretKeyRef.Key != "env.GH_TOKEN" || m.Env[0].Value != "" {
		t.Fatalf("env must come from the secret: %+v", m.Env)
	}
	lim := m.Resources.Limits
	if lim.Cpu().String() != "500m" || lim.Memory().String() != "2Gi" || lim.StorageEphemeral().String() != "4Gi" {
		t.Fatalf("limits %v", lim)
	}
	init := s.InitContainers[0]
	if !strings.Contains(init.Command[2], "sha256sum -c") || init.Env[1].Value == "" || init.Env[2].Value != "" {
		t.Fatalf("init %+v", init)
	}
	for _, v := range s.Volumes {
		if v.Secret != nil && (len(v.Secret.Items) != 1 || v.Secret.Items[0].Key != "token") {
			t.Fatalf("only the token is a file: %+v", v.Secret)
		}
	}
	if np == nil || len(np.Spec.Egress) != 2 || np.Spec.Egress[1].Ports[0].Port.IntValue() != 8080 {
		t.Fatalf("fetch policy %+v", np)
	}

	k.Delivery = DeliverPush
	_, pod, np, err = k.Objects("env_abc", spec, "tok")
	if err != nil || len(np.Spec.Egress) != 0 || !strings.Contains(pod.Spec.InitContainers[0].Command[2], "envd.ready") {
		t.Fatalf("push: %v %+v", err, np)
	}
	spec.Network = nil
	if _, _, np, _ = k.Objects("env_abc", spec, "tok"); np != nil {
		t.Fatal("no policy expected without network none")
	}
}

func TestArch(t *testing.T) {
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64": "arm64", "riscv64": "riscv64"} {
		if got := Arch(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
}

func TestProxyNetwork(t *testing.T) {
	k := &Kubernetes{Namespace: "magi-envs", Delivery: DeliverPush, ControlNamespace: "magi", Envd: &Binaries{Dir: t.TempDir()}}
	proxy := "proxy"
	spec := api.EnvironmentSpec{Image: "golang:1.26-bookworm", Network: &proxy}
	if _, _, _, err := k.Objects("env_abc", spec, "tok"); err == nil {
		t.Fatal("network proxy without the egress settings must fail")
	}
	k.Egress = &EgressProxy{Host: "egress.magi.svc", Port: 3128, Namespace: "magi", Key: []byte("0123456789abcdef0123456789abcdef"), CA: []byte("PEM"),
		SSHPub: "ssh-ed25519 AAAA", Lookup: func(string) ([]string, error) { return []string{"10.43.0.7"}, nil }}
	sec, pod, np, err := k.Objects("env_abc", spec, "tok")
	if err != nil {
		t.Fatal(err)
	}
	// Tools use the proxy port of envd; envd reaches the proxy by address.
	if string(sec.Data["egress.url"]) != egressenv.URL("127.0.0.1:3128", "env_abc", k.Egress.Credential("env_abc")) || string(sec.Data["egress-ca.pem"]) != "PEM" ||
		string(sec.Data["ssh_known_hosts"]) != "* ssh-ed25519 AAAA\n" {
		t.Fatalf("secret must have the proxy URL, the CA, and the known hosts: %q", sec.Data)
	}
	cmd := strings.Join(pod.Spec.Containers[0].Command, " ")
	if !strings.Contains(cmd, "-forward 127.0.0.1:3128=tcp:10.43.0.7:3128 -transparent tcp:10.43.0.7:3128 -dns 127.0.0.1:15053") {
		t.Fatalf("envd command %q", cmd)
	}
	// Only the init container that sets up the firewall has a capability.
	if len(pod.Spec.InitContainers) != 2 {
		t.Fatalf("init containers %+v", pod.Spec.InitContainers)
	}
	ni := pod.Spec.InitContainers[1]
	if strings.Join(ni.Command, " ") != "/magi/envd podnet -exclude 10.43.0.7:3128" || *ni.SecurityContext.RunAsUser != 0 ||
		len(ni.SecurityContext.Capabilities.Add) != 1 || ni.SecurityContext.Capabilities.Add[0] != "NET_ADMIN" || *ni.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("network init container %+v", ni)
	}
	if c := pod.Spec.Containers[0].SecurityContext.Capabilities; len(c.Add) != 0 || c.Drop[0] != "ALL" {
		t.Fatalf("main container capabilities %+v", c)
	}
	vars := map[string]corev1.EnvVar{}
	for _, e := range pod.Spec.Containers[0].Env {
		vars[e.Name] = e
	}
	if v := vars["HTTPS_PROXY"]; v.ValueFrom == nil || v.ValueFrom.SecretKeyRef.Key != "egress.url" || v.Value != "" {
		t.Fatalf("HTTPS_PROXY must come from the secret: %+v", v)
	}
	if vars["SSL_CERT_FILE"].Value != secretDir+"/egress-ca.pem" {
		t.Fatalf("SSL_CERT_FILE %+v", vars["SSL_CERT_FILE"])
	}
	items := 0
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil {
			items = len(v.Secret.Items)
		}
	}
	if items != 3 {
		t.Fatalf("the token, the CA, and the known hosts are files; got %d items", items)
	}
	if vars["GIT_SSH_COMMAND"].Value != "ssh -o UserKnownHostsFile="+secretDir+"/ssh_known_hosts" {
		t.Fatalf("GIT_SSH_COMMAND %+v", vars["GIT_SSH_COMMAND"])
	}
	if np == nil || len(np.Spec.Egress) != 2 || np.Spec.Egress[1].Ports[0].Port.IntValue() != 3128 ||
		np.Spec.Egress[1].To[0].PodSelector.MatchLabels["app.kubernetes.io/name"] != "egress" {
		t.Fatalf("proxy policy %+v", np)
	}
	k.Delivery, k.PublicURL = DeliverFetch, "http://execution.magi.svc:8080"
	if _, _, np, _ = k.Objects("env_abc", spec, "tok"); len(np.Spec.Egress) != 3 {
		t.Fatalf("fetch + proxy: DNS, control plane, proxy; got %+v", np.Spec.Egress)
	}
}
