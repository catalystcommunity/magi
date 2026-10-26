package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/catalystcommunity/magi/config"
	w "github.com/catalystcommunity/magi/generated/go"
	"github.com/catalystcommunity/magi/question"
	"github.com/catalystcommunity/magi/speech"
	"io"
	"log"
	"os"
	"time"
)

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("select voices, tts, stt, read, cancel, audio, or evidence")
	}
	op := os.Args[1]
	flags := flag.NewFlagSet(op, flag.ContinueOnError)
	profile := flags.String("profile", config.ProfilePath("speech", "client.json"), "Private client profile")
	id := flags.String("id", "", "Unique request ID")
	voice := flags.String("voice", "af_heart", "Kokoro stock voice ID")
	output := flags.String("output", "", "New WAV output file; existing files are never overwritten")
	if e := flags.Parse(os.Args[2:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return speech.ErrInvalid
	}
	settings, e := speech.LoadProfile(*profile)
	if e != nil {
		return e
	}
	client, e := speech.NewClient(settings)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if op == "voices" {
		v, e := client.Capabilities(ctx)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	if *id == "" {
		return fmt.Errorf("-id is required; keep it to inspect an uncertain result")
	}
	ref := w.SpeechRef{Owner: settings.Owner, RequestId: *id, Generation: 1}
	switch op {
	case "read":
		v, e := client.Read(ctx, ref)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	case "cancel":
		v, e := client.Cancel(ctx, ref)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	case "evidence":
		v, e := client.Evidence(ctx, ref)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	case "tts", "audio":
		if *output == "" {
			return fmt.Errorf("-output is required for audio")
		}
		if _, e = os.Lstat(*output); !os.IsNotExist(e) {
			return fmt.Errorf("output path already exists or cannot be checked")
		}
	case "stt":
	default:
		return speech.ErrInvalid
	}
	if op != "audio" {
		limit := int64(speech.MaxText + 1)
		if op == "stt" {
			limit = speech.MaxInputSamples*2 + 4097
		}
		raw, e := io.ReadAll(io.LimitReader(os.Stdin, limit))
		if e != nil {
			return e
		}
		v := w.SpeechSubmit{RequestId: *id, Owner: settings.Owner, Kind: w.SpeechKind(op), Generation: 1, Deadline: time.Now().UTC().Truncate(time.Millisecond).Add(2*time.Minute - time.Second), RetentionSeconds: 3600, Sources: []w.RecordRef{}}
		var pcm []byte
		if op == "tts" {
			text := string(raw)
			v.Text = &text
			profile := "kokoro-v1/" + *voice
			v.ProfileId = &profile
		} else {
			pcm, e = speech.ParseWAV(raw)
			if e != nil {
				return fmt.Errorf("input must be mono 16 kHz signed 16-bit PCM WAV, at most 60 seconds")
			}
			samples := uint64(len(pcm) / 2)
			v.InputSamples = &samples
			hash := sha256.Sum256(pcm)
			digest := hash[:]
			v.InputDigest = &digest
		}
		fmt.Fprintln(os.Stderr, "Speech request:", *id)
		if _, e = client.Submit(ctx, v); e != nil {
			return e
		}
		if op == "stt" {
			if e = client.Upload(ctx, ref, pcm); e != nil {
				return e
			}
		}
	}
	status, e := client.Wait(ctx, ref)
	if e != nil {
		return e
	}
	if op == "stt" {
		fmt.Println(question.TerminalText(*status.Transcript))
		return nil
	}
	pcm, e := client.Audio(ctx, status)
	if e != nil {
		return e
	}
	wav, e := speech.WAV(pcm, uint32(status.OutputSampleRate))
	if e != nil {
		return e
	}
	f, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(wav)
	if e == nil {
		e = f.Sync()
	}
	closed := f.Close()
	if e != nil {
		return e
	}
	return closed
}
func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
