package runner

import "testing"

func TestPRURL(t *testing.T) {
	m := prURL.FindStringSubmatch("https://github.com/catalystcommunity/magi/pull/12")
	if m == nil || m[1] != "catalystcommunity" || m[2] != "magi" || m[3] != "12" {
		t.Fatalf("%v", m)
	}
	for _, bad := range []string{"https://github.com/a/b/pull/1; rm -rf /", "https://evil.example/a/b/pull/1", "https://github.com/a/b/pull/x"} {
		if prURL.MatchString(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
