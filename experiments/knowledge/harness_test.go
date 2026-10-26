package knowledge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	wire "opal.local/experiments/knowledge/generated/go"
)

type harness struct {
	admin          *pgxpool.Pool
	dsn, container string
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func token(t testing.TB) string {
	t.Helper()
	b := make([]byte, 8)
	_, err := rand.Read(b)
	must(t, err)
	return hex.EncodeToString(b)
}
func docker(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}

func startHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv("OPAL_KNOWLEDGE_INTEGRATION") != "1" {
		t.Skip("set OPAL_KNOWLEDGE_INTEGRATION=1 to create a disposable database")
	}
	data, err := os.ReadFile("toolchain.json")
	must(t, err)
	var pin struct {
		Image string `json:"postgres_image"`
	}
	must(t, json.Unmarshal(data, &pin))
	if !strings.HasPrefix(pin.Image, "postgres@sha256:") {
		t.Fatal("database image must be digest-pinned")
	}
	name := "opal-knowledge-" + token(t)
	// Docker can change a dynamically published port on restart. Choose an unused
	// loopback port, then pin that number for this container's lifetime. A bind
	// race fails container startup; it never stops another service.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	must(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	must(t, listener.Close())
	id := docker(t, "run", "--pull=never", "-d", "--name", name,
		"--label", "opal.experiment=knowledge", "--memory", "1g", "--cpus", "2",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port), "-e", "POSTGRES_PASSWORD=synthetic-only",
		"-e", "POSTGRES_DB=opal_knowledge", pin.Image)
	if len(id) != 64 {
		t.Fatal("unexpected container ID")
	}
	t.Cleanup(func() {
		label := docker(t, "inspect", "--format", `{{index .Config.Labels "opal.experiment"}}`, id)
		if label != "knowledge" {
			t.Error("refusing cleanup of unlabelled container")
			return
		}
		docker(t, "rm", "-f", "-v", id)
		t.Log("Removed only the disposable synthetic database:", name)
	})
	address := docker(t, "port", id, "5432/tcp")
	t.Log("Initial binding:", address)
	if !strings.HasPrefix(address, "127.0.0.1:") || strings.Contains(address, "\n") {
		t.Fatal("unexpected database binding")
	}
	dsn := "postgres://postgres:synthetic-only@" + address + "/opal_knowledge?sslmode=disable"
	pool, err := pgxpool.New(context.Background(), dsn)
	must(t, err)
	t.Cleanup(pool.Close)
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = pool.Ping(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disposable database did not become ready:", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, err = pool.Exec(context.Background(), `CREATE ROLE opal_fixture LOGIN PASSWORD 'synthetic-runtime' NOSUPERUSER NOBYPASSRLS`)
	must(t, err)
	var version string
	must(t, pool.QueryRow(context.Background(), `SELECT version()`).Scan(&version))
	t.Log(version, "image", pin.Image, "limits: 2 CPU, 1 GiB")
	return &harness{pool, strings.Replace(dsn, "postgres:synthetic-only@", "opal_fixture:synthetic-runtime@", 1), id}
}

var primary = wire.IdentityRef{Domain: "primary.opal.test", AccountId: "opal"}
var secondary = wire.IdentityRef{Domain: "secondary.opal.test", AccountId: "opal"}
var alice = wire.IdentityRef{Domain: "primary.opal.test", AccountId: "alice"}
var bob = wire.IdentityRef{Domain: "secondary.opal.test", AccountId: "bob"}
var janaya = wire.IdentityRef{Domain: "primary.opal.test", AccountId: "janaya"}
var localProcessor = wire.IdentityRef{Domain: "primary.opal.test", AccountId: "local-processor"}
var privateScope = wire.ScopeRef{Authority: primary, ScopeId: "alice-private"}
var workScope = wire.ScopeRef{Authority: primary, ScopeId: "work"}
var purposes = []string{"capture", "history", "recall", "enrich", "route", "assess"}
var fixedTime = time.Date(2026, 10, 11, 12, 0, 0, 123456789, time.UTC)

