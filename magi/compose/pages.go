package compose

// Pages: a persona makes a page for something that is too long or too
// visual for a spoken reply (a plan, a comparison, a diagram). The page is
// an artifact of the person; every client opens it by its link.

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/magi/lib/rpc"
	artifactsapi "github.com/catalystcommunity/magi/primitives/artifacts/api"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"

	magiapi "github.com/catalystcommunity/magi/magi/api"
	"github.com/catalystcommunity/magi/magi/model"
)

const pageMedia = "text/markdown; variant=magi-page"

func pageTools(obj func(map[string]any, ...string) map[string]any, str func(string) map[string]any) []model.Tool {
	return []model.Tool{{Name: "make_page", Description: "Make a page for the person, with a link that opens on all their devices: for an explanation, a plan, a table, or a diagram that is too long or too visual to say. Write Markdown; put a diagram in a ```mermaid block (flowchart, sequenceDiagram, gantt, classDiagram). Then give the person the link and say in one sentence what is on the page.", Parameters: obj(map[string]any{
		"title": str("Short title."),
		"text":  str("The page in Markdown. Diagrams in ```mermaid blocks."),
	}, "title", "text")}}
}

func (s *System) pageLink(id string) string {
	base := s.Cfg.PublicURL
	return base + "/#/page/" + id
}

// makePage stores a page for owner and gives the tool result and a ref.
func (s *System) makePage(ctx context.Context, owner, persona string, args map[string]string) (string, *conversationapi.Ref) {
	title, text := strings.TrimSpace(args["title"]), strings.TrimSpace(args["text"])
	if title == "" || text == "" {
		return "error: give a title and the text", nil
	}
	if len(text) > 200_000 {
		return "error: the page is longer than 200000 characters", nil
	}
	a, err := s.Artifacts.Put(s.AsService(ctx), artifactsapi.PutRequest{MediaType: pageMedia, Body: []byte(text), Owner: &owner,
		Labels: map[string]string{"kind": "page", "title": clip(title, 200), "persona": persona}})
	if err != nil {
		return "error: " + err.Error(), nil
	}
	link := s.pageLink(a.Digest)
	return fmt.Sprintf("Page ready: %s\nGive the person this link.", link), &conversationapi.Ref{Kind: "page", Ref: a.Digest, Title: ptr(clip(title, 200))}
}

func pageOf(a artifactsapi.Artifact, text string) magiapi.Page {
	return magiapi.Page{Id: a.Digest, Title: a.Labels["title"], Text: text, Persona: a.Labels["persona"], CreatedMs: a.CreatedMs}
}

func (m *Service) GetPage(ctx context.Context, req magiapi.GetPageRequest) (magiapi.Page, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return magiapi.Page{}, err
	}
	r, err := m.S.Artifacts.Get(AsCaller(ctx), artifactsapi.GetRequest{Digest: req.Id})
	if err != nil {
		return magiapi.Page{}, err
	}
	if r.Artifact.Labels["kind"] != "page" {
		return magiapi.Page{}, rpc.NotFound("no such page")
	}
	return pageOf(r.Artifact, string(r.Body)), nil
}

func (m *Service) ListPages(ctx context.Context, req magiapi.ListPagesRequest) (magiapi.ListPagesResponse, error) {
	if _, err := rpc.Caller(ctx); err != nil {
		return magiapi.ListPagesResponse{}, err
	}
	lim := uint64(50)
	if req.Limit != nil && *req.Limit > 0 && *req.Limit < 500 {
		lim = *req.Limit
	}
	l, err := m.S.Artifacts.List(AsCaller(ctx), artifactsapi.ListRequest{LabelKey: ptr("kind"), LabelValue: ptr("page"), Limit: &lim})
	if err != nil {
		return magiapi.ListPagesResponse{}, err
	}
	out := magiapi.ListPagesResponse{Pages: []magiapi.Page{}}
	for _, a := range l.Artifacts {
		out.Pages = append(out.Pages, pageOf(a, ""))
	}
	return out, nil
}
