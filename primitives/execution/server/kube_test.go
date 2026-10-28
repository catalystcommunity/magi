package server_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	"github.com/catalystcommunity/magi/primitives/execution/backend"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type kubeEnv struct {
	k      *backend.Kubernetes
	cs     *kubernetes.Clientset
	ns     string
	c      *api.ExecutionClient
	create func(api.EnvironmentSpec) api.Environment
}

// kubeSetup gives a service with a Kubernetes backend that runs outside the
// cluster. It needs MAGI_TEST_KUBE=1, the kube context MAGI_TEST_KUBE_CONTEXT
// (default "default"), and namespace MAGI_TEST_KUBE_NAMESPACE (default
// "magi-envs"). MAGI_TEST_KUBE_PULL_SECRET is optional.
func kubeSetup(t *testing.T) *kubeEnv {
	if os.Getenv("MAGI_TEST_KUBE") != "1" {
		t.Skip("MAGI_TEST_KUBE is not 1")
	}
	kctx, ns := os.Getenv("MAGI_TEST_KUBE_CONTEXT"), os.Getenv("MAGI_TEST_KUBE_NAMESPACE")
	if kctx == "" {
		kctx = "default"
	}
	if ns == "" {
		ns = "magi-envs"
	}
	rc, _, err := backend.RestConfig("", kctx)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	k := &backend.Kubernetes{Client: cs, Config: rc, Namespace: ns, Delivery: backend.DeliverPush, Connect: backend.ConnectPortForward,
		FetchImage: "busybox:1.37", PullSecret: os.Getenv("MAGI_TEST_KUBE_PULL_SECRET"), ControlNamespace: "magi",
		Envd: &backend.Binaries{Dir: envdDir}}
	t.Cleanup(k.Close)
	h := newHarness(t, map[string]backend.Backend{"kubernetes": k}, "kubernetes")
	ke := &kubeEnv{k: k, cs: cs, ns: ns, c: h.client(alice)}
	var ids []string
	t.Cleanup(func() {
		for _, id := range ids {
			k.Destroy(context.Background(), id)
		}
	})
	ke.create = func(spec api.EnvironmentSpec) api.Environment {
		t.Helper()
		start := time.Now()
		e, err := ke.c.Create(context.Background(), api.CreateRequest{Spec: spec, WaitReadyMs: u64(uint64((5 * time.Minute).Milliseconds()))})
		if e.Id != "" {
			ids = append(ids, e.Id)
		}
		if err != nil || e.Status != "ready" {
			t.Fatalf("create %s: %+v %v (message %q)", spec.Image, e, err, deref(e.Message))
		}
		t.Logf("%s ready in %v", e.Id, time.Since(start).Round(time.Millisecond))
		return e
	}
	return ke
}