type fixture struct {
	s      *store
	admin  *pgxpool.Pool
	access wire.ContextAccess
}

func (h *harness) fixture(t *testing.T) *fixture {
	t.Helper()
	schema := "k_" + token(t)
	_, err := h.admin.Exec(context.Background(), "CREATE SCHEMA "+schema)
	must(t, err)
	config, err := pgxpool.ParseConfig(h.dsn)
	must(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.MaxConns = 12
	adminConfig := config.Copy()
	adminConfig.ConnConfig.User, adminConfig.ConnConfig.Password = "postgres", "synthetic-only"
	admin, err := pgxpool.NewWithConfig(context.Background(), adminConfig)
	must(t, err)
	t.Cleanup(admin.Close)
	_, err = admin.Exec(context.Background(), migration)
	must(t, err)
	_, err = admin.Exec(context.Background(), "GRANT USAGE ON SCHEMA "+schema+" TO opal_fixture; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA "+schema+" TO opal_fixture")
	must(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	must(t, err)
	t.Cleanup(pool.Close)
	f := &fixture{s: &store{db: pool, authority: primary, now: func() time.Time { return fixedTime }}, admin: admin,
		access: wire.ContextAccess{Actor: alice, ControlScope: privateScope, Purpose: "capture", Processor: localProcessor}}
	for _, scope := range []wire.ScopeRef{privateScope, workScope} {
		f.grant(t, alice, scope, true)
		for _, purpose := range purposes {
			_, err = admin.Exec(context.Background(), `INSERT INTO processor_grants VALUES($1,$2,$3)`, identity(localProcessor), scopeKey(scope), purpose)
			must(t, err)
		}
	}
	f.grant(t, bob, workScope, false)
	for _, session := range []string{"C", "E"} {
		_, err = admin.Exec(context.Background(), `INSERT INTO sessions(authority,id,scopes) VALUES($1,$2,$3)`, identity(primary), session, []string{scopeKey(privateScope)})
		must(t, err)
	}
	_, err = admin.Exec(context.Background(), `INSERT INTO sessions(authority,id,scopes) VALUES($1,'group',$2)`, identity(primary), []string{scopeKey(workScope)})
	must(t, err)
	return f
}
func (f *fixture) grant(t *testing.T, actor wire.IdentityRef, scope wire.ScopeRef, write bool) {
	t.Helper()
	_, err := f.admin.Exec(context.Background(), `INSERT INTO scope_readers VALUES($1,$2,$3) ON CONFLICT(subject,scope) DO UPDATE SET can_write=excluded.can_write`, identity(actor), scopeKey(scope), write)
	must(t, err)
}
func header(kind, id string, scopes ...wire.ScopeRef) wire.EvidenceHeader {
	if len(scopes) == 0 {
		scopes = []wire.ScopeRef{privateScope}
	}
	return wire.EvidenceHeader{Ref: wire.RecordRef{Authority: primary, Kind: kind, RecordId: id}, Producer: alice,
		ReceivedAt: fixedTime, ObservedAt: &fixedTime, StreamId: id, SourceSequence: 1,
		Restrictions: wire.Restrictions{RequiredScopes: scopes, PermittedPurposes: slicesClone(purposes)}, Capture: "complete"}
}
func slicesClone[T any](s []T) []T { return append([]T{}, s...) }
func interaction(id, text string, scopes ...wire.ScopeRef) wire.InteractionRecord {
	return wire.InteractionRecord{Header: header("interaction", id, scopes...), PointId: "synthetic-desk", Channel: "text", Content: text}
}
func (f *fixture) put(t *testing.T, record wire.EvidenceRecord) wire.Receipt {
	t.Helper()
	h, _, _, _, err := describe(record)
	must(t, err)
	r, err := f.s.put(context.Background(), f.access, h.Ref.RecordId, record, nil)
	must(t, err)
	return r
}
func (f *fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	must(t, f.admin.QueryRow(context.Background(), fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&n))
	return n
}
