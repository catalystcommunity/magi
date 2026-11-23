package compose

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

// EgressAuto lets Magi permit a destination that the policy of the proxy
// does not permit, with no person (docs/decisions/0014). A permitted
// destination is not a permitted request: the proxy still inspects each
// request, removes secrets, and asks for a verdict.
type EgressAuto struct {
	// Sources permits the hosts of sourceHosts.
	Sources bool `json:"sources,omitempty"`
	// Hosts are more host patterns ("name", "*.name") that Magi permits.
	Hosts []string `json:"hosts,omitempty"`
	// Model lets the decision model (task "decide" of the catalog) permit a
	// host that it knows as a public source of software, documentation, or
	// reference data. Limit is the least probability that permits; 0 is
	// autoLimit.
	Model bool    `json:"model,omitempty"`
	Limit float64 `json:"limit,omitempty"`
}

// autoBy is the decider in the record of an automatic decision.
const autoBy = "magi:auto"

const (
	autoModelTimeout = 10 * time.Second // the proxy waits 60 s for the answer
	autoAskAgain     = 30 * time.Minute // a host that the model did not permit
)

// sourceHosts are public hosts that give software to all: package indexes,
// toolchains, and operating system packages. A task reads from them; the
// request rules decide each request that is not a read.
var sourceHosts = []string{
	// Go
	"proxy.golang.org", "sum.golang.org", "go.dev", "golang.org", "pkg.go.dev", "dl.google.com",
	// Python
	"pypi.org", "files.pythonhosted.org", "docs.python.org", "astral.sh", "astral-sh.github.io",
	// JavaScript
	"registry.npmjs.org", "nodejs.org",
	// Rust
	"crates.io", "index.crates.io", "static.crates.io", "static.rust-lang.org", "sh.rustup.rs", "doc.rust-lang.org",
	// Java and Kotlin
	"repo.maven.apache.org", "repo1.maven.org", "plugins.gradle.org", "services.gradle.org", "downloads.gradle.org",
	"maven-central.storage-download.googleapis.com",
	// Ruby, Zig
	"rubygems.org", "index.rubygems.org", "ziglang.org",
	// Operating system packages
	"deb.debian.org", "security.debian.org", "archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com",
	"dl-cdn.alpinelinux.org", "apt.postgresql.org", "ftp.postgresql.org",
	// Source and release files
	"github.com", "codeload.github.com", "raw.githubusercontent.com", "objects.githubusercontent.com",
	"release-assets.githubusercontent.com", "media.githubusercontent.com",
	// Container images
	"registry-1.docker.io", "auth.docker.io", "production.cloudflare.docker.com", "ghcr.io", "pkg-containers.githubusercontent.com",
}

// knownDomains are more domains of well-known projects and companies. Magi
// does not permit them from this list. It uses them, with the domains of
// sourceHosts, to find a name that looks like a known name.
var knownDomains = []string{"python.org", "npmjs.com", "docker.com", "rust-lang.org", "microsoft.com", "mozilla.org",
	"wikipedia.org", "kubernetes.io", "apache.org", "gnu.org", "kernel.org", "google.com", "googleapis.com", "amazon.com",
	"amazonaws.com", "cloudflare.com", "gitlab.com", "bitbucket.org", "openai.com", "anthropic.com", "huggingface.co",
	"stackoverflow.com", "sqlite.org", "nginx.org", "debian.org", "ubuntu.com", "alpinelinux.org", "postgresql.org",
	"gradle.org", "nodejs.org", "pythonhosted.org", "githubusercontent.com", "github.io"}

// domainOf gives the domain that a host belongs to: the last two labels,
// or three when the second is a common label below a country ("co.uk").
// It is not the public suffix list; it is sufficient to compare names.
func domainOf(host string) string {
	l := strings.Split(host, ".")
	n := 2
	if len(l) >= 3 && len(l[len(l)-1]) == 2 && slices.Contains([]string{"co", "com", "org", "net", "ac", "gov", "edu"}, l[len(l)-2]) {
		n = 3
	}
	if len(l) <= n {
		return host
	}
	return strings.Join(l[len(l)-n:], ".")
}

// lookAlike reports whether a host is not in a known domain but looks like
// one: its domain is one or two letters from a known domain ("pypl.org"),
// or the host has the name of a known domain in it
// ("pypi.org.mirror.example", "registry-npmjs.org"). A model reads such a
// name as the known name, so code sends it to a person.
func lookAlike(host string) bool {
	d := domainOf(host)
	known := slices.Clone(knownDomains)
	for _, h := range sourceHosts {
		known = append(known, domainOf(h))
	}
	if slices.Contains(known, d) {
		return false
	}
	for _, k := range known {
		if editDistance(d, k) <= 2 {
			return true
		}
		if name, _, _ := strings.Cut(k, "."); len(name) >= 4 && strings.Contains(host, name) {
			return true
		}
	}
	return false
}

