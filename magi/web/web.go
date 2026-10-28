// Package web serves a small browser client. It is one replaceable client
// of the Magi API; it has no privileges that other clients do not have.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

//go:embed static
var static embed.FS

// Handler serves index.html at "/" and the other files under
// "/s/<content hash>/". A new build has new file URLs, so caches (also
// caches between the browser and Magi) never mix files of two builds.
func Handler() http.Handler {
	sub, _ := fs.Sub(static, "static")
	prefix := "/s/" + contentHash(sub) + "/"
	index, _ := fs.ReadFile(sub, "index.html")
	page := strings.NewReplacer(`href="style.css"`, `href="`+prefix+`style.css"`,
		`src="js/`, `src="`+prefix+`js/`).Replace(string(index))
	files := http.StripPrefix(prefix, http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, prefix):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(page))
		default:
			http.NotFound(w, r)
		}
	})
}

func contentHash(fsys fs.FS) string {
	var names []string
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, _ := fs.ReadFile(fsys, n)
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
