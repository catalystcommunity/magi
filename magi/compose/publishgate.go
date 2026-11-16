package compose

import (
	"fmt"
	"slices"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
)

// Publish steps are the steps that act on the upstream repository or on
// the release: push the branch and open the pull request (publish), read
// the pull request (pr-check), and merge it (pr-merge). They run only on a
// runner that an operator approved in Magi's configuration
// (Config.PublishRunners, MAGI_PUBLISH_RUNNERS). A runner cannot approve
// itself, and Magi cannot see its sandbox kind, so the approval is the
// operator's statement that the runner has a trust tier that is enough
// (docs/runner-trust.md). The same holds for the artifacts that feed the
// release: the build of magi-server and the runner packages.
var publishSteps = []string{"publish", "pr-check", "pr-merge"}

// releaseArtifactKinds are the artifact labels ("kind") that only an
// approved runner can upload, because Magi deploys them.
var releaseArtifactKinds = []string{"build", "runner-package-build"}

func isPublishStep(step string) bool { return slices.Contains(publishSteps, step) }

func (s *System) publishApproved(runner string) bool {
	return runner != "" && slices.Contains(s.Cfg.PublishRunners, runner)
}

func approvedList(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// refusePublish returns the error for a publish action of an unapproved
// runner, and logs it.
func (s *System) refusePublish(runner, what string) error {
	if s.Log != nil {
		s.Log.Warn("refused: runner is not approved to publish", "runner", runner, "action", what, "approved", s.Cfg.PublishRunners)
	}
	return rpc.Denied("runner %s is not approved to publish (%s); an operator must list it in MAGI_PUBLISH_RUNNERS (approved: %s)",
		runner, what, approvedList(s.Cfg.PublishRunners))
}

// publishRunnersFromEnv parses MAGI_PUBLISH_RUNNERS: runner names only. A
// wildcard is not allowed, so that approval is always a decision about a
// named runner.
func publishRunnersFromEnv(v string) ([]string, error) {
	names := list(v)
	for _, n := range names {
		if n == "*" || strings.ContainsAny(n, "*/:@ \t\\") {
			return nil, fmt.Errorf("MAGI_PUBLISH_RUNNERS: %q is not a runner name (list names, no wildcards)", n)
		}
	}
	return names, nil
}
