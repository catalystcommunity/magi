package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
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
