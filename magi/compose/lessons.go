package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// A proposed lesson is a lesson that the repository does not have. It lives
// only in the knowledge graph, so the graph keeps the corpus: the proposals,
// the approved lessons, and the rejected ones. A code change gets a proposal
// only after an administrator approved it. A person decides, not a service:
// a lesson changes what each later code change does.
const (
	proposalSystem = "magi-lesson-proposal"

	lessonProposed = "proposed"
	lessonApproved = "approved"
	lessonRejected = "rejected"
)

// proposalID is the id of a proposal: "lp-" and 8 hex digits of the title.
// The same title gives the same id, so a second proposal of one lesson finds
// the first one.
func proposalID(title string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(title), " "))))
	return "lp-" + hex.EncodeToString(sum[:4])
}

// lessonEntities reads the lesson entities that the magi service owns: the
// copies of the repository lessons and the proposals. It logs an error of
// the knowledge service and gives no entity. The search has no filter for
// an external system, so it reads the entities of the kind and keeps the
// ones with the reference and the owner. One page of 500 is more than the
// lessons of today; a bigger corpus needs a filter in the contract.
func (s *System) lessonEntities(ctx context.Context) []knowledgeapi.Entity {
	res, err := s.Knowledge.Search(ctx, knowledgeapi.SearchRequest{Types: []string{"entity"}, Kind: optional(lessonKind), Limit: ptr(uint64(500))})
	if err != nil {
		s.Log.Warn("base knowledge: search", "err", err)
		return nil
	}
	var out []knowledgeapi.Entity
	for _, e := range res.Entities {
		if e.External != nil && slices.Contains(e.Owners, lessonOwner) &&
			(e.External.System == lessonSystem || e.External.System == proposalSystem) {
			out = append(out, e)
		}
	}
	return out
}

// lessonStatus is the status of a lesson entity. A repository lesson is
// approved: a person put it in the file.
func lessonStatus(e knowledgeapi.Entity) string {
	if e.External.System == lessonSystem {
		return lessonApproved
	}
	return e.Properties["status"]
}

// approvedLessons are the lessons that a code change can get.
func (s *System) approvedLessons(ctx context.Context) []knowledgeapi.Entity {
	var out []knowledgeapi.Entity
	for _, e := range s.lessonEntities(ctx) {
		if lessonStatus(e) == lessonApproved {
			out = append(out, e)
		}
	}
	return out
}

func apiLesson(e knowledgeapi.Entity) magiapi.Lesson {
	origin := "proposal"
	if e.External.System == lessonSystem {
		origin = "repository"
	}
	return magiapi.Lesson{Id: e.Properties["lesson_id"], Title: e.Label, Area: e.Properties["area"], Source: e.Properties["source"],
		Text: e.Summary, Origin: origin, Status: lessonStatus(e), DecidedBy: optional(e.Properties["decided_by"]), CreatedMs: e.CreatedMs}
}

// lessonFields checks the text fields of a lesson. The limits keep one
// lesson a few lines in the first message of a code change.
func lessonFields(title, area, text string) error {
	switch {
	case strings.TrimSpace(title) == "" || len(title) > 120 || strings.Contains(title, "\n"):
		return rpc.Invalid("a lesson title is one line of 1 to 120 characters")
	case strings.TrimSpace(area) == "" || strings.Contains(area, "\n"):
		return rpc.Invalid("a lesson area is \"all code\" or a path")
	case strings.TrimSpace(text) == "" || len(text) > 600:
		return rpc.Invalid("a lesson text has 1 to 600 characters")
	}
	return nil
}

