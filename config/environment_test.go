package config

import "testing"

func TestEnvironmentCompatibility(t *testing.T) {
	t.Setenv("OPAL_RENAME_TEST", "old")
	if got := Environment("MAGI_RENAME_TEST"); got != "old" {
		t.Fatal(got)
	}
	t.Setenv("MAGI_RENAME_TEST", "new")
	if got := Environment("MAGI_RENAME_TEST"); got != "new" {
		t.Fatal(got)
	}
	t.Setenv("MAGI_RENAME_TEST", "")
	if got := Environment("MAGI_RENAME_TEST"); got != "" {
		t.Fatal(got)
	}
}