// TestKubernetes uses push delivery and port-forward connection.
func TestKubernetes(t *testing.T) {
	ke := kubeSetup(t)
	k, cs, ns, c, create := ke.k, ke.cs, ke.ns, ke.c, ke.create
	ctx := context.Background()

	env := create(api.EnvironmentSpec{Image: "golang:1.26-bookworm",
		Env: map[string]string{"MAGI_TEST_SECRET": "abc"}, Network: str("egress")})
	r, err := c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"sh", "-c", `go version; echo "$MAGI_TEST_SECRET"; pwd; uname -m`}})
	if err != nil || r.ExitCode != 0 || !strings.Contains(string(r.Stdout), "go1.26") || !strings.Contains(string(r.Stdout), "abc\n/work\n") {
		t.Fatalf("exec: %+v %v stdout=%q stderr=%q", r, err, r.Stdout, r.Stderr)
	}
	t.Logf("exec: %q", r.Stdout)
	// A long command with no output keeps the connection idle.
	if r, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"sleep", "90"}, TimeoutMs: u64(120000)}); err != nil || r.ExitCode != 0 {
		t.Fatalf("long exec: %+v %v", r, err)
	}
	if _, err := c.WriteFile(ctx, api.WriteFileRequest{Environment: env.Id, Path: "hello/main.go",
		Body: []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi from magi\") }\n")}); err != nil {
		t.Fatal(err)
	}
	r, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"go", "run", "main.go"}, Cwd: str("hello"),
		Env: map[string]string{"GOCACHE": "/work/.cache", "GOFLAGS": "-mod=mod"}, TimeoutMs: u64(180000)})
	if err != nil || strings.TrimSpace(string(r.Stdout)) != "hi from magi" {
		if pod, perr := cs.CoreV1().Pods(ns).Get(ctx, backend.PodName(env.Id), metav1.GetOptions{}); perr == nil {
			for _, st := range pod.Status.ContainerStatuses {
				t.Logf("container %s: restarts=%d state=%+v last=%+v", st.Name, st.RestartCount, st.State, st.LastTerminationState)
			}
		}
		t.Fatalf("go run: %+v %v stderr=%q", r, err, r.Stderr)
	}
	f, err := c.ReadFile(ctx, api.ReadFileRequest{Environment: env.Id, Path: "/work/hello/main.go", MaxBytes: u64(12)})
	if err != nil || string(f.Body) != "package main" || !f.Truncated {
		t.Fatalf("read: %+v %v", f, err)
	}
	lf, err := c.ListFiles(ctx, api.ListFilesRequest{Environment: env.Id, Path: "/work/hello"})
	if err != nil || len(lf.Entries) != 1 || lf.Entries[0].Path != "main.go" {
		t.Fatalf("list: %+v %v", lf, err)
	}
	start := time.Now()
	r, err = c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"sh", "-c", "sleep 60 & sleep 60"}, TimeoutMs: u64(500)})
	if err != nil || !r.TimedOut || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout: %+v %v", r, err)
	}

	// network "none": a NetworkPolicy denies egress.
	none := create(api.EnvironmentSpec{Image: "busybox:1.37", Network: str("none")})
	r, err = c.Exec(ctx, api.ExecRequest{Environment: none.Id, Argv: []string{"wget", "-T", "3", "-q", "-O", "/dev/null", "http://1.1.1.1/"}, TimeoutMs: u64(20000)})
	if err != nil || r.ExitCode == 0 {
		t.Fatalf("egress with network none: %+v %v", r, err)
	}
	if _, err := cs.NetworkingV1().NetworkPolicies(ns).Get(ctx, backend.PodName(none.Id), metav1.GetOptions{}); err != nil {
		t.Fatalf("network policy: %v", err)
	}

	for _, e := range []api.Environment{env, none} {
		if d, err := c.Destroy(ctx, api.DestroyRequest{Id: e.Id}); err != nil || d.Status != "destroyed" {
			t.Fatalf("destroy: %+v %v", d, err)
		}
		name := backend.PodName(e.Id)
		if _, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("secret %s remains: %v", name, err)
		}
		if _, err := cs.NetworkingV1().NetworkPolicies(ns).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("network policy %s remains: %v", name, err)
		}
	}

	// A bad image fails instead of waiting forever.
	e, err := c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{Image: "busybox:no-such-tag-magi"}, WaitReadyMs: u64(120000)})
	defer k.Destroy(context.Background(), e.Id)
	if err != nil || e.Status != "failed" {
		t.Fatalf("bad image: %+v %v", e, err)
	}
	t.Logf("bad image message: %s", deref(e.Message))
	c.Destroy(ctx, api.DestroyRequest{Id: e.Id})
}

