package server

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/catalystcommunity/magi/primitives/identity/linkkeys"
	"github.com/catalystcommunity/magi/primitives/identity/store"
)

// Routes adds the browser login routes to m.
func (s *Service) Routes(m *http.ServeMux) {
	m.HandleFunc("GET /login/start", s.startPage)
	m.HandleFunc("POST /login/start", s.startPost)
	m.HandleFunc("GET /login/callback", s.callback)
}

var page = template.Must(template.New("p").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Magi sign in</title><style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem;line-height:1.5}
button{font-size:1rem;padding:.5rem 1.5rem}</style></head><body>
<h1>{{.Title}}</h1>{{if .Text}}<p>{{.Text}}</p>{{end}}
{{if .Code}}<p><code style="font-size:1.1rem">{{.Masked}}</code></p>
<p><button type="button" id="copy" data-code="{{.Code}}">Copy code</button></p>
<script nonce="{{.Nonce}}">document.getElementById("copy").onclick=function(){var b=this;navigator.clipboard.writeText(b.dataset.code).then(function(){b.textContent="Copied"},function(){b.textContent="Copy failed"})}</script>{{end}}
{{if .Start}}<p>Identity: <b>{{.Identity}}</b>{{if .Client}}<br>Program: <b>{{.Client}}</b>{{end}}</p>
<p>Continue only if you started this sign-in.</p>
<form method="post" action="start"><input type="hidden" name="t" value="{{.Start}}"><button type="submit">Continue</button></form>{{end}}
{{if .Back}}<p><a href="{{.Back}}">Back to Magi</a></p>{{end}}
</body></html>`))

type view struct {
	Title, Text, Start, Identity, Client, Back, Code string
	Masked, Nonce                                    string // set by render
}

func render(w http.ResponseWriter, code int, v view) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	// "no-referrer" makes browsers send "Origin: null" on the form post,
	// which sameOrigin refuses. "same-origin" still hides the start token
	// from other origins.
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	csp := "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https:; frame-ancestors 'none'"
	if v.Code != "" {
		// The page shows only the end of the code. The Copy button needs
		// the one inline script that has this nonce.
		v.Nonce = token()
		v.Masked = strings.Repeat("•", 8) + v.Code[max(0, len(v.Code)-6):]
		csp += "; script-src 'nonce-" + v.Nonce + "'"
	}
	h.Set("Content-Security-Policy", csp)
	w.WriteHeader(code)
	page.Execute(w, v)
}

var startRE = regexp.MustCompile(`^lst_[A-Za-z0-9_-]{32}$`)

func (s *Service) startPage(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	l, err := s.DB.LoginByStart(r.Context(), store.Hash(t))
	if !startRE.MatchString(t) || errors.Is(err, store.ErrNotFound) {
		render(w, http.StatusNotFound, view{Title: "Unknown sign-in link", Text: "Start the sign in again from your program."})
		return
	}
	if err != nil {
		render(w, http.StatusServiceUnavailable, view{Title: "Sign in is not available", Text: "Try again later."})
		return
	}
	if l.Status != store.Pending || s.now().UnixMilli() >= l.ExpiresMs {
		render(w, http.StatusGone, view{Title: "This sign-in link was used or expired", Text: "Start the sign in again from your program."})
		return
	}
	render(w, http.StatusOK, view{Title: "Sign in to Magi", Start: t, Identity: l.Identity, Client: l.Client})
}

// sameOrigin accepts a form post only from the Magi origin. Without this
// check, another site could post the start token from the victim's browser
// and set the binding cookie there.
func (s *Service) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	o := r.Header.Get("Origin")
	return o != "" && strings.EqualFold(strings.TrimRight(o, "/"), s.origin())
}

func (s *Service) origin() string {
	u, err := url.Parse(s.PublicURL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func cookieName(stateHash string) string { return "magi_login_" + stateHash[:16] }

func (s *Service) startPost(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		render(w, http.StatusForbidden, view{Title: "Request refused", Text: "The request did not come from the Magi sign-in page."})
		return
	}
	t := r.PostFormValue("t")
	binding := token()
	l, err := s.DB.Start(r.Context(), store.Hash(t), store.Hash(binding), s.now().UnixMilli())
	if !startRE.MatchString(t) || errors.Is(err, store.ErrNotFound) {
		render(w, http.StatusGone, view{Title: "This sign-in link was used or expired", Text: "Start the sign in again from your program."})
		return
	}
	if err != nil {
		render(w, http.StatusServiceUnavailable, view{Title: "Sign in is not available", Text: "Try again later."})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName(l.StateHash), Value: binding, Path: "/login/callback",
		MaxAge: int(time.UnixMilli(l.ExpiresMs).Sub(s.now()).Seconds()) + 1, HttpOnly: true,
		Secure: strings.HasPrefix(s.PublicURL, "https://"), SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, l.IDPURL, http.StatusSeeOther)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, enc := q.Get("lk_state"), q.Get("encrypted_token")
	if state == "" || len(state) > 256 {
		render(w, http.StatusBadRequest, view{Title: "Sign in failed", Text: "The callback has no login state."})
		return
	}
	ctx := r.Context()
	l, err := s.DB.TakeCallback(ctx, store.Hash(state))
	if errors.Is(err, store.ErrNotFound) {
		render(w, http.StatusConflict, view{Title: "Sign in failed", Text: "This sign-in callback is unknown or was already used."})
		return
	}
	if err != nil {
		render(w, http.StatusServiceUnavailable, view{Title: "Sign in failed", Text: "Try again later."})
		return
	}
	finish := func(status int, msg string) {
		// Finish must run even if the browser goes away.
		if err := s.DB.Finish(context.WithoutCancel(ctx), l.LoginHash, store.Failed, msg, "", "", ""); err != nil {
			s.log().Error("finish login", "err", err)
		}
		render(w, status, view{Title: "Sign in failed", Text: msg, Back: l.ReturnURL})
	}
	c, err := r.Cookie(cookieName(l.StateHash))
	if err != nil || !equal(store.Hash(c.Value), l.BindingHash) {
		finish(http.StatusForbidden, "This browser did not start this sign in. Start again from your program, in this browser.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/login/callback", MaxAge: -1})
	now := s.now()
	if now.UnixMilli() >= l.ExpiresMs {
		finish(http.StatusGone, "The sign in expired. Start again from your program.")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	v, err := s.LinkKeys.Complete(ctx, linkkeys.Pending{Nonce: l.Nonce, Domain: l.Domain, APIBase: l.APIBase, CallbackURL: l.CallbackURL}, enc)
	if err != nil {
		s.log().Warn("login rejected", "domain", l.Domain, "err", err)
		var le *linkkeys.LoginError
		if errors.As(err, &le) {
			finish(http.StatusForbidden, "LinkKeys did not verify the sign in ("+le.Code+").")
		} else {
			finish(http.StatusBadGateway, "LinkKeys could not complete the sign in. Try again.")
		}
		return
	}
	subject, err := s.subject(v)
	if err != nil {
		finish(http.StatusForbidden, err.Error())
		return
	}
	sess := store.Session{ID: "ses_" + token(), Subject: subject, UserID: v.UserID, Domain: v.Domain, Name: displayName(v),
		AuthenticatedMs: now.UnixMilli(), ExpiresMs: now.Add(s.sessionTTL()).UnixMilli()}
	if err := s.DB.CreateSession(ctx, sess); err != nil {
		s.log().Error("create session", "err", err)
		finish(http.StatusServiceUnavailable, "Magi could not save the sign in. Try again.")
		return
	}
	// The code goes only to this browser. The program that polls must show
	// it, so a person who signs in with a link that someone else started
	// does not give that person a token (pasted, or an app scheme).
	code := "lcd_" + token()
	if err := s.DB.Finish(ctx, l.LoginHash, store.Complete, "", subject, sess.ID, store.Hash(code)); err != nil {
		s.log().Error("finish login", "err", err)
		render(w, http.StatusServiceUnavailable, view{Title: "Sign in failed", Text: "Try again."})
		return
	}
	s.log().Info("login complete", "subject", subject, "user_id", v.UserID)
	if l.ReturnURL != "" {
		// Same tab: the client page polls with its login_id and the code.
		u, _ := url.Parse(l.ReturnURL)
		q := u.Query()
		q.Set("code", code)
		u.RawQuery = q.Encode()
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, u.String(), http.StatusSeeOther)
		return
	}
	render(w, http.StatusOK, view{Title: "Signed in as " + firstNonEmpty(sess.Name, subject),
		Text: "Copy this code and paste it into the program that started the sign in. Do not give it to anyone else. The program shows the same last 6 characters.", Code: code})
}

var userIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// subject admits a verified login and names it user:<user_id>@<domain>.
// The LinkKeys user ID is stable. A handle can change or repeat, so it is
// only a display name.
func (s *Service) subject(v linkkeys.Verified) (string, error) {
	d := strings.ToLower(v.Domain)
	if !s.Domains[d] {
		return "", errors.New("Identity domain " + d + " is not admitted.")
	}
	if !userIDRE.MatchString(v.UserID) {
		return "", errors.New("The LinkKeys user ID is not valid.")
	}
	return "user:" + strings.ToLower(v.UserID) + "@" + d, nil
}

// displayName is the name to show for a login, or "" when there is none.
func displayName(v linkkeys.Verified) string {
	n := strings.TrimSpace(v.DisplayName)
	if n == "" {
		n = strings.TrimSpace(v.Handle)
	}
	if r := []rune(n); len(r) > 80 {
		n = string(r[:80])
	}
	return n
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
