package hostsetup

import (
	_ "embed"
	"encoding/json"
	"path"
)

//go:embed pins.json
var pinsJSON []byte

// File is one pinned download.
type File struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Engine is a pinned engine build for one CPU architecture.
type Engine struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // cpu, cuda, rocm, vulkan
	Arch    string `json:"arch"`
	Version string `json:"version"`
	License string `json:"license"`
	Files   []File `json:"files"`
}

// Size is the download size of all files.
func (e Engine) Size() int64 {
	var n int64
	for _, f := range e.Files {
		n += f.Size
	}
	return n
}

// GPU tells if the engine runs the model on a GPU.
func (e Engine) GPU() bool { return e.Kind != "cpu" }

// Model is a pinned model file.
type Model struct {
	Name    string `json:"name"`
	License string `json:"license"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// FileName is the name of the model file on disk.
func (m Model) FileName() string { return path.Base(m.URL) }

// SpeechItem is an engine or model of the speech role. It has no download
// in this version.
type SpeechItem struct {
	Name  string `json:"name"`
	Needs string `json:"needs"`
	Use   string `json:"use"`
	Size  int64  `json:"size"`
}

// Pins is the content of pins.json.
type Pins struct {
	Helper  []string `json:"helper"`
	Engines []Engine `json:"engines"`
	Models  []Model  `json:"models"`
	Speech  struct {
		Engines []SpeechItem `json:"engines"`
		Models  []SpeechItem `json:"models"`
	} `json:"speech"`
}

// LoadPins returns the pins that are built into the program.
func LoadPins() (*Pins, error) {
	var p Pins
	if err := json.Unmarshal(pinsJSON, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Model returns the model with the name.
func (p *Pins) Model(name string) (Model, bool) {
	for _, m := range p.Models {
		if m.Name == name {
			return m, true
		}
	}
	return Model{}, false
}

// Engine returns the engine with the name for arch.
func (p *Pins) Engine(name, arch string) (Engine, bool) {
	for _, e := range p.Engines {
		if e.Name == name && e.Arch == arch {
			return e, true
		}
	}
	return Engine{}, false
}
