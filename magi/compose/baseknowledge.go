package compose

import (
	"context"
	"embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// Base knowledge is what the repository itself knows: the lessons of code
// reviews. The repository is the source of truth; the knowledge graph of
// each installation holds a copy that Magi can search (ImportBaseKnowledge).

//go:embed baseknowledge
var baseKnowledge embed.FS

// baseKnowledgeFile is the one file of base knowledge.
const baseKnowledgeFile = "baseknowledge/review-lessons.md"

// lessonHeader is the first line of a section: "## rl-0001: <title>". The id
// is "rl-" and four digits; no other form is a lesson header.
var lessonHeader = regexp.MustCompile(`^## (rl-\d{4}): (.+)$`)

// Lesson is one lesson of a code review.
type Lesson struct {
	ID     string // "rl-0001"
	Title  string
	Area   string
	Source string
	Text   string
}

// Locator is the address of the lesson in the repository.
func (l Lesson) Locator() string { return baseKnowledgeFile + "#" + l.ID }

// parseLessons reads the sections of a review-lessons file. A section starts
// with a "## rl-nnnn: title" line and holds an Area line, a Source line, and
// the text of the lesson. A bad header or a duplicate id is an error: the
// file is in the repository, so a test of the embedded file finds it before
// a release.
func parseLessons(name, in string) ([]Lesson, error) {
	var lessons []Lesson
	seen := map[string]bool{}
	var cur *Lesson
	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.Area == "" || cur.Source == "" {
			return fmt.Errorf("%s %s: the section needs an Area line and a Source line", name, cur.ID)
		}
		if strings.TrimSpace(cur.Text) == "" {
			return fmt.Errorf("%s %s: the section has no text", name, cur.ID)
		}
		cur.Text = strings.TrimSpace(cur.Text)
		lessons = append(lessons, *cur)
		cur = nil
		return nil
	}
	for _, line := range strings.Split(in, "\n") {
		line = strings.TrimSpace(line)
		if m := lessonHeader.FindStringSubmatch(line); m != nil {
			if err := flush(); err != nil {
				return nil, err
			}
			id, title := m[1], strings.TrimSpace(m[2])
			if seen[id] {
				return nil, fmt.Errorf("%s: %s is in the file two times", name, id)
			}
			seen[id] = true
			cur = &Lesson{ID: id, Title: title}
			continue
		}
		// A heading line that is not a lesson header is a section that the
		// parser cannot read. Make it an error, not the text of a lesson.
		if strings.HasPrefix(line, "##") {
			return nil, fmt.Errorf("%s: %q is not a lesson header", name, line)
		}
		// The lines before the first section are the header of the file.
		if cur == nil {
			continue
		}
		if v, ok := strings.CutPrefix(line, "Area:"); ok {
			cur.Area = strings.TrimSpace(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "Source:"); ok {
			cur.Source = strings.TrimSpace(v)
			continue
		}
		if line != "" {
			cur.Text += line + " "
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(lessons) == 0 {
		return nil, fmt.Errorf("%s: the file has no lesson", name)
	}
	return lessons, nil
}

// ReviewLessons are the lessons of the embedded review-lessons file.
func ReviewLessons() ([]Lesson, error) {
	b, err := baseKnowledge.ReadFile(baseKnowledgeFile)
	if err != nil {
		return nil, err
	}
	return parseLessons(baseKnowledgeFile, string(b))
}

// lessonNumber is the digits of a lesson id, for the order of the file.
func lessonNumber(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "rl-"))
	return n
}

// lessonKind is the entity kind of a lesson, and the system of its external
// reference. The owner is the magi service: only the import changes lessons.
const (
	lessonKind   = "lesson"
	lessonSystem = "magi-repo"
	lessonOwner  = "service:magi"
)

// ImportBaseKnowledge copies the lessons of the repository into the
// knowledge graph: each lesson is an entity of kind "lesson" with an
// external reference to the file. A lesson that changed gets a fix; a lesson
// that the file no longer has gets a retraction. The repository is the
// source of truth, so the graph only holds a copy.
func (s *System) ImportBaseKnowledge(ctx context.Context) error {
	lessons, err := ReviewLessons()
	if err != nil {
		return err
	}
	ctx = s.AsService(ctx)
	// The graph is read before the writes. put-entity returns a new entity
	// with the fields of the file, so only this list tells a new lesson from
	// an unchanged one.
	existing := s.repoLessons(ctx)
	have := map[string]bool{}
	for _, e := range existing {
		have[e.Properties["lesson_id"]] = true
	}
	byID := map[string]bool{}
	added, fixed, unchanged := 0, 0, 0
	for _, l := range lessons {
		byID[l.ID] = true
		was := have[l.ID]
		e, err := s.Knowledge.PutEntity(ctx, knowledgeapi.PutEntityRequest{Kind: lessonKind, Label: l.Title, Summary: optional(l.Text),
			Properties: map[string]string{"lesson_id": l.ID, "area": l.Area, "source": l.Source},
			External:   &knowledgeapi.ExternalRef{System: lessonSystem, Locator: l.Locator()}, Owners: []string{lessonOwner}})
		if err != nil {
			return fmt.Errorf("put lesson %s: %w", l.ID, err)
		}
		set := map[string]string{}
		if e.Label != l.Title {
			set["label"] = l.Title
		}
		if e.Summary != l.Text {
			set["summary"] = l.Text
		}
		for k, v := range map[string]string{"lesson_id": l.ID, "area": l.Area, "source": l.Source} {
			if e.Properties[k] != v {
				set["property."+k] = v
			}
		}
		if len(set) > 0 {
			// put-entity returns an existing entity as it is; the file is the
			// authority, so the differences are errors of the copy.
			if _, err := s.Knowledge.Change(ctx, knowledgeapi.ChangeRequest{Id: e.Id, Operation: "fix", Set: set,
				Reason: optional("changed in review-lessons.md")}); err != nil {
				return fmt.Errorf("fix lesson %s: %w", l.ID, err)
			}
		}
		switch {
		case !was:
			added++
		case len(set) > 0:
			fixed++
		default:
			unchanged++
		}
	}
	retracted := 0
	for _, e := range existing {
		if byID[e.Properties["lesson_id"]] {
			continue
		}
		if _, err := s.Knowledge.Change(ctx, knowledgeapi.ChangeRequest{Id: e.Id, Operation: "retract",
			Reason: optional("removed from review-lessons.md")}); err != nil {
			return fmt.Errorf("retract lesson %s: %w", e.Id, err)
		}
		retracted++
	}
	s.Log.Info("base knowledge", "added", added, "fixed", fixed, "retracted", retracted, "unchanged", unchanged)
	return nil
}

// repoLessons finds the lesson entities that the repository owns: kind
// "lesson", external system "magi-repo", owner "service:magi". Search gives
// only current entities, so a retracted lesson is not in the list: the next
// start does not retract it again.
func (s *System) repoLessons(ctx context.Context) []knowledgeapi.Entity {
	var out []knowledgeapi.Entity
	for _, e := range s.lessonEntities(ctx) {
		if e.External.System == lessonSystem {
			out = append(out, e)
		}
	}
	return out
}
