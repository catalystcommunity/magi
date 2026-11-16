package server_test

import (
	"context"
	"testing"
	"time"

	api "github.com/catalystcommunity/magi/primitives/identity/api"
	"github.com/catalystcommunity/magi/primitives/identity/server"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

// Role assignments: the editor by user and the editor by role, the
// identity:roles scope, and the list of the subjects that signed in.
func TestRoles(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	admin := e.client(e.keys.Token(t, "service:ops", server.ScopeRoles))
	user := e.client(e.keys.Token(t, person))
	other := "user:019d6ac9-58bf-7303-8a5a-e60fba633240@example.local"

	// The editor by user: set, then list by subject and by role.
	set, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: person,
		Roles: []string{"admin.chats", "egress.review", "admin.chats"}})
	if err != nil || len(set.Assignments) != 2 {
		t.Fatalf("set-subject-roles: %+v %v", set, err)
	}
	if set.Assignments[0].Role != "admin.chats" || set.Assignments[0].GrantedBy != "service:ops" || set.Assignments[0].GrantedMs == 0 {
		t.Fatalf("assignment: %+v", set.Assignments[0])
	}
	bySubject, err := admin.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(person)})
	if err != nil || len(bySubject.Assignments) != 2 || bySubject.Assignments[0].Subject != person {
		t.Fatalf("list by subject: %+v %v", bySubject, err)
	}
	byRole, err := admin.ListRoles(ctx, api.ListRolesRequest{Role: ptr("egress.review")})
	if err != nil || len(byRole.Assignments) != 1 || byRole.Assignments[0].Subject != person {
		t.Fatalf("list by role: %+v %v", byRole, err)
	}

	// The editor by role: a subject that stays keeps granted_ms, one that is
	// left out is removed.
	first := set.Assignments[0].GrantedMs
	setRole, err := admin.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats",
		Subjects: []string{person, other, person}})
	if err != nil || len(setRole.Assignments) != 2 {
		t.Fatalf("set-role-subjects: %+v %v", setRole, err)
	}
	var kept *api.RoleAssignment
	for i := range setRole.Assignments {
		if setRole.Assignments[i].Subject == person {
			kept = &setRole.Assignments[i]
		}
	}
	if kept == nil || kept.GrantedMs != first || kept.GrantedBy != "service:ops" {
		t.Fatalf("a subject that stays changed: %+v", kept)
	}
	after, err := admin.ListRoles(ctx, api.ListRolesRequest{Role: ptr("egress.review")})
	if err != nil || len(after.Assignments) != 1 || after.Assignments[0].Subject != person {
		t.Fatalf("another role kept its subject: %+v %v", after, err)
	}
	// An empty list removes every subject of the role.
	gone, err := admin.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats", Subjects: nil})
	if err != nil || len(gone.Assignments) != 0 {
		t.Fatalf("empty set-role-subjects: %+v %v", gone, err)
	}

	// A caller without the scope is denied on each operation, but it can
	// list its own roles.
	if _, err := user.ListRoles(ctx, api.ListRolesRequest{Role: ptr("egress.review")}); code(err) != 403 {
		t.Fatalf("list by role without scope: %v", err)
	}
	if _, err := user.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(other)}); code(err) != 403 {
		t.Fatalf("list another subject without scope: %v", err)
	}
	if _, err := user.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: person, Roles: []string{"admin.chats"}}); code(err) != 403 {
		t.Fatalf("set-subject-roles without scope: %v", err)
	}
	if _, err := user.SetRoleSubjects(ctx, api.SetRoleSubjectsRequest{Role: "admin.chats", Subjects: []string{person}}); code(err) != 403 {
		t.Fatalf("set-role-subjects without scope: %v", err)
	}
	if _, err := user.ListUsers(ctx, api.ListUsersRequest{}); code(err) != 403 {
		t.Fatalf("list-users without scope: %v", err)
	}
	own, err := user.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(person)})
	if err != nil || len(own.Assignments) != 1 || own.Assignments[0].Role != "egress.review" {
		t.Fatalf("own roles: %+v %v", own, err)
	}

	// Bad input: a role name, and both or neither of subject and role.
	if _, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: person, Roles: []string{"Admin Chats"}}); code(err) != 400 {
		t.Fatalf("bad role name: %v", err)
	}
	if _, err := admin.ListRoles(ctx, api.ListRolesRequest{Subject: ptr(person), Role: ptr("admin.chats")}); code(err) != 400 {
		t.Fatalf("both subject and role: %v", err)
	}
	if _, err := admin.ListRoles(ctx, api.ListRolesRequest{}); code(err) != 400 {
		t.Fatalf("neither subject nor role: %v", err)
	}
	if _, err := admin.SetSubjectRoles(ctx, api.SetSubjectRolesRequest{Subject: "", Roles: []string{"admin.chats"}}); code(err) != 400 {
		t.Fatalf("empty subject: %v", err)
	}
}

// ListUsers gives the subjects that signed in, newest first.
func TestListUsers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	admin := e.client(e.keys.Token(t, "service:ops", server.ScopeRoles))
	now := time.Now().UnixMilli()
	for _, s := range []struct {
		subject string
		at      int64
	}{
		{person, now - 3_600_000},
		{"user:019d6ac9-58bf-7303-8a5a-e60fba633240@example.local", now},
		{"user:019d6ac9-58bf-7303-8a5a-e60fba633241@example.local", now - 60_000},
	} {
		if err := e.svc.DB.CreateSession(ctx, store.Session{ID: "sess_" + s.subject[len(s.subject)-6:],
			Subject: s.subject, UserID: "uid", Domain: "example.local", AuthenticatedMs: s.at, ExpiresMs: now + 3_600_000}); err != nil {
			t.Fatal(err)
		}
	}
	// A second session of the first subject is older: it does not change the order.
	if err := e.svc.DB.CreateSession(ctx, store.Session{ID: "sess_old", Subject: person,
		UserID: "uid", Domain: "example.local", AuthenticatedMs: now - 10_000_000, ExpiresMs: now + 3_600_000}); err != nil {
		t.Fatal(err)
	}
	r, err := admin.ListUsers(ctx, api.ListUsersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Users) != 3 {
		t.Fatalf("users: %+v", r.Users)
	}
	want := []string{"user:019d6ac9-58bf-7303-8a5a-e60fba633240@example.local",
		"user:019d6ac9-58bf-7303-8a5a-e60fba633241@example.local", person}
	for i, u := range r.Users {
		if u.Subject != want[i] {
			t.Fatalf("user %d = %s, want %s", i, u.Subject, want[i])
		}
		if u.LastSeenMs == 0 {
			t.Fatalf("user %d has no last_seen_ms", i)
		}
	}
	// The limit is applied.
	two := uint64(2)
	r, err = admin.ListUsers(ctx, api.ListUsersRequest{Limit: &two})
	if err != nil || len(r.Users) != 2 {
		t.Fatalf("limited users: %+v %v", r.Users, err)
	}
}
