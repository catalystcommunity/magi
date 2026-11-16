package server

// Role assignments. Magi gets granular admin roles, for example
// "admin.tasks". The primitive does not know the meaning of a role name:
// Magi defines which names exist and what they permit. Identity only keeps
// the assignments, because it owns the subjects and the logins.
//
// A caller with the scope identity:roles edits and lists all assignments.
// Any caller can list its own roles, so a client can hide what it cannot do.

import (
	"context"
	"regexp"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	api "github.com/catalystcommunity/magi/primitives/identity/api"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

// ScopeRoles permits the role operations of this primitive.
const ScopeRoles = "identity:roles"

// Defaults of list-users.
const (
	usersDefault = 100
	usersMax     = 1000
)

var roleRE = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

func (s *Service) apiRoles(rows []store.RoleAssignment) api.ListRolesResponse {
	out := api.ListRolesResponse{Assignments: []api.RoleAssignment{}}
	for _, r := range rows {
		out.Assignments = append(out.Assignments, api.RoleAssignment{Subject: r.Subject, Role: r.Role,
			GrantedBy: r.GrantedBy, GrantedMs: uint64(r.GrantedMs)})
	}
	return out
}

// requireRoles checks the scope for the role operations.
func requireRoles(p authn.Principal) error {
	if !p.Has(ScopeRoles) {
		return rpc.Denied("%s is required", ScopeRoles)
	}
	return nil
}

func (s *Service) ListRoles(ctx context.Context, req api.ListRolesRequest) (api.ListRolesResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	subject, role := "", ""
	if req.Subject != nil {
		subject = *req.Subject
	}
	if req.Role != nil {
		role = *req.Role
	}
	if (subject == "") == (role == "") {
		return api.ListRolesResponse{}, rpc.Invalid("give exactly one of subject or role")
	}
	if subject != "" && subject != p.Subject && !p.Has(ScopeRoles) {
		return api.ListRolesResponse{}, rpc.Denied("%s is required", ScopeRoles)
	}
	if role != "" {
		if err := requireRoles(p); err != nil {
			return api.ListRolesResponse{}, err
		}
		if !roleRE.MatchString(role) {
			return api.ListRolesResponse{}, rpc.Invalid("role must be 1 to 64 characters of a-z 0-9 . _ -")
		}
	}
	rows, err := s.DB.Roles(ctx, subject, role)
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	return s.apiRoles(rows), nil
}

// checkSubjects validates the subjects of a set operation. Duplicates count
// once.
func checkSubjects(subjects []string) ([]string, error) {
	seen := make(map[string]bool, len(subjects))
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		if s == "" || len(s) > 128 {
			return nil, rpc.Invalid("subject must be 1 to 128 characters")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// checkRoles validates the role names of a set operation. Duplicates count
// once.
func checkRoles(roles []string) ([]string, error) {
	seen := make(map[string]bool, len(roles))
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if !roleRE.MatchString(r) {
			return nil, rpc.Invalid("role must be 1 to 64 characters of a-z 0-9 . _ -")
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// SetSubjectRoles replaces the roles of one subject: the editor by user.
func (s *Service) SetSubjectRoles(ctx context.Context, req api.SetSubjectRolesRequest) (api.ListRolesResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	if err := requireRoles(p); err != nil {
		return api.ListRolesResponse{}, err
	}
	if req.Subject == "" || len(req.Subject) > 128 {
		return api.ListRolesResponse{}, rpc.Invalid("subject must be 1 to 128 characters")
	}
	roles, err := checkRoles(req.Roles)
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	rows, err := s.DB.SetSubjectRoles(ctx, req.Subject, roles, p.Subject, s.now().UnixMilli())
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	s.log().Info("subject roles set", "by", p.Subject, "subject", req.Subject, "count", len(roles))
	return s.apiRoles(rows), nil
}

// SetRoleSubjects replaces the subjects of one role: the editor by role.
func (s *Service) SetRoleSubjects(ctx context.Context, req api.SetRoleSubjectsRequest) (api.ListRolesResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	if err := requireRoles(p); err != nil {
		return api.ListRolesResponse{}, err
	}
	if !roleRE.MatchString(req.Role) {
		return api.ListRolesResponse{}, rpc.Invalid("role must be 1 to 64 characters of a-z 0-9 . _ -")
	}
	subjects, err := checkSubjects(req.Subjects)
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	rows, err := s.DB.SetRoleSubjects(ctx, req.Role, subjects, p.Subject, s.now().UnixMilli())
	if err != nil {
		return api.ListRolesResponse{}, err
	}
	s.log().Info("role subjects set", "by", p.Subject, "role", req.Role, "count", len(subjects))
	return s.apiRoles(rows), nil
}

// ListUsers gives the subjects that signed in, newest first, so an editor
// can pick a person.
func (s *Service) ListUsers(ctx context.Context, req api.ListUsersRequest) (api.ListUsersResponse, error) {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return api.ListUsersResponse{}, err
	}
	if err := requireRoles(p); err != nil {
		return api.ListUsersResponse{}, err
	}
	limit := usersDefault
	if req.Limit != nil && *req.Limit > 0 {
		limit = min(int(*req.Limit), usersMax)
	}
	rows, err := s.DB.KnownUsers(ctx, limit)
	if err != nil {
		return api.ListUsersResponse{}, err
	}
	out := api.ListUsersResponse{Users: []api.KnownUser{}}
	for _, u := range rows {
		out.Users = append(out.Users, api.KnownUser{Subject: u.Subject, LastSeenMs: uint64(u.LastSeenMs)})
	}
	return out, nil
}
