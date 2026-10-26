// Package harness supervises the Python proposal loop. It owns no task queue.
package harness

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

//go:embed loop.py
var source string

//go:embed formatting.py
var InitialSource string

const Revision = "opal-python-proposals-v1"

type Frame struct {
	Operation string          `json:"operation"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}
type Exchange func(context.Context, Frame) (json.RawMessage, error)

// Run never interprets a tool name as a host command. The caller must record
// admission before effects. Each model and tool exchange receives a stable ID.
func Run(ctx context.Context, python, task string, turns int, exchange Exchange) (json.RawMessage, error) {
	if !filepath.IsAbs(python) || len(task) == 0 || len(task) > 3000 || turns < 1 || turns > 6 || exchange == nil {
		return nil, errors.New("invalid harness configuration")
	}
	command := exec.CommandContext(ctx, python, "-I", "-B", "-c", source)
	command.Env = []string{"LANG=C.UTF-8"}
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	input, e := command.StdinPipe()
	if e != nil {
		return nil, e
	}
	output, e := command.StdoutPipe()
	if e != nil {
		input.Close()
		return nil, e
	}
	if e = command.Start(); e != nil {
		input.Close()
		return nil, e
	}
	finished := false
	defer func() {
		input.Close()
		if !finished {
			command.Process.Kill()
			command.Wait()
		}
	}()
	initial, _ := json.Marshal(map[string]any{"task": task, "turns": turns})
	if _, e = input.Write(append(initial, '\n')); e != nil {
		return nil, e
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 65537)
	models := 0
	toolAllowed := false
	for scanner.Scan() {
		var frame Frame
		dec := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		dec.DisallowUnknownFields()
		if dec.Decode(&frame) != nil || dec.Decode(new(any)) != io.EOF {
			return nil, errors.New("invalid harness frame")
		}
		if frame.Kind == "finish" && frame.Operation == "finish" && models > 0 {
			input.Close()
			if e = command.Wait(); e != nil {
				return nil, errors.New("harness exit failed")
			}
			finished = true
			return frame.Payload, nil
		}
		switch {
		case frame.Kind == "model" && models < turns && frame.Operation == "model-"+strconv.Itoa(models):
			models++
			toolAllowed = true
		case frame.Kind == "tool" && toolAllowed && frame.Operation == "tool-"+strconv.Itoa(models-1):
			toolAllowed = false
		default:
			return nil, errors.New("harness operation sequence")
		}
		result, e := exchange(ctx, frame)
		if e != nil {
			return nil, e
		}
		if !json.Valid(result) || len(result) > 60000 {
			return nil, errors.New("invalid exchange reply")
		}
		reply, _ := json.Marshal(struct {
			Operation string          `json:"operation"`
			Result    json.RawMessage `json:"result"`
		}{frame.Operation, result})
		if _, e = input.Write(append(reply, '\n')); e != nil {
			return nil, e
		}
	}
	return nil, errors.New("harness ended without a final record")
}
