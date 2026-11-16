package server_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
	"github.com/catalystcommunity/magi/primitives/identity/server"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

// The subjects of the role tests.
var (
	rolePerson = "user:019d6ac9-58bf-7303-8a5a-e60fba633240@example.local"
	roleOther  = "user:019d6ac9-58bf-7303-8a5a-e60fba633241@example.local"
)

// msg returns the message of a service error.
func msg(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return ""
}

// Role assignments: the editor by user and the editor by role, the
// identity:roles scope, and the input rules.
func TestRoles(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	admin := e.client(e.keys.Token(t, "service:ops", server.ScopeRoles))
	user := e.client(e.keys.Token(t, rolePerson))

	// An earlier run of this test on the same database must not matter: the
	// subjects and the roles that this test uses start with no rows.
	for _, s := range []string{rolePerson, roleOther} {
		if _, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: s}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []string{"admin.chats", "egress.review"} {
		if _, err := admin.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: r}); err != nil {
			t.Fatal(err)
		}
	}

	// The editor by user: set, then list by subject and by role. A duplicate
	// in the request counts once.
	set, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: rolePerson,
		Roles: []string{"admin.chats", "egress.review", "admin.chats"}})
	if err != nil || len(set.Assignments) != 2 {
		t.Fatalf("set-subject-roles: %+v %v", set, err)
	}
	if set.Assignments[0].Role != "admin.chats" || set.Assignments[0].GrantedBy != "service:ops" || set.Assignments[0].GrantedMs == 0 {
		t.Fatalf("assignment: %+v", set.Assignments[0])
	}
	bySubject, err := admin.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(rolePerson)})
	if err != nil || len(bySubject.Assignments) != 2 || bySubject.Assignments[0].Subject != rolePerson {
		t.Fatalf("list by subject: %+v %v", bySubject, err)
	}
	byRole, err := admin.ListRoles(ctx, api.ListRolesRequest{Role: ptr("egress.review")})
	if err != nil || len(byRole.Assignments) != 1 || byRole.Assignments[0].Subject != rolePerson {
		t.Fatalf("list by role: %+v %v", byRole, err)
	}

	// The editor by role: a subject that stays keeps granted_by and
	// granted_ms, one that is left out is removed. The clock moves first, so
	// a row that the code wrongly deletes and inserts again gets a new
	// granted_ms and the test sees it.
	first := set.Assignments[0].GrantedMs
	e.offset.Store(60_000)
	setRole, err := admin.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats",
		Subjects: []string{rolePerson, roleOther, rolePerson}})
	if err != nil || len(setRole.Assignments) != 2 {
		t.Fatalf("set-role-subjects: %+v %v", setRole, err)
	}
	var kept *api.RoleAssignment
	for i := range setRole.Assignments {
		if setRole.Assignments[i].Subject == rolePerson {
			kept = &setRole.Assignments[i]
		}
	}
	if kept == nil || kept.GrantedMs != first || kept.GrantedBy != "service:ops" {
		t.Fatalf("a subject that stays changed: %+v", kept)
	}
	after, err := admin.ListRoles(ctx, api.ListRolesRequest{Role: ptr("egress.review")})
	if err != nil || len(after.Assignments) != 1 || after.Assignments[0].Subject != rolePerson {
		t.Fatalf("another role kept its subject: %+v %v", after, err)
	}
	// The editor by user keeps the rows of the roles that stay too. The
	// clock moves again for the same reason.
	e.offset.Store(120_000)
	again, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: rolePerson,
		Roles: []string{"admin.chats", "egress.review"}})
	if err != nil || len(again.Assignments) != 2 || again.Assignments[0].GrantedMs != first {
		t.Fatalf("a role that stays changed: %+v %v", again, err)
	}
	// A subject that is left out goes, and the one that stays keeps
	// granted_ms.
	e.offset.Store(180_000)
	leftOut, err := admin.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats", Subjects: []string{rolePerson}})
	if err != nil || len(leftOut.Assignments) != 1 || leftOut.Assignments[0].Subject != rolePerson || leftOut.Assignments[0].GrantedMs != first {
		t.Fatalf("a subject that is left out stayed: %+v %v", leftOut, err)
	}
	// An empty list removes every subject of the role.
	gone, err := admin.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats"})
	if err != nil || len(gone.Assignments) != 0 {
		t.Fatalf("empty set-role-subjects: %+v %v", gone, err)
	}

	// A caller without the scope is denied on each operation, but it can
	// list its own roles.
	if _, err := user.ListRoles(ctx, api.ListRolesRequest{Role: ptr("egress.review")}); code(err) != 403 {
		t.Fatalf("list by role without scope: %v", err)
	}
	if _, err := user.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(roleOther)}); code(err) != 403 {
		t.Fatalf("list another subject without scope: %v", err)
	}
	if _, err := user.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: rolePerson, Roles: []string{"admin.chats"}}); code(err) != 403 {
		t.Fatalf("set-subject-roles without scope: %v", err)
	}
	if _, err := user.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats", Subjects: []string{rolePerson}}); code(err) != 403 {
		t.Fatalf("set-role-subjects without scope: %v", err)
	}
	if _, err := user.ListUsers(ctx, api.ListUsersRequest{}); code(err) != 403 {
		t.Fatalf("list-users without scope: %v", err)
	}
	own, err := user.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(rolePerson)})
	if err != nil || len(own.Assignments) != 1 || own.Assignments[0].Role != "egress.review" {
		t.Fatalf("own roles: %+v %v", own, err)
	}

	// Bad input: a role name, a subject, and both or neither of subject and
	// role. Each error names its rule.
	if _, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: rolePerson, Roles: []string{"Admin Chats"}}); code(err) != 400 || !strings.Contains(msg(err), "role must be") {
		t.Fatalf("bad role name: %v", err)
	}
	if _, err := admin.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats", Subjects: []string{""}}); code(err) != 400 || !strings.Contains(msg(err), "subject must be") {
		t.Fatalf("empty subject in the list: %v", err)
	}
	if _, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Roles: []string{"admin.chats"}}); code(err) != 400 || !strings.Contains(msg(err), "subject must be") {
		t.Fatalf("empty subject: %v", err)
	}
	if _, err := admin.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(rolePerson), Role: ptr("admin.chats")}); code(err) != 400 || !strings.Contains(msg(err), "exactly one of subject or role") {
		t.Fatalf("both subject and role: %v", err)
	}
	if _, err := admin.ListRoles(ctx, api.ListRolesRequest{}); code(err) != 400 || !strings.Contains(msg(err), "exactly one of subject or role") {
		t.Fatalf("neither subject nor role: %v", err)
	}
}

