package server_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/models/api"
	"github.com/catalystcommunity/magi/primitives/models/internal/testenv"
	"github.com/catalystcommunity/magi/primitives/models/server"
	"github.com/catalystcommunity/magi/primitives/models/store"
)

func ptr[T any](v T) *T { return &v }

func TestCatalog(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, testenv.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	keys := testenv.NewKeys(t)
	s := &rpc.Server{Verifier: keys.Verifier(t)}
	server.Register(s, &server.Service{DB: db})
	hs := httptest.NewServer(rpc.Mux(s, nil))
	t.Cleanup(hs.Close)
	client := func(sub string, scopes ...string) *api.ModelClient {
		return api.NewModelClient(rpc.NewClient(hs.URL, rpc.StaticToken(keys.Token(t, sub, scopes...))))
	}
	adm := client("service:magi", server.ScopeAdmin)
	user := client("user:alice@example.local")

	m := api.Model{Name: "gemma-4-12b-notetaker", Description: "Tuned for the note taker.",
		Endpoint: api.Endpoint{BaseUrl: "http://studio.revconn/v1", Model: "gemma-4-12b-notetaker"},
		Tags:     []string{"local", "schema", "notetaker"}, Enabled: true}
	if _, err := user.PutModel(ctx, api.PutModelRequest{Model: m}); rpc.CodeOf(err) != 403 {
		t.Fatalf("put without admin: %v", err)
	}
	if _, err := adm.PutModel(ctx, api.PutModelRequest{Model: api.Model{Name: "Bad Name", Endpoint: m.Endpoint}}); rpc.CodeOf(err) != 400 {
		t.Fatalf("bad name: %v", err)
	}
	got, err := adm.PutModel(ctx, api.PutModelRequest{Model: m})
	if err != nil || got.UpdatedMs == 0 || got.Scores == nil {
		t.Fatalf("put: %+v %v", got, err)
	}
	if _, err := adm.PutModel(ctx, api.PutModelRequest{Model: api.Model{Name: "glm-5.3", Endpoint: api.Endpoint{BaseUrl: "https://zen.example.local/v1", Model: "glm-5.3", KeyRef: ptr("zen")},
		Tags: []string{"hosted", "schema", "tools"}, Cost: api.Cost{InputPerMtok: 600000, OutputPerMtok: 2200000}, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	sc, err := adm.Score(ctx, api.ScoreRequest{Name: m.Name, Scores: map[string]float64{"notetaker/data5-test": 0.93}, Calibration: map[string]float64{"record": 1.57}})
	if err != nil || sc.Scores["notetaker/data5-test"] != 0.93 || sc.Calibration["record"] != 1.57 {
		t.Fatalf("score: %+v %v", sc, err)
	}
	if g, err := user.GetModel(ctx, api.GetModelRequest{Name: m.Name}); err != nil || g.Endpoint.Model != "gemma-4-12b-notetaker" {
		t.Fatalf("get: %+v %v", g, err)
	}
	if _, err := user.GetModel(ctx, api.GetModelRequest{Name: "nope"}); rpc.CodeOf(err) != 404 {
		t.Fatalf("get missing: %v", err)
	}
	if l, err := user.ListModels(ctx, api.ListModelsRequest{Tag: ptr("hosted")}); err != nil || len(l.Models) != 1 || l.Models[0].Name != "glm-5.3" {
		t.Fatalf("list: %+v %v", l, err)
	}
	if _, err := adm.PutTask(ctx, api.PutTaskRequest{Task: api.TaskDefault{Task: "note-taker",
		Selector: api.Selector{All: []string{"schema"}, RankBy: ptr("notetaker/data5-test")}}}); err != nil {
		t.Fatal(err)
	}
	if ts, err := user.ListTasks(ctx, api.ListTasksRequest{}); err != nil || len(ts.Tasks) != 1 {
		t.Fatalf("tasks: %+v %v", ts, err)
	}
	if tk, err := user.GetTask(ctx, api.GetTaskRequest{Task: "note-taker"}); err != nil || tk.Selector.All[0] != "schema" {
		t.Fatalf("get task: %+v %v", tk, err)
	}
	sel, err := user.Select(ctx, api.SelectRequest{Task: ptr("note-taker")})
	if err != nil || len(sel.Candidates) != 2 || sel.Candidates[0].Model.Name != m.Name {
		t.Fatalf("select: %+v %v", sel, err)
	}
	// A persona that asks for a hosted model by name, with no fallback.
	sel, err = user.Select(ctx, api.SelectRequest{Task: ptr("note-taker"), Selector: &api.Selector{Name: ptr("glm-5.3"), OnlyName: ptr(true)}})
	if err != nil || len(sel.Candidates) != 1 || sel.Candidates[0].Model.Name != "glm-5.3" || sel.Candidates[0].Reason != "named" {
		t.Fatalf("select by name: %+v %v", sel, err)
	}
	// A task with no default: only the selector.
	sel, err = user.Select(ctx, api.SelectRequest{Task: ptr("decide"), Selector: &api.Selector{None: []string{"hosted"}}})
	if err != nil || len(sel.Candidates) != 1 {
		t.Fatalf("select no default: %+v %v", sel, err)
	}
	if d, err := adm.DeleteModel(ctx, api.DeleteModelRequest{Name: "glm-5.3"}); err != nil || !d.Deleted {
		t.Fatalf("delete: %+v %v", d, err)
	}
}