// TestKubernetesFetch checks fetch delivery, the default in a cluster. A
// short-lived busybox httpd Pod stands in for the control plane: the test
// pushes the envd builds into it, and environments download them from its
// Pod IP. With network "none", the NetworkPolicy must still permit the
// download from the control namespace.
func TestKubernetesFetch(t *testing.T) {
	ke := kubeSetup(t)
	k, cs, ns := ke.k, ke.cs, ke.ns
	ctx := context.Background()
	name := fmt.Sprintf("magi-envd-server-%d", time.Now().UnixNano()%1e9)
	no := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app.kubernetes.io/part-of": "magi"}},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &no,
			Containers: []corev1.Container{{Name: "httpd", Image: "busybox:1.37",
				Command: []string{"sh", "-c", "mkdir -p /srv/envd && exec httpd -f -p 8080 -h /srv"}}},
		},
	}
	if _, err := cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		zero := int64(0)
		cs.CoreV1().Pods(ns).Delete(context.Background(), name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
	})
	var ip string
	for i := 0; i < 120 && ip == ""; i++ {
		time.Sleep(time.Second)
		if p, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{}); err == nil && p.Status.Phase == corev1.PodRunning {
			ip = p.Status.PodIP
		}
	}
	if ip == "" {
		t.Fatal("envd server pod did not start")
	}
	for _, p := range backend.Platforms {
		var err error
		for try := 0; try < 3; try++ {
			var f *os.File
			if f, err = os.Open(filepath.Join(envdDir, "envd-"+p)); err != nil {
				t.Fatal(err)
			}
			_, err = k.Exec(ctx, name, "httpd", []string{"sh", "-c", "cat > /srv/envd/" + p + ".part && mv /srv/envd/" + p + ".part /srv/envd/" + p}, f)
			f.Close()
			if err == nil {
				break
			}
			t.Logf("upload envd %s (try %d): %v", p, try+1, err)
		}
		if err != nil {
			t.Fatalf("upload envd %s: %v", p, err)
		}
	}
	k.Delivery, k.PublicURL, k.ControlNamespace = backend.DeliverFetch, "http://"+ip+":8080", ns
	for _, network := range []string{"egress", "none"} {
		env := ke.create(api.EnvironmentSpec{Image: "busybox:1.37", Network: str(network)})
		r, err := ke.c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"sh", "-c", "uname -m; ls -l /magi/envd"}})
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("%s exec: %+v %v", network, r, err)
		}
		t.Logf("network %s: %q", network, r.Stdout)
		if _, err := ke.c.Destroy(ctx, api.DestroyRequest{Id: env.Id}); err != nil {
			t.Fatal(err)
		}
	}

	// MAGI_TEST_KUBE_ARM64_NODE names an arm64 node for the arm64 build.
	if node := os.Getenv("MAGI_TEST_KUBE_ARM64_NODE"); node != "" {
		env := ke.create(api.EnvironmentSpec{Image: "busybox:1.37", Host: &node})
		r, err := ke.c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"uname", "-m"}})
		if err != nil || strings.TrimSpace(string(r.Stdout)) != "aarch64" {
			t.Fatalf("arm64: %+v %v", r, err)
		}
		ke.c.Destroy(ctx, api.DestroyRequest{Id: env.Id})
	}

	// A failed download must stop the environment.
	k.PublicURL = "http://" + ip + ":8080/nothing"
	e, err := ke.c.Create(ctx, api.CreateRequest{Spec: api.EnvironmentSpec{Image: "busybox:1.37"}, WaitReadyMs: u64(120000)})
	defer k.Destroy(context.Background(), e.Id)
	if err != nil || e.Status != "failed" {
		t.Fatalf("bad download: %+v %v", e, err)
	}
	t.Logf("bad download message: %s", deref(e.Message))
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// TestKubernetesDevImage runs MAGI_TEST_KUBE_DEV_IMAGE, such as
// localhost:30500/magi/dev-env:<tag>, with MAGI_TEST_KUBE_PULL_SECRET.
func TestKubernetesDevImage(t *testing.T) {
	image := os.Getenv("MAGI_TEST_KUBE_DEV_IMAGE")
	if image == "" {
		t.Skip("MAGI_TEST_KUBE_DEV_IMAGE is not set")
	}
	ke := kubeSetup(t)
	ctx := context.Background()
	env := ke.create(api.EnvironmentSpec{Image: image})
	r, err := ke.c.Exec(ctx, api.ExecRequest{Environment: env.Id, Argv: []string{"sh", "-c",
		"id -u; pwd; touch /work/x; go version; python3 -m venv /tmp/v && echo venv; rg --version | head -1; git --version; jq --version; make --version | head -1"}})
	if err != nil || r.ExitCode != 0 || !strings.HasPrefix(string(r.Stdout), "1000\n/work\n") || !strings.Contains(string(r.Stdout), "venv\n") {
		t.Fatalf("dev image: %+v %v stdout=%q stderr=%q", r, err, r.Stdout, r.Stderr)
	}
	t.Logf("dev image: %q", r.Stdout)
	ke.c.Destroy(ctx, api.DestroyRequest{Id: env.Id})
}
