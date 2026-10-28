package server

import (
	"net/http"
	"net/http/cgi"
	"os/exec"
	"strings"

	"github.com/catalystcommunity/magi/lib/authn"
)

// GitHTTP serves Git smart HTTP at /git/<name>.git/ with git http-backend.
// The client uses Basic authentication: any user name, and a Magi bearer
// token as the password. Clone and push both need access to the repository.
func (s *Service) GitHTTP(v *authn.Verifier) http.Handler {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		gitPath = "git"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/git/")
		repo, sub, ok := strings.Cut(rest, ".git/")
		if !ok || validName(repo) != nil || strings.Contains(sub, "..") {
			http.NotFound(w, r)
			return
		}
		// Only the smart protocol is served.
		svc := r.URL.Query().Get("service")
		switch {
		case sub == "info/refs" && r.Method == http.MethodGet && (svc == "git-upload-pack" || svc == "git-receive-pack"):
		case (sub == "git-upload-pack" || sub == "git-receive-pack") && r.Method == http.MethodPost:
		default:
			http.Error(w, "only Git smart HTTP is supported", http.StatusForbidden)
			return
		}
		_, token, hasAuth := r.BasicAuth()
		if !hasAuth || v == nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="magi"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		p, err := v.Verify(token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="magi"`)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		m, err := s.readMeta(repo)
		if err != nil || !canAccess(p, m.Owner) {
			http.NotFound(w, r)
			return
		}
		if sub == "git-receive-pack" {
			defer s.lock(repo)()
		}
		h := &cgi.Handler{
			Path: gitPath,
			Root: "/git",
			Args: []string{"http-backend"},
			Env: []string{
				"GIT_PROJECT_ROOT=" + s.Dir,
				"GIT_HTTP_EXPORT_ALL=1",
				"GIT_CONFIG_NOSYSTEM=1",
				// REMOTE_USER enables receive-pack in http-backend.
				"REMOTE_USER=" + p.Subject,
			},
		}
		h.ServeHTTP(w, r)
	})
}
