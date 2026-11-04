package hostsetup

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Device is one device that llama-server --list-devices shows.
type Device struct {
	Name, Desc string
	Mem, Free  int64 // bytes
}

var deviceRow = regexp.MustCompile(`^\s*([A-Za-z]+\d+):\s*(.*?)\s*\((\d+) MiB, (\d+) MiB free\)\s*$`)

// ParseDevices reads the output of llama-server --list-devices.
func ParseDevices(out string) []Device {
	var ds []Device
	for _, l := range strings.Split(out, "\n") {
		if m := deviceRow.FindStringSubmatch(l); m != nil {
			mem, _ := strconv.ParseInt(m[3], 10, 64)
			free, _ := strconv.ParseInt(m[4], 10, 64)
			ds = append(ds, Device{Name: m[1], Desc: m[2], Mem: mem << 20, Free: free << 20})
		}
	}
	return ds
}

func nameTokens(s string) map[string]bool {
	t := map[string]bool{}
	for _, w := range regexp.MustCompile(`[a-z0-9]+`).FindAllString(strings.ToLower(s), -1) {
		if len(w) >= 2 && w != "amd" && w != "nvidia" && w != "radeon" && w != "geforce" && w != "corporation" && w != "inc" && w != "ati" {
			t[w] = true
		}
	}
	return t
}

// PickDevice returns the device of GPU g: the device whose name has the
// most words of the GPU name, then the device whose memory is nearest to
// the memory of g. An integrated GPU can show the system RAM as its
// memory, so a model must not go to the device with the most memory.
// It returns "" when there is no device.
func PickDevice(ds []Device, g GPU) string {
	want := nameTokens(g.Name)
	best, bestScore := -1, -1
	var bestDiff int64
	for i, d := range ds {
		score := 0
		for w := range nameTokens(d.Desc) {
			if want[w] {
				score++
			}
		}
		diff := d.Mem - g.Mem
		if diff < 0 {
			diff = -diff
		}
		if score > bestScore || score == bestScore && diff < bestDiff {
			best, bestScore, bestDiff = i, score, diff
		}
	}
	if best < 0 {
		return ""
	}
	return ds[best].Name
}

// device returns the llama.cpp device of a GPU engine, or "" when it is
// not known (then llama.cpp selects).
func (l *Local) device(e Engine) string {
	if !e.GPU() || l.GPUOf == nil {
		return ""
	}
	g, ok := l.GPUOf(e.Name)
	if !ok {
		return ""
	}
	if d, ok := l.devices[e.Name]; ok {
		return d
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, filepath.Join(l.engineDir(e), "llama-server"), "--list-devices")
	c.Env = l.engineEnv(e)
	out, _ := c.CombinedOutput()
	d := PickDevice(ParseDevices(string(out)), g)
	if l.devices == nil {
		l.devices = map[string]string{}
	}
	l.devices[e.Name] = d
	if d != "" {
		l.say("(%s: GPU device %s)\n", e.Name, d)
	}
	return d
}
