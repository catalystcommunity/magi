package compose

import (
	"context"
	"fmt"
	"strings"
	"testing"

	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// lessonEntity is one lesson entity, as ImportBaseKnowledge stores it.
func lessonEntity(id, title, area, text string) knowledgeapi.Entity {
	return knowledgeapi.Entity{Id: "ent_" + id, Kind: "lesson", Label: title, Summary: text,
		Properties: map[string]string{"lesson_id": id, "area": area, "source": "run_x (review 2026-11-14)"},
		External:   &knowledgeapi.ExternalRef{System: lessonSystem, Locator: "baseknowledge/review-lessons.md#" + id},
		Owners:     []string{lessonOwner}, Readers: []string{}}
}

// lessonsSystem starts a System with the lesson entities in the fake knowledge
// service, on the real HTTP transport. The repository of Magi is "magi".
func lessonsSystem(t *testing.T, ents ...knowledgeapi.Entity) *System {
	t.Helper()
	var buf strings.Builder
	s := lessonRepoSystem(t, &fakeKnowledge{ents: ents}, &buf)
	s.Cfg.Repo = "magi"
	return s
}

// An area path in the instructions scores higher than a lesson for all code,
// so the area lesson comes first.
func TestLessonsForAreaScoresHigher(t *testing.T) {
	s := lessonsSystem(t,
		lessonEntity("rl-0001", "Keep the state of one run in the run method", "primitives/apprentice", "Set the flags at the start of the run method."),
		lessonEntity("rl-0002", "No helper for one expression", "all code", "Write the expression where the code uses it."))
	got := s.lessonsFor(context.Background(), "magi", "Fix the worker",
		"Change primitives/apprentice so that the worker keeps its state in one place.")
	want := "- rl-0001 Keep the state of one run in the run method: Set the flags at the start of the run method.\n" +
		"- rl-0002 No helper for one expression: Write the expression where the code uses it."
	if got != want {
		t.Fatalf("lessons =\n%s\nwant\n%s", got, want)
	}
}

// An area path applies only to the repository of Magi. For another repository
// the area lesson is left out; the lesson for all code stays.
func TestLessonsForOtherRepo(t *testing.T) {
	s := lessonsSystem(t,
		lessonEntity("rl-0001", "Keep the state of one run in the run method", "primitives/apprentice", "Set the flags at the start of the run method."),
		lessonEntity("rl-0002", "No helper for one expression", "all code", "Write the expression where the code uses it."))
	got := s.lessonsFor(context.Background(), "other", "Fix the worker",
		"Change the worker so that it starts cleanly.")
	if got != "- rl-0002 No helper for one expression: Write the expression where the code uses it." {
		t.Fatalf("lessons = %q", got)
	}
}

// A word of the lesson title that the task also names scores; a lesson that
// relates to nothing is left out.
func TestLessonsForWordsAndNoMatch(t *testing.T) {
	s := lessonsSystem(t,
		lessonEntity("rl-0001", "A diff for review shows new files", "primitives/speech", "Run git add --intent-to-add --all first."),
		lessonEntity("rl-0002", "No renames that the task did not ask for", "primitives/speech", "Use the existing name."),
		lessonEntity("rl-0003", "No helper for one expression", "primitives/speech", "Write the expression where the code uses it."))
	got := s.lessonsFor(context.Background(), "magi", "Show the new files in the diff", "The review must see a new file.")
	if got != "- rl-0001 A diff for review shows new files: Run git add --intent-to-add --all first." {
		t.Fatalf("lessons = %q", got)
	}
	if s.lessonsFor(context.Background(), "magi", "Nothing", "An unrelated task.") != "" {
		t.Fatal("a lesson that relates to nothing is in the list")
	}
}

// The word score stops at 3: a lesson with more shared words does not score
// higher, so the order falls back to the id.
func TestLessonsForWordCap(t *testing.T) {
	s := lessonsSystem(t,
		lessonEntity("rl-0001", "State flags helper", "all code", "Three shared words."),
		lessonEntity("rl-0009", "State flags helper method sandbox", "all code", "Five shared words."))
	got := s.lessonsFor(context.Background(), "magi", "A title",
		"Change the state flags helper method sandbox.")
	want := "- rl-0001 State flags helper: Three shared words.\n" +
		"- rl-0009 State flags helper method sandbox: Five shared words."
	if got != want {
		t.Fatalf("lessons =\n%s\nwant\n%s", got, want)
	}
}

// At most lessonsMax lessons, highest score first, then by id.
func TestLessonsForOrderAndLimit(t *testing.T) {
	var ents []knowledgeapi.Entity
	for i := 1; i <= lessonsMax+2; i++ {
		ents = append(ents, lessonEntity(fmt.Sprintf("rl-%04d", i), "Lesson about the sandbox of the runner", "all code", "Text."))
	}
	// This lesson has the area of the task, so it scores higher than the rest.
	ents = append(ents, lessonEntity("rl-0099", "Lesson about the sandbox of the runner", "magi/runner", "Text."))
	s := lessonsSystem(t, ents...)
	lines := strings.Split(s.lessonsFor(context.Background(), "magi", "A task", "Work in magi/runner."), "\n")
	if len(lines) != lessonsMax {
		t.Fatalf("lines = %d, want %d:\n%s", len(lines), lessonsMax, strings.Join(lines, "\n"))
	}
	if lines[0] != "- rl-0099 Lesson about the sandbox of the runner: Text." {
		t.Fatalf("first line = %q", lines[0])
	}
	// The same score: the order is by id.
	for i, id := range []string{"rl-0001", "rl-0002", "rl-0003", "rl-0004", "rl-0005", "rl-0006", "rl-0007"} {
		if !strings.HasPrefix(lines[i+1], "- "+id+" ") {
			t.Fatalf("line %d = %q, want %s", i+1, lines[i+1], id)
		}
	}
}

// An error of the knowledge service gives "" and a log line.
func TestLessonsForError(t *testing.T) {
	var buf strings.Builder
	s := lessonRepoSystem(t, &fakeKnowledge{fail: true}, &buf)
	s.Cfg.Repo = "magi"
	if got := s.lessonsFor(context.Background(), "magi", "t", "instructions"); got != "" {
		t.Fatalf("lessons = %q, want empty", got)
	}
	if !strings.Contains(buf.String(), "base knowledge: search") {
		t.Fatalf("no log line: %q", buf.String())
	}
}

// A topic lesson goes to a task that shares two or more subject words with
// its title, and not to a task that shares one word or only generic words.
func TestLessonsForTopic(t *testing.T) {
	s := lessonsSystem(t,
		lessonEntity("lp-0001", "Backfill old rows before you require a new field in a migration", lessonTopic, "Text."),
		lessonEntity("lp-0002", "Select cryptographic keys by their declared purpose", lessonTopic, "Text."))
	got := s.lessonsFor(context.Background(), "magi", "Add a required column",
		"Write the migration that adds the column and backfills the rows that exist.")
	if got != "- lp-0001 Backfill old rows before you require a new field in a migration: Text." {
		t.Fatalf("lessons = %q", got)
	}
	// One shared subject word ("keys"), and generic words ("check", "their").
	if got := s.lessonsFor(context.Background(), "magi", "Sort the output",
		"Sort the keys of the map. Check their order in the tests."); got != "" {
		t.Fatalf("lessons = %q", got)
	}
}
