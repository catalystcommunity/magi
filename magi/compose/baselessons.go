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
	stems := topicStems(text)
	for _, e := range entities {
		score, topic, subject := 0, false, strings.ToLower(e.Label)
		for _, area := range list(e.Properties["area"]) {
			switch {
			case area == "all code":
				score += 3
			case area == lessonTopic:
				topic = true
			case strings.HasPrefix(area, lessonTopic+":"):
				// The words after the colon are more subject words: the words of
				// a task about the subject that the title does not have.
				topic, subject = true, subject+" "+strings.ToLower(area[len(lessonTopic)+1:])
			case repo == s.Cfg.Repo && strings.Contains(text, strings.ToLower(area)):
				score += 5
			}
		}
		if topic {
			// A topic lesson is for a task about its subject only: one shared
			// word is chance.
			n := 0
			for stem := range topicStems(subject) {
				if stems[stem] {
					n++
				}
			}
			if n < 2 {
				continue
			}
			score += 2 + min(3, n)
		} else {
			score += min(3, lessonWords(e.Label, text))
		}
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

// lessonTopic is the area of a lesson about one subject (a migration, a
// permission check, a queue). The corpus has many of these, and a code
// change gets one only when its task is about that subject.
const lessonTopic = "topic"

// genericStems are stems of words that most tasks and most lessons have, so
// they do not show a shared subject.
var genericStems = map[string]bool{"check": true, "tests": true, "every": true, "other": true, "first": true, "value": true,
	"error": true, "befor": true, "after": true, "chang": true, "shoul": true, "which": true, "their": true, "there": true,
	"where": true, "while": true, "using": true, "whole": true, "exact": true, "files": true, "needs": true, "match": true,
	"witho": true, "requi": true, "reque": true, "curre": true, "exist": true, "separ": true, "diffe": true}

// topicStems are the stems of the words of 5 or more letters of a lower-case
// text: the first 5 letters, so "migrate" and "migration" are the same.
func topicStems(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return r < 'a' || r > 'z' }) {
		if len(w) >= 5 && !genericStems[w[:5]] {
			out[w[:5]] = true
		}
	}
	return out
}
