package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

const lessonAdmin = "user:admin@example.local"

// lessonsEnv serves the MagiService and a fake knowledge service on the real
// HTTP transport. as gives the context of a caller with the scopes.
func lessonsEnv(t *testing.T, ents ...knowledgeapi.Entity) (*System, *magiapi.MagiClient, func(sub string, scopes ...string) context.Context) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	ver := &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tok, err := iss.Issue(authn.Principal{Subject: lessonOwner, Kind: authn.KindService, Scopes: []string{"knowledge:act-as"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ksrv := &rpc.Server{Verifier: ver, Log: log}
	ksrv.Register("KnowledgeService", knowledgeapi.KnowledgeServiceOps(&fakeKnowledge{ents: ents}))
	kh := httptest.NewServer(rpc.Mux(ksrv, nil))
	t.Cleanup(kh.Close)
	s := &System{Log: log, Cfg: Config{ServiceToken: tok, Repo: "magi", Admins: []string{lessonAdmin}},
		Knowledge: knowledgeapi.NewKnowledgeClient(rpc.NewClient(kh.URL, rpc.StaticToken(tok)))}
	msrv := &rpc.Server{Verifier: ver, Log: log}
	msrv.Register("MagiService", magiapi.MagiServiceOps(&Service{S: s}))
	mh := httptest.NewServer(rpc.Mux(msrv, nil))
	t.Cleanup(mh.Close)
	as := func(sub string, scopes ...string) context.Context {
		kind := authn.KindUser
		if strings.HasPrefix(sub, "service:") {
			kind = authn.KindService
		}
		tok, err := iss.Issue(authn.Principal{Subject: sub, Kind: kind, Scopes: scopes}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return rpc.WithToken(context.Background(), tok)
	}
	return s, magiapi.NewMagiClient(rpc.NewClient(mh.URL, nil)), as
}

var proposal = magiapi.ProposeLessonRequest{Title: "Keep the columns of command output", Area: "all code",
	Source: "run_x (review 2026-11-22)", Text: "Do not remove the space\nat the start of command output."}

// A proposal is not a lesson of a code change until an administrator
// approves it. The approval can change the text.
func TestProposedLessonNeedsApproval(t *testing.T) {
	s, mc, as := lessonsEnv(t, lessonEntity("rl-0002", "No helper for one expression", "all code", "Write the expression."))
	op := as("service:reviewer", ScopeOperate)
	l, err := mc.ProposeLesson(op, proposal)
	if err != nil || l.Status != "proposed" || l.Origin != "proposal" || !strings.HasPrefix(l.Id, "lp-") ||
		l.Text != "Do not remove the space at the start of command output." {
		t.Fatalf("propose: %+v %v", l, err)
	}
	// The same title gives the same proposal.
	again, err := mc.ProposeLesson(op, proposal)
	if err != nil || again.Id != l.Id {
		t.Fatalf("second proposal: %+v %v", again, err)
	}
	if got := s.lessonsFor(context.Background(), "magi", "A task", "Print the output of a command."); strings.Contains(got, l.Id) {
		t.Fatalf("a proposed lesson is in the lessons of a code change:\n%s", got)
	}
	res, err := mc.ListLessons(op, magiapi.ListLessonsRequest{Status: optional("proposed")})
	if err != nil || len(res.Lessons) != 1 || res.Lessons[0].Id != l.Id {
		t.Fatalf("list proposed: %+v %v", res, err)
	}
	all, err := mc.ListLessons(op, magiapi.ListLessonsRequest{})
	if err != nil || len(all.Lessons) != 2 || all.Lessons[1].Origin != "repository" || all.Lessons[1].Status != "approved" {
		t.Fatalf("list all: %+v %v", all, err)
	}
	d, err := mc.DecideLesson(as(lessonAdmin), magiapi.DecideLessonRequest{Id: l.Id, Decision: "approve", Text: optional("Keep the first column.")})
	if err != nil || d.Status != "approved" || deref(d.DecidedBy) != lessonAdmin || d.Text != "Keep the first column." {
		t.Fatalf("approve: %+v %v", d, err)
	}
	got := s.lessonsFor(context.Background(), "magi", "A task", "Print the output of a command.")
	if !strings.Contains(got, "- "+l.Id+" Keep the columns of command output: Keep the first column.") {
		t.Fatalf("the approved lesson is not in the lessons of a code change:\n%s", got)
	}
}

// A rejected proposal stays in the graph and no code change gets it.
func TestRejectedLessonStaysOut(t *testing.T) {
	s, mc, as := lessonsEnv(t)
	l, err := mc.ProposeLesson(as("service:reviewer", ScopeOperate), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := mc.DecideLesson(as(lessonAdmin), magiapi.DecideLessonRequest{Id: l.Id, Decision: "reject"}); err != nil || d.Status != "rejected" {
		t.Fatalf("reject: %+v %v", d, err)
	}
	if got := s.lessonsFor(context.Background(), "magi", "A task", "Print the output of a command."); got != "" {
		t.Fatalf("lessons = %q", got)
	}
	res, err := mc.ListLessons(as("service:reviewer", ScopeOperate), magiapi.ListLessonsRequest{Status: optional("rejected")})
	if err != nil || len(res.Lessons) != 1 {
		t.Fatalf("list rejected: %+v %v", res, err)
	}
}

// Only an administrator decides: the operate scope of a service is not
// sufficient. A repository lesson has no decision. A person without the
// operate scope cannot propose or list.
func TestLessonDecisionIsForAnAdministrator(t *testing.T) {
	_, mc, as := lessonsEnv(t, lessonEntity("rl-0002", "No helper for one expression", "all code", "Write the expression."))
	op := as("service:reviewer", ScopeOperate)
	l, err := mc.ProposeLesson(op, proposal)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		ctx  context.Context
		req  magiapi.DecideLessonRequest
		code string
	}{
		"service with the operate scope": {op, magiapi.DecideLessonRequest{Id: l.Id, Decision: "approve"}, "rpc 403"},
		"repository lesson":              {as(lessonAdmin), magiapi.DecideLessonRequest{Id: "rl-0002", Decision: "reject"}, "rpc 400"},
		"unknown decision":               {as(lessonAdmin), magiapi.DecideLessonRequest{Id: l.Id, Decision: "maybe"}, "rpc 400"},
		"unknown lesson":                 {as(lessonAdmin), magiapi.DecideLessonRequest{Id: "lp-00000000", Decision: "approve"}, "rpc 404"},
		"text too long":                  {as(lessonAdmin), magiapi.DecideLessonRequest{Id: l.Id, Decision: "approve", Text: optional(strings.Repeat("word ", 200))}, "rpc 400"},
	} {
		if _, err := mc.DecideLesson(c.ctx, c.req); err == nil || !strings.Contains(err.Error(), c.code) {
			t.Errorf("%s: err = %v, want %s", name, err, c.code)
		}
	}
	plain := as("user:a@example.local")
	if _, err := mc.ProposeLesson(plain, proposal); err == nil {
		t.Error("a person without the operate scope proposed a lesson")
	}
	if _, err := mc.ListLessons(plain, magiapi.ListLessonsRequest{}); err == nil {
		t.Error("a person without the operate scope listed the lessons")
	}
	if _, err := mc.ProposeLesson(op, magiapi.ProposeLessonRequest{Title: "A title of a lesson", Area: "all code", Text: "Text."}); err == nil {
		t.Error("a proposal without a source passed")
	}
}
