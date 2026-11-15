package compose

import (
	"slices"
	"strings"
	"testing"
)

const goodLessons = `# Review lessons

This file keeps the lessons of code reviews.

## rl-0001: Keep the state of one run in the run method

Area: primitives/apprentice
Source: run_DnUog7zOwB5NxHpymwkz (review 2026-11-14)

Set the flags of one run at the start of the run method.
An object can run more than once.

## rl-0002: No helper for one expression

Area: all code
Source: run_DnUog7zOwB5NxHpymwkz (review 2026-11-14)

Do not add a function that only wraps one expression.
`

func TestParseLessonsGood(t *testing.T) {
	ls, err := parseLessons("review-lessons.md", goodLessons)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 {
		t.Fatalf("lessons = %d", len(ls))
	}
	want := Lesson{ID: "rl-0001", Title: "Keep the state of one run in the run method", Area: "primitives/apprentice",
		Source: "run_DnUog7zOwB5NxHpymwkz (review 2026-11-14)",
		Text:   "Set the flags of one run at the start of the run method. An object can run more than once."}
	if ls[0] != want {
		t.Fatalf("lesson 0 = %+v, want %+v", ls[0], want)
	}
	if ls[1].ID != "rl-0002" || ls[1].Area != "all code" {
		t.Fatalf("lesson 1 = %+v", ls[1])
	}
	if ls[0].Locator() != "baseknowledge/review-lessons.md#rl-0001" {
		t.Fatalf("locator = %q", ls[0].Locator())
	}
}

func TestParseLessonsBadHeader(t *testing.T) {
	for _, in := range []string{
		"## rl-0001 no colon\n\nArea: all code\nSource: run_1 (review 2026-11-14)\n\nText.",
		"## lesson one: title\n\nArea: all code\nSource: run_1 (review 2026-11-14)\n\nText.",
		"## rl-1: title\n\nArea: all code\nSource: run_1 (review 2026-11-14)\n\nText.",
		"## rl-0001: title\n\nSource: run_1 (review 2026-11-14)\n\nText.",
		"## rl-0001: title\n\nArea: all code\n\nText.",
		"## rl-0001: title\n\nArea: all code\nSource: run_1 (review 2026-11-14)\n",
		"# Review lessons\n\nThis file keeps the lessons of code reviews.\n",
	} {
		if _, err := parseLessons("review-lessons.md", in); err == nil {
			t.Errorf("no error for %q", firstLine(in, 40))
		}
	}
}

func TestParseLessonsDuplicateID(t *testing.T) {
	in := strings.ReplaceAll(goodLessons, "rl-0002", "rl-0001")
	if _, err := parseLessons("review-lessons.md", in); err == nil || !strings.Contains(err.Error(), "two times") {
		t.Fatalf("err = %v", err)
	}
}

// The embedded file is in the repository, so a bad file must fail the tests
// (and thus tools/check.sh) before a release.
func TestReviewLessonsEmbedded(t *testing.T) {
	ls, err := ReviewLessons()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) < 3 {
		t.Fatalf("lessons = %d", len(ls))
	}
	for _, l := range ls {
		if !lessonID.MatchString(l.ID) || l.Title == "" || l.Area == "" || l.Source == "" || l.Text == "" {
			t.Fatalf("bad lesson %+v", l)
		}
	}
	ids := make([]string, len(ls))
	for i, l := range ls {
		ids[i] = l.ID
	}
	if len(ls) != len(slices.Compact(ids)) {
		t.Fatalf("duplicate ids: %v", ids)
	}
	for i, l := range ls[1:] {
		if lessonNumber(l.ID) <= lessonNumber(ls[i].ID) {
			t.Fatalf("ids not in order: %v", ids)
		}
	}
	// The three lessons of the first reviews.
	for _, id := range []string{"rl-0001", "rl-0002", "rl-0003"} {
		if !slices.Contains(ids, id) {
			t.Fatalf("no %s in %v", id, ids)
		}
	}
}
