package server

import (
	"testing"

	api "github.com/catalystcommunity/magi/primitives/models/api"
)

func ptr[T any](v T) *T { return &v }

func names(c []api.Candidate) []string {
	var out []string
	for _, x := range c {
		out = append(out, x.Model.Name)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSelect(t *testing.T) {
	ms := []api.Model{
		{Name: "gemma-4-12b", Enabled: true, Tags: []string{"local", "chat", "tools", "schema"}, Scores: map[string]float64{"notetaker/data5-test": 0.90}},
		{Name: "gemma-4-12b-notetaker", Enabled: true, Tags: []string{"local", "chat", "schema", "notetaker"}, Scores: map[string]float64{"notetaker/data5-test": 0.93}},
		{Name: "glm-5.3", Enabled: true, Tags: []string{"hosted", "chat", "tools", "schema"}, Cost: api.Cost{InputPerMtok: 600_000, OutputPerMtok: 2_200_000}},
		{Name: "qwen3.5-4b-nas", Enabled: true, Tags: []string{"local", "small", "decide"}, Attributes: map[string]string{"gpu": "rx7600"}},
		{Name: "old", Enabled: false, Tags: []string{"local", "chat"}},
	}
	cases := []struct {
		name   string
		sel    api.Selector
		want   []string
		strict bool
	}{
		{"tags and rank", api.Selector{All: []string{"schema"}, RankBy: ptr("notetaker/data5-test")}, []string{"gemma-4-12b-notetaker", "gemma-4-12b", "glm-5.3"}, false},
		{"name first, then the class", api.Selector{Name: ptr("glm-5.3"), All: []string{"local", "chat"}}, []string{"glm-5.3", "gemma-4-12b", "gemma-4-12b-notetaker"}, false},
		{"only name", api.Selector{Name: ptr("glm-5.3"), OnlyName: ptr(true)}, []string{"glm-5.3"}, false},
		{"disabled name", api.Selector{Name: ptr("old"), OnlyName: ptr(true)}, nil, false},
		{"none hosted", api.Selector{All: []string{"chat"}, None: []string{"hosted"}}, []string{"gemma-4-12b", "gemma-4-12b-notetaker"}, false},
		{"any", api.Selector{Any: []string{"decide", "notetaker"}}, []string{"gemma-4-12b-notetaker", "qwen3.5-4b-nas"}, false},
		{"attribute", api.Selector{Attributes: map[string]string{"gpu": "rx7600"}}, []string{"qwen3.5-4b-nas"}, false},
		{"cost limit", api.Selector{All: []string{"chat"}, MaxInputCost: ptr(uint64(100_000))}, []string{"gemma-4-12b", "gemma-4-12b-notetaker"}, false},
		{"cheaper first without rank", api.Selector{All: []string{"tools"}}, []string{"gemma-4-12b", "glm-5.3"}, false},
		{"strict name over the cost limit", api.Selector{Name: ptr("glm-5.3"), MaxInputCost: ptr(uint64(0)), OnlyName: ptr(true)}, nil, true},
		{"strict name in the limits", api.Selector{Name: ptr("gemma-4-12b"), MaxInputCost: ptr(uint64(0)), OnlyName: ptr(true)}, []string{"gemma-4-12b"}, true},
	}
	for _, c := range cases {
		if got := names(Select(ms, c.sel, c.strict)); !eq(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMerge(t *testing.T) {
	def := api.Selector{All: []string{"chat"}, None: []string{"hosted"}, RankBy: ptr("x")}
	got := Merge(def, api.Selector{Name: ptr("glm-5.3"), None: []string{}})
	if *got.Name != "glm-5.3" || len(got.None) != 0 || got.All[0] != "chat" || *got.RankBy != "x" {
		t.Fatalf("merge = %+v", got)
	}
}

func TestCostAge(t *testing.T) {
	day := uint64(24 * 3600 * 1000)
	now := 200 * day
	for _, c := range []struct {
		cost api.Cost
		want string
	}{
		{api.Cost{}, ""},
		{api.Cost{InputPerMtok: 1}, "; cost date unknown"},
		{api.Cost{InputPerMtok: 1, AsOfMs: ptr(now - 10*day)}, ""},
		{api.Cost{InputPerMtok: 1, AsOfMs: ptr(now - 100*day)}, "; cost stale (1970-04-11)"},
	} {
		if got := costAge(c.cost, now); got != c.want {
			t.Errorf("%+v: %q, want %q", c.cost, got, c.want)
		}
	}
}
