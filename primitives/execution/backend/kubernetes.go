package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
)

// Delivery modes put envd into a Pod without an envd image.
const (
	// DeliverFetch: the init container downloads envd from the control plane
	// (PublicURL/envd/linux-<arch>) and checks its SHA-256.
	DeliverFetch = "fetch"
	// DeliverPush: the init container waits, and the control plane writes
	// envd into it through the Kubernetes exec API.
	DeliverPush = "push"
)

// Connection modes reach envd.
const (
	ConnectDirect      = "direct"       // Pod IP. The control plane runs in the cluster.
	ConnectPortForward = "port-forward" // Kubernetes port-forward. Works from outside the cluster.
)

const (
	initName   = "envd-init"
	mainName   = "env"
	envdDir    = "/magi"
	secretDir  = "/magi-secret"
	labelEnv   = "magi.environment"
	labelPart  = "app.kubernetes.io/part-of"
	defaultCPU = 1000
	defaultMem = 2048
)

// Kubernetes runs each environment as a Pod in Namespace.
type Kubernetes struct {
	Client     kubernetes.Interface
	Config     *rest.Config
	Namespace  string
	Delivery   string
	Connect    string
	PublicURL  string // fetch: control plane base URL as seen from Pods
	FetchImage string // init container image, such as busybox
	PullSecret string // optional image pull secret name
	// ControlNamespace is where the control plane runs. With network
	// "none" and fetch delivery, Pods can reach only this namespace.
	ControlNamespace string
	Envd             *Binaries

	mu      sync.Mutex
	pushing map[string]bool
	fwds    map[string]*forward
}

var _ Backend = (*Kubernetes)(nil)

// RestConfig uses the in-cluster config, or kubeconfig (KUBECONFIG or
// ~/.kube/config) with context when not in a cluster.
func RestConfig(kubeconfig, kubeContext string) (*rest.Config, bool, error) {
	if kubeconfig == "" && kubeContext == "" {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, true, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	c, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	return c, false, err
}

// PodName is the name of the Pod, Secret, and NetworkPolicy of id.
func PodName(id string) string { return "magi-" + strings.ReplaceAll(strings.ToLower(id), "_", "-") }

func (k *Kubernetes) Create(ctx context.Context, id string, spec api.EnvironmentSpec, token string) error {
	sec, pod, np, err := k.Objects(id, spec, token)
	if err != nil {
		return err
	}
	if _, err := k.Client.CoreV1().Secrets(k.Namespace).Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create secret: %w", err)
	}
	if np != nil {
		if _, err := k.Client.NetworkingV1().NetworkPolicies(k.Namespace).Create(ctx, np, metav1.CreateOptions{}); err != nil {
			k.Destroy(context.WithoutCancel(ctx), id)
			return fmt.Errorf("create network policy: %w", err)
		}
	}
	if _, err := k.Client.CoreV1().Pods(k.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		k.Destroy(context.WithoutCancel(ctx), id)
		return fmt.Errorf("create pod: %w", err)
	}
	return nil
}

func quantity(v *uint64, def uint64, unit string) resource.Quantity {
	n := def
	if v != nil && *v != 0 {
		n = *v
	}
	return resource.MustParse(strconv.FormatUint(n, 10) + unit)
}

