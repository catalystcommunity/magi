package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	magiapi "github.com/catalystcommunity/magi/magi/api"
)

// Devices and notices (docs/attention.md). "watch" makes this program a
// desktop device of the person: it prints the notices, and with -play it
// says them.

const attentionUsage = `
  devices                    list your devices that use Magi
  notices [-open]            list your notices, newest first
  ack ID                     acknowledge a notice (it closes on all devices)
  watch [-name N] [-play]    be a desktop device: print notices as they
                             come; -play says them (aplay, paplay, or afplay)`

func (c *cli) attention(cmd string, args []string) (bool, error) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "devices":
		res, err := c.magi.ListDevices(c.ctx, magiapi.ListDevicesRequest{})
		if err != nil {
			return true, err
		}
		for _, d := range res.Devices {
			state := ""
			if d.Active {
				state = " in use"
			}
			if d.HandsFree {
				state += " hands-free"
			}
			fmt.Printf("%s  %-8s %-24s seen %s%s\n", d.Id, d.Kind, d.Name, time.UnixMilli(int64(d.SeenMs)).Format("Jan 2 15:04"), state)
		}
		return true, nil
	case "notices":
		open := fs.Bool("open", false, "only notices that are not acknowledged")
		fs.Parse(args)
		res, err := c.magi.ListNotices(c.ctx, magiapi.ListNoticesRequest{Open: open, Limit: ptr(uint64(30))})
		if err != nil {
			return true, err
		}
		for _, n := range res.Notices {
			printNotice(n)
		}
		return true, nil
	case "ack":
		if len(args) != 1 {
			return true, errors.New("ack ID")
		}
		_, err := c.magi.AckNotice(c.ctx, magiapi.AckNoticeRequest{Id: args[0]})
		return true, err
	case "watch":
		host, _ := os.Hostname()
		name := fs.String("name", "magi on "+host, "device name")
		play := fs.Bool("play", false, "say notices")
		fs.Parse(args)
		return true, c.watch(*name, *play)
	}
	return false, nil
}

func printNotice(n magiapi.Notice) {
	state := "open"
	if n.AckedMs != nil {
		state = "done"
	}
	fmt.Printf("%s  %s  %s  [%s, %s]\n  %s\n", n.Id, time.UnixMilli(int64(n.CreatedMs)).Format("Jan 2 15:04"), n.Title, n.Urgency, state,
		strings.ReplaceAll(strings.TrimSpace(n.Text), "\n", "\n  "))
}

// deviceID is the id of this program as a device, kept in the config
// directory.
func deviceID() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "magi", "device-id")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 8 {
		return strings.TrimSpace(string(b)), nil
	}
	b := make([]byte, 12)
	rand.Read(b)
	id := "cli-" + hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(p, []byte(id+"\n"), 0o600)
}

func player() []string {
	if runtime.GOOS == "darwin" {
		return []string{"afplay"}
	}
	for _, p := range [][]string{{"paplay"}, {"aplay", "-q"}} {
		if _, err := exec.LookPath(p[0]); err == nil {
			return p
		}
	}
	return nil
}

func (c *cli) watch(name string, play bool) error {
	id, err := deviceID()
	if err != nil {
		return err
	}
	var cmd []string
	can := []string{"show"}
	if play {
		if cmd = player(); cmd == nil {
			return errors.New("-play needs aplay, paplay, or afplay")
		}
		can = append(can, "speak")
	}
	hello := func() error {
		_, err := c.magi.PutDevice(c.ctx, magiapi.PutDeviceRequest{Id: id, Kind: "desktop", Name: name, Can: can, Active: ptr(true)})
		return err
	}
	if err := hello(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "watching as %s (%s)\n", name, id)
	last, closed := time.Now(), uint64(0)
	for {
		if time.Since(last) > time.Minute {
			hello()
			last = time.Now()
		}
		r, err := c.magi.WaitNotices(c.ctx, magiapi.WaitNoticesRequest{Device: id, WaitMs: ptr(uint64(25000)), ClosedAfter: &closed})
		if err != nil {
			fmt.Fprintln(os.Stderr, "magi:", err)
			time.Sleep(5 * time.Second)
			hello()
			continue
		}
		closed = r.NowMs
		for _, cid := range r.Closed {
			fmt.Printf("(closed %s)\n", cid)
		}
		for _, n := range r.Notices {
			fmt.Print("\a")
			printNotice(n)
			how := "shown"
			if play && n.Urgency != "low" && c.playMessage(cmd, n.Message) == nil {
				how = "spoken"
			}
			c.magi.DeliverNotice(c.ctx, magiapi.DeliverNoticeRequest{Id: n.Id, Device: id, How: how})
		}
	}
}

// playMessage says the parts of a message as they come.
func (c *cli) playMessage(cmd []string, message string) error {
	after := uint64(0)
	for {
		r, err := c.magi.Listen(c.ctx, magiapi.ListenRequest{Message: message, After: &after, WaitMs: ptr(uint64(20000))})
		if err != nil {
			return err
		}
		for _, p := range r.Parts {
			a, err := c.magi.GetAudio(c.ctx, magiapi.GetAudioRequest{Digest: p.Audio})
			if err != nil {
				return err
			}
			f, err := os.CreateTemp("", "magi-*.wav")
			if err != nil {
				return err
			}
			f.Write(a.Body)
			f.Close()
			err = exec.Command(cmd[0], append(cmd[1:], f.Name())...).Run()
			os.Remove(f.Name())
			if err != nil {
				return err
			}
		}
		after += uint64(len(r.Parts))
		if r.Done {
			if after == 0 {
				return errors.New("no audio")
			}
			return nil
		}
	}
}

// personaID accepts a short persona name: "engineering" is
// "persona:engineering".
func personaID(p string) string {
	if p != "" && !strings.Contains(p, ":") {
		return "persona:" + p
	}
	return p
}