func (m *Service) ProposeLesson(ctx context.Context, req magiapi.ProposeLessonRequest) (magiapi.Lesson, error) {
	if err := m.S.isOperator(ctx); err != nil {
		return magiapi.Lesson{}, err
	}
	if err := lessonFields(req.Title, req.Area, req.Text); err != nil {
		return magiapi.Lesson{}, err
	}
	if strings.TrimSpace(req.Source) == "" {
		return magiapi.Lesson{}, rpc.Invalid("a lesson needs a source")
	}
	id := proposalID(req.Title)
	text := strings.Join(strings.Fields(req.Text), " ")
	e, err := m.S.Knowledge.PutEntity(m.S.AsService(ctx), knowledgeapi.PutEntityRequest{Kind: lessonKind, Label: strings.TrimSpace(req.Title),
		Summary:    optional(text),
		Properties: map[string]string{"lesson_id": id, "area": strings.TrimSpace(req.Area), "source": strings.TrimSpace(req.Source), "status": lessonProposed},
		External:   &knowledgeapi.ExternalRef{System: proposalSystem, Locator: "proposal/" + id}, Owners: []string{lessonOwner}})
	if err != nil {
		return magiapi.Lesson{}, err
	}
	if e.External == nil || e.External.System != proposalSystem {
		// put-entity gives an entity with the same kind and label: a lesson of
		// the repository has this title.
		return magiapi.Lesson{}, rpc.Invalid("the repository has a lesson with this title: %s", e.Properties["lesson_id"])
	}
	return apiLesson(e), nil
}

func (m *Service) ListLessons(ctx context.Context, req magiapi.ListLessonsRequest) (magiapi.ListLessonsResponse, error) {
	if err := m.S.isOperator(ctx); err != nil {
		return magiapi.ListLessonsResponse{}, err
	}
	out := magiapi.ListLessonsResponse{Lessons: []magiapi.Lesson{}}
	for _, e := range m.S.lessonEntities(m.S.AsService(ctx)) {
		if st := deref(req.Status); st == "" || st == lessonStatus(e) {
			out.Lessons = append(out.Lessons, apiLesson(e))
		}
	}
	slices.SortFunc(out.Lessons, func(a, b magiapi.Lesson) int { return strings.Compare(a.Id, b.Id) })
	return out, nil
}

func (m *Service) DecideLesson(ctx context.Context, req magiapi.DecideLessonRequest) (magiapi.Lesson, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return magiapi.Lesson{}, err
	}
	// The operate scope is not sufficient: a service has it.
	if !slices.Contains(m.S.Cfg.Admins, p.Subject) {
		return magiapi.Lesson{}, rpc.Denied("only an administrator decides a lesson")
	}
	status := map[string]string{"approve": lessonApproved, "reject": lessonRejected}[req.Decision]
	if status == "" {
		return magiapi.Lesson{}, rpc.Invalid("a decision is approve or reject")
	}
	sctx := m.S.AsService(ctx)
	for _, e := range m.S.lessonEntities(sctx) {
		if e.Properties["lesson_id"] != req.Id {
			continue
		}
		if e.External.System == lessonSystem {
			return magiapi.Lesson{}, rpc.Invalid("%s is a lesson of the repository: change the lessons file", req.Id)
		}
		title, area, text := e.Label, e.Properties["area"], e.Summary
		set := map[string]string{"property.status": status, "property.decided_by": p.Subject}
		if v := strings.TrimSpace(deref(req.Title)); v != "" {
			title, set["label"] = v, v
		}
		if v := strings.TrimSpace(deref(req.Area)); v != "" {
			area, set["property.area"] = v, v
		}
		if v := strings.Join(strings.Fields(deref(req.Text)), " "); v != "" {
			text, set["summary"] = v, v
		}
		if err := lessonFields(title, area, text); err != nil {
			return magiapi.Lesson{}, err
		}
		res, err := m.S.Knowledge.Change(sctx, knowledgeapi.ChangeRequest{Id: e.Id, Operation: "fix", Set: set,
			Reason: optional(req.Decision + " by " + p.Subject)})
		if err != nil {
			return magiapi.Lesson{}, err
		}
		if res.Entity == nil {
			return magiapi.Lesson{}, rpc.Unavailable("the knowledge service gave no entity for %s", req.Id)
		}
		return apiLesson(*res.Entity), nil
	}
	return magiapi.Lesson{}, rpc.NotFound("no lesson %s", req.Id)
}
