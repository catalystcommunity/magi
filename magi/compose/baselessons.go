package compose

import (
	"context"
	"slices"
	"strings"
)

// The lessons of earlier code reviews are base knowledge: they apply to the
// next change of the same area. A code change gets the lessons that relate to
// it, so that the apprentice does not repeat an error that a review already
// found.

// lessonsMax is the number of lessons that one code change gets. The first
// message must stay small: the lessons are one part of it.
const lessonsMax = 12

// lessonsFor gives the lessons that relate to one code change: one line for
// each lesson, highest score first. A lesson of area "all code" applies to
// every repository; a lesson with an area path applies only to the repository
// of Magi. repoLessons logs an error of the knowledge service and gives no
// entity, so this returns "": a task does not stop for the lessons.
func (s *System) lessonsFor(ctx context.Context, repo, title, instructions string) string {
	entities := s.approvedLessons(ctx)
	if len(entities) == 0 {
		return ""
	}
	// The title and the instructions are the text that the lesson must relate
	// to. The case does not matter.
	text := strings.ToLower(title + "\n" + instructions)
	type scored struct {
		id, line string
		score    int
	}
	var out []scored
	for _, e := range entities {
		score := 0
		for _, area := range list(e.Properties["area"]) {
			switch {
			case area == "all code":
				score += 3
			case repo == s.Cfg.Repo && strings.Contains(text, strings.ToLower(area)):
				score += 5
			}
		}
		score += min(3, lessonWords(e.Label, text))
		if score == 0 {
			continue
		}
		out = append(out, scored{id: e.Properties["lesson_id"], score: score,
			line: "- " + e.Properties["lesson_id"] + " " + e.Label + ": " + e.Summary})
	}
	slices.SortFunc(out, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(a.id, b.id)
	})
	var lines []string
	for _, l := range out[:min(lessonsMax, len(out))] {
		lines = append(lines, l.line)
	}
	return strings.Join(lines, "\n")
}

// lessonWords counts the words of the lesson title that the title or the
// instructions of the task also have. Only a word of 5 or more letters counts,
// because a short word matches almost any text.
func lessonWords(title, text string) int {
	n := 0
	for _, w := range strings.Fields(strings.ToLower(title)) {
		w = strings.Trim(w, "!?.,;:()[]\"'")
		if len([]rune(w)) < 5 || !strings.Contains(text, w) {
			continue
		}
		n++
		if n == 3 {
			break
		}
	}
	return n
}
