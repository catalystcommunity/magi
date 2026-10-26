package speech

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

//go:embed model_worker.py
var modelSource string

//go:embed model-pins.json
var modelPins json.RawMessage

type PythonConfig struct {
	Python   string `json:"python"`
	ModelDir string `json:"model_dir"`
	Kind     string `json:"kind"`
	SSHHost  string `json:"ssh_host,omitempty"`
}
type PythonBackend struct {
	Config PythonConfig
	mu     sync.Mutex
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
	source string // Test fixture override; never populated from configuration.
}

func (b *PythonBackend) stop() {
	if b.cmd != nil {
		b.input.Close()
		b.cmd.Process.Kill()
		b.cmd.Wait()
		b.cmd = nil
	}
}
func (b *PythonBackend) Close() { b.mu.Lock(); defer b.mu.Unlock(); b.stop() }

func remotePythonCommand(python, source string) string {
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	return "exec env -i PATH=/usr/bin:/bin LANG=C.UTF-8 HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 PYTHONDONTWRITEBYTECODE=1 TOKENIZERS_PARALLELISM=false " + quote(python) + " -I -u -c " + quote(source)
}

func (b *PythonBackend) start() error {
	if !filepath.IsAbs(b.Config.Python) || !filepath.IsAbs(b.Config.ModelDir) || (b.Config.Kind != "tts" && b.Config.Kind != "stt") {
		return ErrInvalid
	}
	source := b.source
	if source == "" {
		source = modelSource
	}
	cmd := exec.Command(b.Config.Python, "-I", "-u", "-c", source)
	if b.Config.SSHHost != "" {
		if !regexp.MustCompile(`^[a-z_][a-z0-9_-]*@[a-z0-9][a-z0-9.-]*$`).MatchString(b.Config.SSHHost) {
			return ErrInvalid
		}
		remote := remotePythonCommand(b.Config.Python, source)
		cmd = exec.Command("/usr/bin/ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "StrictHostKeyChecking=yes", b.Config.SSHHost, remote)
	}
	// No RP, database, provider, proxy, or workstation credentials enter Python.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "PYTHONDONTWRITEBYTECODE=1", "TOKENIZERS_PARALLELISM=false"}
	cmd.Stderr = io.Discard
	input, e := cmd.StdinPipe()
	if e != nil {
		return e
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		input.Close()
		return e
	}
	if e = cmd.Start(); e != nil {
		input.Close()
		output.Close()
		return e
	}
	b.cmd = cmd
	b.input = input
	b.output = bufio.NewScanner(output)
	b.output.Buffer(make([]byte, 65536), 8000000)
	return nil
}
func (b *PythonBackend) Process(ctx context.Context, work Work) (Result, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	fresh := b.cmd == nil
	if fresh {
		if e := b.start(); e != nil {
			return Result{}, e
		}
	}
	finished := make(chan struct{})
	type answer struct {
		result Result
		err    error
	}
	returned := make(chan answer, 1)
	go func() {
		defer close(finished)
		if fresh {
			config := struct {
				Kind     string          `json:"kind"`
				ModelDir string          `json:"model_dir"`
				Pins     json.RawMessage `json:"pins"`
			}{b.Config.Kind, b.Config.ModelDir, modelPins}
			if e := json.NewEncoder(b.input).Encode(config); e != nil {
				returned <- answer{err: e}
				return
			}
			var ready struct {
				Ready bool `json:"ready"`
			}
			if !b.output.Scan() || json.Unmarshal(b.output.Bytes(), &ready) != nil || !ready.Ready {
				returned <- answer{err: ErrUnavailable}
				return
			}
		}
		request := struct {
			Text  string `json:"text"`
			Voice string `json:"voice"`
			PCM   []byte `json:"pcm"`
		}{PCM: work.Input}
		if work.Request.Text != nil {
			request.Text = *work.Request.Text
		}
		if work.Request.ProfileId != nil {
			v, ok := profile(*work.Request.ProfileId)
			if !ok {
				returned <- answer{err: ErrInvalid}
				return
			}
			request.Voice = v.Voice
		}
		var out struct {
			PCM        []byte `json:"pcm"`
			Transcript string `json:"transcript"`
			SampleRate uint64 `json:"sample_rate"`
			Error      string `json:"error_code"`
		}
		e := json.NewEncoder(b.input).Encode(request)
		if e == nil {
			if !b.output.Scan() {
				e = ErrUnavailable
			} else {
				e = json.Unmarshal(b.output.Bytes(), &out)
			}
		}
		if out.Error != "" {
			e = ErrUnavailable
		}
		returned <- answer{Result{PCM: out.PCM, Transcript: out.Transcript, SampleRate: out.SampleRate}, e}
	}()
	select {
	case value := <-returned:
		<-finished
		if value.err != nil {
			b.stop()
		}
		return value.result, value.err
	case <-ctx.Done():
		b.cmd.Process.Kill()
		<-finished
		b.stop()
		return Result{}, ctx.Err()
	}
}
