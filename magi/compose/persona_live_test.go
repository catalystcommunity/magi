package compose

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/magi/model"
)

// TestLivePersonaTools checks that a model follows the persona rules for
// tools: recall before an answer about the person's things, remember new
// facts, start a code change, list tasks, and no tool for small talk. It
// checks the first model call only. Set MAGI_TEST_PERSONA_URL and
// MAGI_TEST_PERSONA_MODEL, and MAGI_TEST_PERSONA_KEY_FILE for a key (a
// provider key or an inference:use token). MAGI_TEST_PERSONA_RUNS (default
// 3) repeats each case; at least two thirds of the runs must pass.
func TestLivePersonaTools(t *testing.T) {
	base, name := os.Getenv("MAGI_TEST_PERSONA_URL"), os.Getenv("MAGI_TEST_PERSONA_MODEL")
	if base == "" || name == "" {
		t.Skip("MAGI_TEST_PERSONA_URL and MAGI_TEST_PERSONA_MODEL are not set")
	}
	c := &model.Client{BaseURL: strings.TrimRight(base, "/"), Model: name, HTTP: &http.Client{Timeout: 3 * time.Minute}}
	if f := os.Getenv("MAGI_TEST_PERSONA_KEY_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		c.APIKey = strings.TrimSpace(string(b))
	}
	runs := 3
	if n, err := strconv.Atoi(os.Getenv("MAGI_TEST_PERSONA_RUNS")); err == nil && n > 0 {
		runs = n
	}
	cases := []struct{ say, tool string }{
		{"Who is my dentist?", "recall"},
		{"What is on my work calendar today?", "recall"},
		{"Remember that my dentist is Dr. Rivera at Maple Street Dental.", "remember"},
		{"My family calendar is in the household Nextcloud.", "remember"},
		{"Please add a health endpoint summary to the Magi README.", "start_code_change"},
		{"Please add a health endpoint summary to the Magi Readme.", "start_code_change"}, // as Parakeet transcribes it
		{"How are my tasks going?", "list_tasks"},
		{"Hello! How are you today?", ""},
	}
	system := fmt.Sprintf(personaPrompt, "Alex", time.Now().Format("Monday 2 January 2006 15:04 MST"))
	failed := 0
	for _, tc := range cases {
		pass := 0
		var got []string
		var ms int64
		for i := 0; i < runs; i++ {
			start := time.Now()
			m, err := c.Chat(context.Background(), []model.Message{{Role: "system", Content: system}, {Role: "user", Content: tc.say}}, personaTools(), 1024)
			ms += time.Since(start).Milliseconds()
			first := ""
			switch {
			case err != nil:
				first = "error: " + err.Error()
			case len(m.ToolCalls) > 0:
				first = m.ToolCalls[0].Function.Name
			}
			got = append(got, first)
			if first == tc.tool {
				pass++
			}
		}
		ok := pass*3 >= runs*2
		if !ok {
			failed++
		}
		t.Logf("%-5v %d/%d %5d ms  %-62q want %-17q got %q", ok, pass, runs, ms/int64(runs), tc.say, tc.tool, got)
	}
	if failed > 0 {
		t.Errorf("%s: %d of %d cases failed", name, failed, len(cases))
	}
}