// Objects returns the Secret, Pod, and (for network "none") NetworkPolicy
// of an environment.
func (k *Kubernetes) Objects(id string, spec api.EnvironmentSpec, token string) (*corev1.Secret, *corev1.Pod, *netv1.NetworkPolicy, error) {
	name := PodName(id)
	labels := map[string]string{labelPart: "magi", labelEnv: id, "app.kubernetes.io/component": "environment"}
	meta := metav1.ObjectMeta{Name: name, Namespace: k.Namespace, Labels: labels}
	workdir := "/work"
	if spec.Workdir != nil && *spec.Workdir != "" {
		workdir = *spec.Workdir
	}

	sec := &corev1.Secret{ObjectMeta: meta, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"token": []byte(token)}}
	var env []corev1.EnvVar
	for kname, v := range spec.Env {
		key := "env." + kname
		sec.Data[key] = []byte(v)
		env = append(env, corev1.EnvVar{Name: kname, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key},
		}})
	}

	script, initEnv, err := k.initScript()
	if err != nil {
		return nil, nil, nil, err
	}
	no := false
	sc := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &no,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	res := corev1.ResourceList{
		corev1.ResourceCPU:    quantity(spec.CpuMillis, defaultCPU, "m"),
		corev1.ResourceMemory: quantity(spec.MemoryMb, defaultMem, "Mi"),
	}
	work := corev1.EmptyDirVolumeSource{}
	if spec.DiskMb != nil && *spec.DiskMb != 0 {
		q := quantity(spec.DiskMb, 0, "Mi")
		res[corev1.ResourceEphemeralStorage] = q
		work.SizeLimit = &q
	}
	small := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}
	grace := int64(5)
	mode := int32(0o444)
	pod := &corev1.Pod{
		ObjectMeta: meta,
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			AutomountServiceAccountToken:  &no,
			EnableServiceLinks:            &no,
			TerminationGracePeriodSeconds: &grace,
			InitContainers: []corev1.Container{{
				Name: initName, Image: k.FetchImage, Command: []string{"sh", "-c", script}, Env: initEnv,
				VolumeMounts:    []corev1.VolumeMount{{Name: "envd", MountPath: envdDir}},
				SecurityContext: sc,
				Resources:       corev1.ResourceRequirements{Requests: small, Limits: small},
			}},
			Containers: []corev1.Container{{
				Name: mainName, Image: spec.Image,
				Command:    []string{envdDir + "/envd", "-listen", fmt.Sprintf(":%d", EnvdPort), "-token-file", secretDir + "/token", "-dir", workdir},
				WorkingDir: workdir,
				Env:        env,
				Ports:      []corev1.ContainerPort{{Name: "envd", ContainerPort: EnvdPort}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "envd", MountPath: envdDir, ReadOnly: true},
					{Name: "token", MountPath: secretDir, ReadOnly: true},
					{Name: "work", MountPath: workdir},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(EnvdPort)}},
					PeriodSeconds: 1, FailureThreshold: 3,
				},
				SecurityContext: sc,
				Resources:       corev1.ResourceRequirements{Requests: res, Limits: res},
			}},
			Volumes: []corev1.Volume{
				{Name: "envd", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &work}},
				{Name: "token", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: name, DefaultMode: &mode, Items: []corev1.KeyToPath{{Key: "token", Path: "token"}},
				}}},
			},
		},
	}
	if spec.Host != nil && *spec.Host != "" {
		pod.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": *spec.Host}
	}
	if k.PullSecret != "" {
		pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: k.PullSecret}}
	}

	var np *netv1.NetworkPolicy
	if spec.Network != nil && *spec.Network == "none" {
		np = &netv1.NetworkPolicy{ObjectMeta: meta, Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{labelEnv: id}},
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeEgress},
		}}
		if k.Delivery == DeliverFetch {
			// The init container must download envd. Allow DNS and the
			// control plane namespace only.
			port, err := urlPort(k.PublicURL)
			if err != nil {
				return nil, nil, nil, err
			}
			tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
			dns := intstr.FromInt32(53)
			cp := intstr.FromInt32(port)
			ns := func(n string) []netv1.NetworkPolicyPeer {
				return []netv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": n}}}}
			}
			np.Spec.Egress = []netv1.NetworkPolicyEgressRule{
				{To: ns("kube-system"), Ports: []netv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns}}},
				{To: ns(k.ControlNamespace), Ports: []netv1.NetworkPolicyPort{{Protocol: &tcp, Port: &cp}}},
			}
		}
	}
	return sec, pod, np, nil
}

func urlPort(u string) (int32, error) {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return 0, fmt.Errorf("public URL %q is not valid", u)
	}
	if s := p.Port(); s != "" {
		n, err := strconv.ParseInt(s, 10, 32)
		return int32(n), err
	}
	if p.Scheme == "https" {
		return 443, nil
	}
	return 80, nil
}

const pushWait = 600 // seconds that a push init container waits

func (k *Kubernetes) initScript() (string, []corev1.EnvVar, error) {
	switch k.Delivery {
	case DeliverPush:
		return fmt.Sprintf(`i=0; until [ -f %[1]s/envd.ready ]; do i=$((i+1)); if [ $i -gt %[2]d ]; then echo "envd was not delivered" >&2; exit 1; fi; sleep 1; done`, envdDir, pushWait), nil, nil
	case DeliverFetch:
		if k.PublicURL == "" {
			return "", nil, errors.New("fetch delivery needs MAGI_EXEC_PUBLIC_URL")
		}
		env := []corev1.EnvVar{{Name: "MAGI_ENVD_URL", Value: strings.TrimRight(k.PublicURL, "/")}}
		for _, arch := range []string{"amd64", "arm64"} {
			sum, _ := k.Envd.Sum("linux-" + arch) // A missing build fails the check in the Pod.
			env = append(env, corev1.EnvVar{Name: "MAGI_ENVD_SHA256_" + strings.ToUpper(arch), Value: sum})
		}
		return `set -eu
case "$(uname -m)" in
x86_64|amd64) a=amd64; s="$MAGI_ENVD_SHA256_AMD64" ;;
aarch64|arm64) a=arm64; s="$MAGI_ENVD_SHA256_ARM64" ;;
*) echo "unsupported machine $(uname -m)" >&2; exit 1 ;;
esac
wget -q -O /magi/envd.part "$MAGI_ENVD_URL/envd/linux-$a"
echo "$s  /magi/envd.part" | sha256sum -c -
chmod 0755 /magi/envd.part
mv /magi/envd.part /magi/envd`, env, nil
	}
	return "", nil, fmt.Errorf("unknown envd delivery %q", k.Delivery)
}

