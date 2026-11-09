// Command notes-eval runs the note taker of Magi on the cases of
// evals/notetaker (gen.py) with one model, and scores the answers: the raw
// answer of the model, and the items that Magi applies after its checks.
//
//	notes-eval -cases 'evals/notetaker/data/test-*.jsonl' -url URL -model NAME
//	           [-key-file F] [-workers N] [-out FILE] [-label L]
//
// The URL is an OpenAI-compatible endpoint (llama-server, a provider).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/magi/compose"
	"github.com/catalystcommunity/magi/magi/model"
)

// Item is an expected or a forbidden item of a case. Empty fields match all.
type Item struct {
	Op            string   `json:"op"`
	Id            string   `json:"id,omitempty"`
	Subject       string   `json:"subject,omitempty"`
	Relation      []string `json:"relation,omitempty"`
	Object        string   `json:"object,omitempty"`
	Value         string   `json:"value,omitempty"`
	ValueOrObject string   `json:"value_or_object,omitempty"`
}

type Case struct {
	Id         string `json:"id"`
	Category   string `json:"category"`
	When       string `json:"when"`
	Self       string `json:"self"`
	Notes      string `json:"notes"`
	Text       string `json:"text"`
	Expect     []Item `json:"expect"`
	Forbid     []Item `json:"forbid"`
	AllowExtra bool   `json:"allow_extra"`
}

type Result struct {
	Id       string             `json:"id"`
	Category string             `json:"category"`
	Ms       int64              `json:"ms"`
	Error    string             `json:"error,omitempty"`
	Raw      []compose.NoteItem `json:"raw"`
	Items    []compose.NoteItem `json:"items"`
	RawOK    bool               `json:"raw_ok"`
	OK       bool               `json:"ok"`
	Why      string             `json:"why,omitempty"`
}

func main() {
	glob := flag.String("cases", "evals/notetaker/data/test-*.jsonl", "case files (glob)")
	url := flag.String("url", "", "OpenAI-compatible base URL (…/v1)")
	name := flag.String("model", "local", "model name")
	keyFile := flag.String("key-file", "", "file with the API key (optional)")
	workers := flag.Int("workers", 2, "parallel requests")
	out := flag.String("out", "", "result file (JSON lines); default: none")
	label := flag.String("label", "", "label for the summary")
	limit := flag.Int("limit", 0, "at most this many cases (0: all)")
	flag.String("tz", "America/Denver", "household time zone (the cases are in it)")
	flag.Parse()
	tz := flag.Lookup("tz").Value.String()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		fail(err)
	}
	compose.SetHouseholdZone(loc)
	cases, err := load(*glob)
	if err != nil {
		fail(err)
	}
	if *limit > 0 && *limit < len(cases) {
		cases = cases[:*limit]
	}
	c := &model.Client{BaseURL: strings.TrimRight(*url, "/"), Model: *name, HTTP: &http.Client{Timeout: 10 * time.Minute}, Attempts: 2}
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			fail(err)
		}
		c.APIKey = strings.TrimSpace(string(b))
	}
	res := make([]Result, len(cases))
	var wg sync.WaitGroup
	next := make(chan int)
	var mu sync.Mutex
	done := 0
	for w := 0; w < max(1, *workers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				res[i] = run(c, cases[i])
				mu.Lock()
				done++
				if done%25 == 0 {
					fmt.Fprintf(os.Stderr, "%d/%d\n", done, len(cases))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range cases {
		next <- i
	}
	close(next)
	wg.Wait()
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fail(err)
		}
		for _, r := range res {
			b, _ := json.Marshal(r)
			f.Write(append(b, '\n'))
		}
		f.Close()
	}
	summary(*label, *name, res)
}

func run(c *model.Client, k Case) Result {
	when, err := time.Parse(time.RFC3339, k.When)
	if err != nil {
		return Result{Id: k.Id, Category: k.Category, Error: err.Error()}
	}
	start := time.Now()
	raw, items, err := compose.NoteItems(context.Background(), c, k.Text, k.Notes, when, k.Self)
	r := Result{Id: k.Id, Category: k.Category, Ms: time.Since(start).Milliseconds(), Raw: raw, Items: items}
	if err != nil {
		r.Error = err.Error()
		r.Why = "error"
		return r
	}
	r.RawOK, _ = score(k, raw)
	r.OK, r.Why = score(k, items)
	return r
}

