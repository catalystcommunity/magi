package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
)

// applyVariant changes a request for a model name of Engine.Models: the
// model becomes the engine model, and the request gets the adapters of the
// variant. It returns the variant name, or "" for the base model.
func (h *Host) applyVariant(r *http.Request) (string, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	r.Body.Close()
	name, out, err := h.variantBody(b)
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(out))
	r.ContentLength = int64(len(out))
	r.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return name, nil
}

// variantBody is applyVariant on a request body.
func (h *Host) variantBody(b []byte) (string, []byte, error) {
	var req map[string]json.RawMessage
	if json.Unmarshal(b, &req) != nil {
		return "", b, nil // not JSON: the engine answers
	}
	var model string
	json.Unmarshal(req["model"], &model)
	v, ok := h.cfg.Engine.Models[model]
	if !ok {
		if _, has := req["lora"]; has {
			return "", nil, fmt.Errorf("use a model name for an adapter, not a lora field")
		}
		return "", b, nil
	}
	req["model"], _ = json.Marshal(h.cfg.Engine.Model)
	req["lora"], _ = json.Marshal(v.Lora)
	out, err := json.Marshal(req)
	return model, out, err
}

// writeModels answers GET /v1/models with the base model and the variants.
func (h *Host) writeModels(w http.ResponseWriter) {
	type entry struct {
		Id          string `json:"id"`
		Object      string `json:"object"`
		OwnedBy     string `json:"owned_by"`
		Description string `json:"description,omitempty"`
	}
	list := []entry{{Id: h.cfg.Engine.Model, Object: "model", OwnedBy: "magi-inference", Description: h.cfg.Engine.Description}}
	names := make([]string, 0, len(h.cfg.Engine.Models))
	for n := range h.cfg.Engine.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		list = append(list, entry{Id: n, Object: "model", OwnedBy: "magi-inference", Description: h.cfg.Engine.Models[n].Description})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": list})
}

// renameWriter changes the model name in a JSON answer of the engine. A
// stream (text/event-stream) passes unchanged.
type renameWriter struct {
	http.ResponseWriter
	from, to string
	buf      bytes.Buffer
	stream   bool
	decided  bool
	status   int
}

func (w *renameWriter) WriteHeader(code int) {
	w.status = code
	if w.Header().Get("Content-Type") == "text/event-stream" || code != 200 {
		w.stream = true
		w.decided = true
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *renameWriter) Write(p []byte) (int, error) {
	if !w.decided {
		w.decided = true
		if w.Header().Get("Content-Type") == "text/event-stream" {
			w.stream = true
		}
	}
	if w.stream {
		return w.ResponseWriter.Write(p)
	}
	return w.buf.Write(p)
}

func (w *renameWriter) Flush() {
	if w.stream {
		if f, ok := w.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// finish writes a buffered answer with the new model name.
func (w *renameWriter) finish() {
	if w.stream {
		return
	}
	b := w.buf.Bytes()
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) == nil {
		if _, ok := m["model"]; ok {
			m["model"], _ = json.Marshal(w.to)
			if nb, err := json.Marshal(m); err == nil {
				b = nb
			}
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	if w.status == 0 {
		w.status = 200
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.ResponseWriter.Write(b)
}
