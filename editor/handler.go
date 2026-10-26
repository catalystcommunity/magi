// Package workfloweditor provides a local authoring tool with no execution API.
package workfloweditor

import (
	"embed"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	w "github.com/catalystcommunity/magi/generated/go"
	p "github.com/catalystcommunity/magi/protocol"
	"github.com/catalystcommunity/magi/workflow"
)

//go:embed index.html editor.css editor.mjs model.mjs
var assets embed.FS

// NewHandler is only for literal-loopback development. It stores no submitted
// document and provides no credentials, service proxy, or external operations.
func NewHandler(origin, codecDir string) (http.Handler, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, p.ErrWire
	}
	files := map[string][]byte{}
	types := map[string]string{}
	for _, name := range []string{"index.html", "editor.css", "editor.mjs", "model.mjs"} {
		b, e := assets.ReadFile(name)
		if e != nil {
			return nil, e
		}
		path := "/" + name
		if name == "index.html" {
			path = "/"
		}
		files[path] = b
	}
	for _, name := range []string{"codec.gen.js", "types.gen.js"} {
		b, e := os.ReadFile(filepath.Join(codecDir, name))
		if e != nil {
			return nil, e
		}
		files["/"+name] = b
	}
	types["/"] = "text/html; charset=utf-8"
	types["/editor.css"] = "text/css; charset=utf-8"
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		if r.Host != u.Host {
			rw.WriteHeader(421)
			return
		}
		if r.URL.RawQuery != "" {
			rw.WriteHeader(400)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != origin {
			rw.WriteHeader(403)
			return
		}
		if r.Method == "GET" {
			b, ok := files[r.URL.Path]
			if !ok {
				rw.WriteHeader(404)
				return
			}
			typ := types[r.URL.Path]
			if typ == "" {
				typ = "text/javascript; charset=utf-8"
			}
			rw.Header().Set("Content-Type", typ)
			rw.Write(b)
			return
		}
		if r.Method != "POST" {
			rw.WriteHeader(405)
			return
		}
		if r.Header.Get("Content-Type") != "application/cbor" || r.Header.Get("Content-Encoding") != "" {
			rw.WriteHeader(415)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(rw, r.Body, workflow.MaxDefinitionBytes))
		if e != nil {
			rw.WriteHeader(413)
			return
		}
		var result []byte
		switch r.URL.Path {
		case "/validate":
			d, e := p.Decode(raw, w.DecodeWorkflowDefinition, w.EncodeWorkflowDefinition)
			if e != nil {
				rw.WriteHeader(400)
				return
			}
			result = w.EncodeWorkflowValidation(workflow.ValidateDefinition(d))
		case "/check-draft":
			d, e := p.Decode(raw, w.DecodeWorkflowDraft, w.EncodeWorkflowDraft)
			if e != nil || len(d.Positions) > 16 || len(d.Definition.Steps) > 16 {
				rw.WriteHeader(400)
				return
			}
			for _, pos := range d.Positions {
				if pos.X < 0 || pos.X > 690 || pos.Y < 0 || pos.Y > 550 {
					rw.WriteHeader(400)
					return
				}
			}
			result = w.EncodeWorkflowDraft(d)
		default:
			rw.WriteHeader(404)
			return
		}
		rw.Header().Set("Content-Type", "application/cbor")
		rw.Write(result)
	}), nil
}