// ListUsers gives the subjects that signed in, newest first, with the name
// of the newest session that has one.
func TestListUsers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	admin := e.client(e.keys.Token(t, "service:ops", server.ScopeRoles))
	now := time.Now().UnixMilli()
	carol := "user:019d6ac9-58bf-7303-8a5a-e60fba633242@example.local"
	// The session IDs are unique, so an earlier run of this test on the same
	// database leaves no row with the same key.
	prefix := "sess_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	// One session per person, newest first.
	people := []struct {
		subject, name string
		at            int64
	}{
		{roleOther, "", now},
		{carol, "Carol", now - 60_000},
		{rolePerson, "Alice", now - 3_600_000},
	}
	for i, p := range people {
		if err := e.svc.DB.CreateSession(ctx, store.Session{ID: prefix + "_" + strconv.Itoa(i),
			Subject: p.subject, UserID: "uid", Domain: "example.local", Name: p.name,
			AuthenticatedMs: p.at, ExpiresMs: now + 3_600_000}); err != nil {
			t.Fatal(err)
		}
	}
	// Two more sessions of the last person, both without a name: one newer
	// than the named one and one older. The newest sign-in time wins, and the
	// name still comes from the session that has one.
	for _, s := range []struct {
		id string
		at int64
	}{
		{prefix + "_new", now - 1_000},
		{prefix + "_old", now - 10_000_000},
	} {
		if err := e.svc.DB.CreateSession(ctx, store.Session{ID: s.id, Subject: rolePerson,
			UserID: "uid", Domain: "example.local", AuthenticatedMs: s.at, ExpiresMs: now + 3_600_000}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := admin.ListUsers(ctx, api.ListUsersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Each subject counts once, and the three subjects come newest first:
	// other, then Alice, then Carol. Alice's nameless session at now - 1 s
	// is newer than Carol's session, so Alice comes before Carol. The rows
	// of earlier runs do not change this.
	pos := map[string]int{}
	for i, u := range r.Users {
		if _, ok := pos[u.Subject]; ok {
			t.Fatalf("%s is listed twice: %+v", u.Subject, r.Users)
		}
		pos[u.Subject] = i
	}
	for _, p := range people {
		if _, ok := pos[p.subject]; !ok {
			t.Fatalf("%s is not in the list: %+v", p.subject, r.Users)
		}
	}
	if !(pos[roleOther] < pos[rolePerson] && pos[rolePerson] < pos[carol]) {
		t.Fatalf("the subjects are not newest first: %+v", r.Users)
	}
	for _, p := range people {
		u := r.Users[pos[p.subject]]
		if str(u.Name) != p.name {
			t.Fatalf("%s name = %q, want %q", p.subject, str(u.Name), p.name)
		}
	}
	// The newest sign-in of the last person is the nameless session.
	if got := r.Users[pos[rolePerson]].LastSeenMs; got != uint64(now-1_000) {
		t.Fatalf("%s last_seen_ms = %d, want %d", rolePerson, got, now-1_000)
	}
	// The limit is applied: the list stops after limit users.
	two := uint64(2)
	limited, err := admin.ListUsers(ctx, api.ListUsersRequest{Limit: &two})
	if err != nil || len(limited.Users) != 2 {
		t.Fatalf("limited users: %+v %v", limited.Users, err)
	}
	for i := range limited.Users {
		if limited.Users[i].Subject != r.Users[i].Subject {
			t.Fatalf("limited user %d = %s, want %s", i, limited.Users[i].Subject, r.Users[i].Subject)
		}
	}
}