// Reasons that do not resolve without a change to the request.
var fatalWaiting = map[string]bool{
	"ImagePullBackOff": true, "InvalidImageName": true, "ErrImageNeverPull": true,
	"CreateContainerConfigError": true, "CreateContainerError": true, "CrashLoopBackOff": true,
}

func containerProblem(sts []corev1.ContainerStatus) (fatal bool, msg string) {
	for _, s := range sts {
		if w := s.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" && w.Reason != "ContainerCreating" {
			msg = fmt.Sprintf("%s: %s %s", s.Name, w.Reason, w.Message)
			if fatalWaiting[w.Reason] {
				return true, msg
			}
		}
		if t := s.State.Terminated; t != nil && t.ExitCode != 0 {
			return true, fmt.Sprintf("%s: exited %d (%s) %s", s.Name, t.ExitCode, t.Reason, t.Message)
		}
	}
	return false, msg
}

func (k *Kubernetes) Status(ctx context.Context, id string) (Status, error) {
	name := PodName(id)
	pod, err := k.Client.CoreV1().Pods(k.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Status{State: Gone, Message: "pod not found"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	if pod.DeletionTimestamp != nil {
		return Status{State: Gone, Message: "pod is being deleted"}, nil
	}
	if fatal, msg := containerProblem(pod.Status.InitContainerStatuses); fatal {
		return Status{State: Failed, Message: strings.TrimSpace(msg + " " + k.logTail(ctx, name, initName))}, nil
	}
	fatal, msg := containerProblem(pod.Status.ContainerStatuses)
	if fatal {
		return Status{State: Failed, Message: msg}, nil
	}
	switch pod.Status.Phase {
	case corev1.PodFailed, corev1.PodSucceeded:
		return Status{State: Failed, Message: strings.TrimSpace("pod " + string(pod.Status.Phase) + " " + pod.Status.Message)}, nil
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && msg == "" {
			msg = strings.TrimSpace(c.Reason + " " + c.Message)
		}
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && pod.Status.PodIP != "" {
			host := pod.Status.PodIP
			if k.Connect == ConnectPortForward {
				host = name
			}
			return Status{State: Ready, Endpoint: "http://" + net.JoinHostPort(host, strconv.Itoa(EnvdPort))}, nil
		}
	}
	if k.Delivery == DeliverPush {
		for _, s := range pod.Status.InitContainerStatuses {
			if s.Name == initName && s.State.Running != nil {
				if err := k.push(ctx, name); err != nil {
					msg = "deliver envd: " + err.Error()
				}
			}
		}
	}
	return Status{State: Creating, Message: msg}, nil
}

// logTail returns the last lines of a container log, or "" if it cannot.
func (k *Kubernetes) logTail(ctx context.Context, pod, container string) string {
	n, limit := int64(5), int64(2048)
	b, err := k.Client.CoreV1().Pods(k.Namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: &n, LimitBytes: &limit}).DoRaw(ctx)
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// push writes envd into the waiting init container. It runs once at a time
// per Pod; a repeated push replaces the file atomically.
func (k *Kubernetes) push(ctx context.Context, pod string) error {
	k.mu.Lock()
	if k.pushing == nil {
		k.pushing = map[string]bool{}
	}
	if k.pushing[pod] {
		k.mu.Unlock()
		return nil
	}
	k.pushing[pod] = true
	k.mu.Unlock()
	defer func() { k.mu.Lock(); delete(k.pushing, pod); k.mu.Unlock() }()

	machine, err := k.exec(ctx, pod, []string{"uname", "-m"}, nil)
	if err != nil {
		return err
	}
	platform := "linux-" + Arch(string(machine))
	path, err := k.Envd.Path(platform)
	if err != nil {
		return err
	}
	sum, err := k.Envd.Sum(platform)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	script := fmt.Sprintf(`set -e; cat > %[1]s/envd.part; echo "$1  %[1]s/envd.part" | sha256sum -c - >/dev/null; chmod 0755 %[1]s/envd.part; mv %[1]s/envd.part %[1]s/envd; touch %[1]s/envd.ready`, envdDir)
	_, err = k.exec(ctx, pod, []string{"sh", "-c", script, "sh", sum}, f)
	return err
}

func (k *Kubernetes) exec(ctx context.Context, pod string, cmd []string, stdin io.Reader) ([]byte, error) {
	return k.Exec(ctx, pod, initName, cmd, stdin)
}

// Exec runs cmd in a container of a Pod in Namespace and returns stdout.
func (k *Kubernetes) Exec(ctx context.Context, pod, container string, cmd []string, stdin io.Reader) ([]byte, error) {
	req := k.Client.CoreV1().RESTClient().Post().Resource("pods").Namespace(k.Namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: container, Command: cmd, Stdin: stdin != nil, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	sp, err := remotecommand.NewSPDYExecutor(k.Config, http.MethodPost, req.URL())
	if err != nil {
		return nil, err
	}
	ws, err := remotecommand.NewWebSocketExecutor(k.Config, http.MethodGet, req.URL().String())
	if err != nil {
		return nil, err
	}
	ex, err := remotecommand.NewFallbackExecutor(ws, sp, func(err error) bool {
		return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out, errb bytes.Buffer
	if err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: &out, Stderr: &errb}); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

func (k *Kubernetes) Destroy(ctx context.Context, id string) error {
	name := PodName(id)
	k.stopForward(name)
	var errs []error
	zero := int64(0)
	ignore := func(err error) {
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		}
	}
	ignore(k.Client.CoreV1().Pods(k.Namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero}))
	ignore(k.Client.CoreV1().Secrets(k.Namespace).Delete(ctx, name, metav1.DeleteOptions{}))
	ignore(k.Client.NetworkingV1().NetworkPolicies(k.Namespace).Delete(ctx, name, metav1.DeleteOptions{}))
	return errors.Join(errs...)
}

// HTTP returns a client that dials Pod IPs, or opens a port-forward for
// "http://<pod>:7070" endpoints.
func (k *Kubernetes) HTTP() *http.Client {
	if k.Connect != ConnectPortForward {
		return &http.Client{}
	}
	var d net.Dialer
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := k.forward(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("port-forward %s: %w", host, err)
			}
			return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
		},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     time.Minute,
	}}
}