// score: every expected item is in the answer (each by another item), no
// forbidden item is, and with allow_extra false there is nothing else.
func score(k Case, got []compose.NoteItem) (bool, string) {
	used := make([]bool, len(got))
	for _, e := range k.Expect {
		hit := false
		for i, g := range got {
			if !used[i] && match(e, g, k.Self, false) {
				used[i], hit = true, true
				break
			}
		}
		if !hit {
			return false, "missing " + describe(e)
		}
	}
	for _, f := range k.Forbid {
		for _, g := range got {
			if match(f, g, k.Self, true) {
				return false, "forbidden " + describe(f)
			}
		}
	}
	if !k.AllowExtra {
		for i, g := range got {
			if !used[i] {
				return false, fmt.Sprintf("extra %s %s %s %s%s", g.Op, g.Subject, g.Relation, g.Object, g.Value)
			}
		}
	}
	return true, ""
}

// match: exact true (a forbidden item) wants the relation itself ("old car"
// is not "car").
func match(e Item, g compose.NoteItem, self string, exact bool) bool {
	if e.Op != "" && !strings.EqualFold(e.Op, g.Op) {
		return false
	}
	if e.Id != "" && e.Id != g.Id {
		return false
	}
	if e.Subject != "" && !same(e.Subject, g.Subject, self) {
		return false
	}
	if len(e.Relation) > 0 {
		r := strings.ToLower(strings.TrimSpace(g.Relation))
		ok := false
		for _, x := range e.Relation {
			if r == x || (!exact && strings.Contains(r, x)) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if e.Object != "" && !same(e.Object, g.Object, self) && !contains(g.Value, e.Object) {
		return false
	}
	if e.Value != "" && !contains(g.Value, e.Value) && !contains(g.Object, e.Value) {
		return false
	}
	if e.ValueOrObject != "" && !contains(g.Value, e.ValueOrObject) && !contains(g.Object, e.ValueOrObject) {
		return false
	}
	return true
}

// same: names match ("me" is the person), or one has the other ("Dr. Shaw",
// "Shaw"; "Nadia Kerr", "Nadia").
func same(want, got, self string) bool {
	w, g := strings.ToLower(strings.TrimSpace(want)), strings.ToLower(strings.TrimSpace(got))
	if w == "me" {
		return g == "me" || g == "i" || g == "my" || g == "myself" || g == strings.ToLower(self)
	}
	return g != "" && (strings.Contains(g, w) || strings.Contains(w, g))
}

func contains(have, want string) bool {
	return want != "" && strings.Contains(strings.ToLower(have), strings.ToLower(want))
}

func describe(e Item) string {
	b, _ := json.Marshal(e)
	return string(b)
}

func summary(label, name string, res []Result) {
	type agg struct{ n, ok, raw, err int }
	by := map[string]*agg{}
	var all agg
	var ms []int64
	for _, r := range res {
		a := by[r.Category]
		if a == nil {
			a = &agg{}
			by[r.Category] = a
		}
		for _, x := range []*agg{a, &all} {
			x.n++
			if r.OK {
				x.ok++
			}
			if r.RawOK {
				x.raw++
			}
			if r.Error != "" {
				x.err++
			}
		}
		if r.Error == "" {
			ms = append(ms, r.Ms)
		}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })
	med := int64(0)
	if len(ms) > 0 {
		med = ms[len(ms)/2]
	}
	fmt.Printf("%s (%s): %d/%d with checks (%.0f%%), raw %d/%d, errors %d, median %d ms\n", label, name, all.ok, all.n,
		100*float64(all.ok)/float64(max(1, all.n)), all.raw, all.n, all.err, med)
	cats := make([]string, 0, len(by))
	for k := range by {
		cats = append(cats, k)
	}
	sort.Strings(cats)
	for _, k := range cats {
		a := by[k]
		fmt.Printf("  %-14s %3d/%-3d raw %3d\n", k, a.ok, a.n, a.raw)
	}
}

func load(glob string) ([]Case, error) {
	files, err := filepath.Glob(glob)
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no case files for %q", glob)
	}
	sort.Strings(files)
	var out []Case
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var c Case
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			out = append(out, c)
		}
	}
	return out, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "notes-eval:", err)
	os.Exit(1)
}