// editDistance is the number of letters to add, remove, or change to make
// b from a.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// neverAutoHosts are host patterns of popular sites that take content from
// anyone, run code of anyone, or follow users: document and file services,
// code playgrounds, trackers. Their domains are in each popularity list and
// in the text that a model learned from, so a model gives some of them a
// high probability. A person decides them.
var neverAutoHosts = []string{
	// Documents, files, and forms of anyone
	"docs.google.com", "drive.google.com", "forms.google.com", "sheets.google.com", "sites.google.com", "script.google.com",
	"colab.research.google.com", "*.googleusercontent.com", "dropbox.com", "*.dropbox.com", "*.dropboxusercontent.com",
	"onedrive.live.com", "*.sharepoint.com", "notion.so", "*.notion.so", "*.notion.site", "airtable.com", "*.airtable.com",
	// Trackers and tag managers
	"*.google-analytics.com", "google-analytics.com", "*.googletagmanager.com", "googletagmanager.com", "*.doubleclick.net",
	"*.googlesyndication.com", "*.clarity.ms", "clarity.ms", "*.hotjar.com", "*.segment.io", "*.segment.com", "*.mixpanel.com",
	// Sites that run or show the code of anyone
	"godbolt.org", "*.godbolt.org", "observablehq.com", "*.observablehq.com", "play.rust-lang.org", "play.golang.org",
	"replit.com", "*.replit.com", "*.repl.co", "codepen.io", "*.codepen.io", "jsfiddle.net", "*.jsfiddle.net",
	"codesandbox.io", "*.codesandbox.io", "*.csb.app", "stackblitz.com", "*.stackblitz.io", "glitch.com", "*.glitch.me",
	"dotnetfiddle.net", "onecompiler.com", "ideone.com", "tio.run",
	// Chat
	"matrix.org", "*.matrix.org", "web.telegram.org", "api.telegram.org", "discord.com", "*.discord.com", "*.slack.com",
}

// neverAuto reports whether a person must decide the host.
func neverAuto(host string) bool { return anyHost(neverAutoHosts, host) }

// reservedSuffixes are more name endings that are not public web names
// (privateSuffixes of search.go has the private ones). Magi never permits
// them automatically.
var reservedSuffixes = []string{".invalid", ".test", ".example", ".onion"}

// autoHost gives the host of a destination that an automatic decision can
// permit: a public DNS name on the HTTP or HTTPS port. An address, a name
// with one label, a private name, or a name in the punycode form (it can
// look like a different name) always goes to a person.
func autoHost(dest string) (string, bool) {
	host, port, err := net.SplitHostPort(dest)
	if err != nil || (port != "443" && port != "80") {
		return "", false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return "", false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "xn--") || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return "", false
		}
	}
	for _, s := range slices.Concat(privateSuffixes, reservedSuffixes) {
		if strings.HasSuffix(host, s) {
			return "", false
		}
	}
	return host, true
}

// egressAutoAsked keeps the hosts that the model did not permit, so that
// the same task does not ask the model each minute.
type egressAutoAsked struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (a *egressAutoAsked) recent(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(a.at[key]) < autoAskAgain
}

func (a *egressAutoAsked) set(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.at == nil {
		a.at = map[string]time.Time{}
	}
	a.at[key] = time.Now()
}

// autoDestination gives the reason to permit a destination with no person,
// or "" when a person must decide.
func (e *Egress) autoDestination(ctx context.Context, req magiapi.EgressDestinationRequest) string {
	auto := e.rules().Auto
	if auto == nil {
		return ""
	}
	host, ok := autoHost(req.Destination)
	if !ok {
		return ""
	}
	if auto.Sources && anyHost(sourceHosts, host) {
		return "a public software source in the list of Magi"
	}
	if anyHost(auto.Hosts, host) {
		return "a host in the automatic list of the installation"
	}
	if !auto.Model || (e.S.Router == nil && e.S.hostDecide == nil) {
		return ""
	}
	if neverAuto(host) {
		e.S.Log.Info("egress automatic decision: the host is in the list that a person decides", "host", host)
		return ""
	}
	if lookAlike(host) {
		e.S.Log.Info("egress automatic decision: the name looks like a known name", "host", host)
		return ""
	}
	// The model decides only for a task: the task tells what the host is
	// for. A different environment (a persona tool, a service) has none.
	env, err := e.environment(ctx, magiapi.EgressEnvironmentRequest{Environment: req.Environment})
	if err != nil || env.Task == nil {
		return ""
	}
	run, err := e.S.Workflow.GetRun(e.S.AsService(ctx), workflowapi.GetRunRequest{Id: *env.Task})
	if err != nil {
		return ""
	}
	asked := run.Id + "\x00" + host
	if e.S.egressAsked.recent(asked) {
		return ""
	}
	mctx, cancel := context.WithTimeout(ctx, autoModelTimeout)
	defer cancel()
	limit := auto.Limit
	if limit <= 0 {
		limit = autoLimit
	}
	why, err := e.autoModel(mctx, host, run, limit)
	if err != nil {
		e.S.Log.Warn("egress automatic decision", "destination", req.Destination, "err", err)
	}
	if why == "" {
		e.S.egressAsked.set(asked)
	}
	return why
}