type forward struct {
	port uint16
	stop chan struct{}
	done chan struct{}
}

func (k *Kubernetes) forward(ctx context.Context, pod string) (uint16, error) {
	k.mu.Lock()
	if f := k.fwds[pod]; f != nil {
		select {
		case <-f.done:
			delete(k.fwds, pod)
		default:
			k.mu.Unlock()
			return f.port, nil
		}
	}
	k.mu.Unlock()

	rt, upgrader, err := spdy.RoundTripperFor(k.Config)
	if err != nil {
		return 0, err
	}
	u := k.Client.CoreV1().RESTClient().Post().Resource("pods").Namespace(k.Namespace).Name(pod).SubResource("portforward").URL()
	var dialer httpstream.Dialer = spdy.NewDialer(upgrader, &http.Client{Transport: rt}, http.MethodPost, u)
	if ws, err := portforward.NewSPDYOverWebsocketDialer(u, k.Config); err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	}
	f := &forward{stop: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", EnvdPort)}, f.stop, ready, io.Discard, io.Discard)
	if err != nil {
		return 0, err
	}
	errc := make(chan error, 1)
	go func() { errc <- pf.ForwardPorts(); close(f.done) }()
	select {
	case <-ready:
	case err := <-errc:
		return 0, err
	case <-ctx.Done():
		close(f.stop)
		return 0, ctx.Err()
	}
	ports, err := pf.GetPorts()
	if err != nil || len(ports) == 0 {
		close(f.stop)
		return 0, fmt.Errorf("no forwarded port: %v", err)
	}
	f.port = ports[0].Local
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fwds == nil {
		k.fwds = map[string]*forward{}
	}
	if old := k.fwds[pod]; old != nil {
		close(f.stop)
		return old.port, nil
	}
	k.fwds[pod] = f
	return f.port, nil
}

func (k *Kubernetes) stopForward(pod string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if f := k.fwds[pod]; f != nil {
		close(f.stop)
		delete(k.fwds, pod)
	}
}

// Close stops every port-forward.
func (k *Kubernetes) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for name, f := range k.fwds {
		close(f.stop)
		delete(k.fwds, name)
	}
}
