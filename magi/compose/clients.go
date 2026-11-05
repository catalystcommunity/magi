// Package compose connects the Magi primitives into one system for people.
// It is the only package that uses more than one primitive.
package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	executionapi "github.com/catalystcommunity/magi/primitives/execution/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	releaseapi "github.com/catalystcommunity/magi/primitives/release/goapi"
	sourceapi "github.com/catalystcommunity/magi/primitives/source/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"

	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/magi/model"
)

// Persona is the subject that Magi's own utterances use.
const Persona = "persona:magi"

// Config selects the primitive endpoints and the service credential.
type Config struct {
	Artifacts, Conversation, Execution, Knowledge, Source, Speech, Workflow string
	// ServiceToken authenticates background work. It needs the act-as
	// scopes of each primitive and workflow:worker.
	ServiceToken string
	// Repo is the default repository for code changes.
	Repo string
	// EnvImage is the execution environment image for code changes.
	EnvImage string
	// EnvNetwork is the execution network for code changes: "proxy" when
	// the egress proxy is set (Egress), else "egress".
	EnvNetwork string
	// Runner names the default runner for code changes ("" runs them in
	// the cluster). See RunnerService.
	Runner string
	// Publisher names the runner that pushes merged changes upstream. ""
	// uses a connected runner that publishes the repository.
	Publisher string
	// Admins are user subjects that can make operator calls.
	Admins []string
	// LexiconEditors are user subjects that can change the household
	// lexicon. Empty means the admins.
	LexiconEditors []string
	// CheckCommand runs in the repository after the apprentice works.
	CheckCommand string
	// Release is the release controller endpoint; empty turns releases off.
	Release    string
	ReleaseCfg ReleaseConfig
	// Egress is the API of the egress proxy. When it is set, code-change
	// environments use network "proxy" (EnvNetwork).
	Egress string
	// EgressRules come from the file MAGI_EGRESS_RULES; nil reviews each
	// flow that the magi-review plugin asks about.
	EgressRules *EgressRules
	// EgressProxy is the proxy route of runner sandboxes
	// (MAGI_EGRESS_PROXY, MAGI_EGRESS_ENV_KEY_FILE, MAGI_EGRESS_CA, and
	// MAGI_EGRESS_SSH_PUB for the SSH host key of the proxy); nil
	// lets them use the network of the runner machine.
	EgressProxy *EgressProxy
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// list splits a comma-separated list and drops empty items.
func list(v string) []string {
	var out []string
	for _, a := range strings.Split(v, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// ConfigFromEnv reads MAGI_* variables.
func ConfigFromEnv() (Config, error) {
	c := Config{
		Artifacts:    env("MAGI_ARTIFACTS_URL", "http://artifacts:8080"),
		Conversation: env("MAGI_CONVERSATION_URL", "http://conversation:8080"),
		Execution:    env("MAGI_EXECUTION_URL", "http://execution:8080"),
		Knowledge:    env("MAGI_KNOWLEDGE_URL", "http://knowledge:8080"),
		Source:       env("MAGI_SOURCE_URL", "http://source:8080"),
		Speech:       env("MAGI_SPEECH_URL", "http://speech:8080"),
		Workflow:     env("MAGI_WORKFLOW_URL", "http://workflow:8080"),
		Repo:         env("MAGI_REPO", "magi"),
		EnvImage:     env("MAGI_ENV_IMAGE", "golang:1.26-bookworm"),
		Runner:       os.Getenv("MAGI_RUNNER"),
		Publisher:    os.Getenv("MAGI_PUBLISHER"),
		CheckCommand: env("MAGI_CHECK_COMMAND", "./tools/check.sh"),
	}
	c.Admins = list(os.Getenv("MAGI_ADMINS"))
	c.LexiconEditors = list(os.Getenv("MAGI_LEXICON_EDITORS"))
	c.Release = os.Getenv("MAGI_RELEASE_URL")
	c.Egress = os.Getenv("MAGI_EGRESS_URL")
	if addr := os.Getenv("MAGI_EGRESS_PROXY"); addr != "" {
		key, err := os.ReadFile(os.Getenv("MAGI_EGRESS_ENV_KEY_FILE"))
		if err != nil {
			return c, fmt.Errorf("MAGI_EGRESS_ENV_KEY_FILE: %w", err)
		}
		ca, err := os.ReadFile(os.Getenv("MAGI_EGRESS_CA"))
		if err != nil {
			return c, fmt.Errorf("MAGI_EGRESS_CA: %w", err)
		}
		k := []byte(strings.TrimSpace(string(key)))
		if len(k) < 32 {
			return c, errors.New("MAGI_EGRESS_ENV_KEY_FILE: the key must have at least 32 bytes")
		}
		c.EgressProxy = &EgressProxy{Addr: addr, Key: k, CA: ca}
		if f := os.Getenv("MAGI_EGRESS_SSH_PUB"); f != "" {
			pub, err := os.ReadFile(f)
			if err != nil {
				return c, fmt.Errorf("MAGI_EGRESS_SSH_PUB: %w", err)
			}
			c.EgressProxy.SSHPub = strings.TrimSpace(string(pub))
		}
	}
	c.EnvNetwork = os.Getenv("MAGI_ENV_NETWORK")
	if c.EnvNetwork == "" {
		c.EnvNetwork = "egress"
		if c.Egress != "" {
			c.EnvNetwork = "proxy"
		}
	}
	if f := os.Getenv("MAGI_EGRESS_RULES"); f != "" {
		r, err := LoadEgressRules(f)
		if err != nil {
			return c, err
		}
		c.EgressRules = r
	}
	c.ReleaseCfg = ReleaseConfig{
		Registry:     os.Getenv("MAGI_IMAGE_REGISTRY"),
		PullRegistry: env("MAGI_IMAGE_PULL_REGISTRY", "localhost:30500"),
		BaseImage:    env("MAGI_IMAGE_BASE", "gcr.io/distroless/static-debian12:nonroot"),
		Target:       env("MAGI_RELEASE_TARGET", "magi"),
	}
	if f := os.Getenv("MAGI_IMAGE_CREDENTIALS_FILE"); f != "" {
		var cred struct{ Username, Password string }
		b, err := os.ReadFile(f)
		if err != nil {
			return c, err
		}
		if err := json.Unmarshal(b, &cred); err != nil {
			return c, err
		}
		c.ReleaseCfg.Username, c.ReleaseCfg.Password = cred.Username, cred.Password
	}
	if f := os.Getenv("MAGI_SERVICE_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return c, err
		}
		c.ServiceToken = strings.TrimSpace(string(b))
	}
	return c, nil
}

// System holds the clients and settings that all parts of compose share.
type System struct {
	Cfg   Config
	Log   *slog.Logger
	Model model.Chatter // the persona model; often a chain of local then remote
	// LibrarianModel classifies utterances (see Librarian); nil turns the
	// librarian off. A local model is best: the work is in the background.
	LibrarianModel model.Chatter
	// ReviewModel decides egress flows that no rule decides (EgressRules
	// default "model"); nil sends them to review.
	ReviewModel model.Chatter
	// Detector scores text for prompt injections (detect.go); nil
	// passes all text.
	Detector    *Detector
	EgressRules *EgressRules
	EgressProxy *EgressProxy
	// egressReviews caches decided egress reviews (egressreview.go).
	egressReviews *egressReviews
	Artifacts     *artifactsapi.ArtifactClient
	Conversation  *conversationapi.ConversationClient
	Execution     *executionapi.ExecutionClient
	Knowledge     *knowledgeapi.KnowledgeClient
	Source        *sourceapi.SourceClient
	Speech        *speechapi.SpeechClient
	Workflow      *workflowapi.WorkflowClient
	Release       *releaseapi.ReleaseClient
	ReleaseCfg    ReleaseConfig
	fleet         fleet
}

// New creates clients. Each call uses the token in its context (see
// AsService and AsCaller), so one client serves users and background work.
func New(cfg Config, m model.Chatter, log *slog.Logger) *System {
	c := func(base string) *rpc.Client { return rpc.NewClient(base, nil) }
	sys := &System{
		Cfg: cfg, Log: log, Model: m, ReleaseCfg: cfg.ReleaseCfg,
		Artifacts:    artifactsapi.NewArtifactClient(c(cfg.Artifacts)),
		Conversation: conversationapi.NewConversationClient(c(cfg.Conversation)),
		Execution:    executionapi.NewExecutionClient(c(cfg.Execution)),
		Knowledge:    knowledgeapi.NewKnowledgeClient(c(cfg.Knowledge)),
		Source:       sourceapi.NewSourceClient(c(cfg.Source)),
		Speech:       speechapi.NewSpeechClient(c(cfg.Speech)),
		Workflow:     workflowapi.NewWorkflowClient(c(cfg.Workflow)),
	}
	if cfg.Release != "" {
		sys.Release = releaseapi.NewReleaseClient(c(cfg.Release))
	}
	sys.EgressRules, sys.EgressProxy = cfg.EgressRules, cfg.EgressProxy
	sys.egressReviews = &egressReviews{reported: map[string]time.Time{}}
	return sys
}

// AsService makes calls with Magi's own service credential.
func (s *System) AsService(ctx context.Context) context.Context {
	return rpc.WithToken(ctx, s.Cfg.ServiceToken)
}

// AsCaller makes calls with the token of the person who called Magi, so
// each primitive authorizes that person directly.
func AsCaller(ctx context.Context) context.Context { return rpc.Forward(ctx) }

func ptr[T any](v T) *T { return &v }

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}