// hostQuestion is the question of the automatic decision. The same text is
// in evals/egress/decide_destinations.py: change them together.
const hostQuestion = "Is this host the official site of a well-known public source of software, documentation, or reference data? " +
	"Answer no for a host that you do not know, for a host that can receive data from anyone (a paste site, a file " +
	"share, a webhook or request collector, a tunnel, a URL shortener, a chat or mail service), for a name that looks " +
	"like a known name but has different letters or a different domain, and for the host of one person or one small company. " +
	"The popularity line is evidence: an invented or look-alike host is not in the list, but many popular sites are not sources."

// hostQuestionID names the question for the calibration of the catalog.
const hostQuestionID = "egress-known-source"

// autoLimit is the default least probability of "yes" that permits a host.
// It is the measured limit of the adapter that was trained with the
// popularity line: no wrong host in the two halves of the corpus
// (evals/egress/README.md). Measure again after a new training.
const autoLimit = 0.97

// hostState is the state of the question. It has the host, the line of
// the popularity list, and the title of the task, and no data from the
// environment but the host name.
func hostState(host, popularity, title string) string {
	return fmt.Sprintf("A task environment wants to connect to a host that the network policy does not list.\nHost: %s\nPort: 443\n%s\nTask: %s",
		host, popularity, trunc(title, 200))
}

// autoModel asks the decision model (task "decide") about a host. It gives
// the reason to permit the host when the probability of "yes" is at least
// the limit; a smaller probability, and no answer, is "". The decision
// model gives a probability, not text: a chat model gave the confidence
// "high" for each of its verdicts, also for the wrong ones.
func (e *Egress) autoModel(ctx context.Context, host string, run workflowapi.Run, limit float64) (string, error) {
	decide := e.S.hostDecide
	if decide == nil {
		decide = func(ctx context.Context, state string) (float64, string, error) {
			res, err := e.S.Decide(ctx, DecisionTask, nil, false, state,
				[]magiapi.DecisionQuestion{{Id: hostQuestionID, Type: "noul", Question: hostQuestion}})
			if err != nil {
				return 0, "", err
			}
			if len(res.Answers) != 1 || len(res.Answers[0].Probs) == 0 {
				return 0, res.Model, fmt.Errorf("the decision model gave no probability")
			}
			return res.Answers[0].Probs[0], res.Model, nil
		}
	}
	pop, err := e.S.popularity()
	if err != nil {
		// The model was trained with the popularity line. Without the list
		// its probability has no measured meaning: a person decides.
		return "", err
	}
	yes, mdl, err := decide(ctx, hostState(host, pop.line(host), run.Vars["title"]))
	if err != nil {
		return "", err
	}
	// The probability is in the log for each host: a person who looks at an
	// open review can see why Magi did not decide it.
	e.S.Log.Info("egress automatic decision: model", "host", host, "model", mdl, "yes", yes, "limit", limit)
	if yes < limit {
		return "", nil
	}
	return fmt.Sprintf("the decision model %s gives %.3f that the host is a known public source (limit %.2f)", mdl, yes, limit), nil
}

// permitAuto records an automatic decision: a report, so that an admin
// sees it in the reviews, and the decision. An admin can decide the review
// again (for example "block").
func (e *Egress) permitAuto(ctx context.Context, req magiapi.EgressDestinationRequest, why string) error {
	f := magiapi.EgressFlow{Environment: req.Environment, Destination: req.Destination, Method: "CONNECT", Url: "tcp://" + req.Destination}
	p := reviewKey("destination", f)
	key, ms := p.key(), nowMs()
	owner := ""
	if env, err := e.environment(ctx, magiapi.EgressEnvironmentRequest{Environment: req.Environment}); err == nil {
		owner = env.Owner
	}
	rep := egressReport{Key: key, Kind: "destination", Owner: owner, Environment: req.Environment, Destination: req.Destination,
		Method: p.method, URL: f.Url, Headers: []magiapi.EgressReviewHeader{}, Reason: "permitted with no person: " + why, By: autoBy, Ms: ms}
	if err := putRecord(ctx, e.S, kindReport, key, rep); err != nil {
		return err
	}
	d := egressDecision{Key: key, Kind: "destination", Decision: "allow-host", Host: p.host, Method: p.method, Path: p.path,
		By: autoBy, Note: why, Ms: ms}
	if err := putRecord(ctx, e.S, kindDecision, key, d); err != nil {
		return err
	}
	e.S.reviews().load(ctx, e.S, true)
	e.S.Log.Info("egress destination permitted automatically", "destination", req.Destination, "environment", req.Environment, "why", why)
	return nil
}
