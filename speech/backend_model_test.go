//go:build speechmodel

package speech

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRealSpeechBackends(t *testing.T) {
	config := func(kind string) PythonConfig {
		prefix := "OPAL_SPEECH_TEST_" + strings.ToUpper(kind)
		c := PythonConfig{Kind: kind, Python: os.Getenv(prefix + "_PYTHON"), ModelDir: os.Getenv(prefix + "_MODEL")}
		if c.Python == "" || c.ModelDir == "" {
			t.Fatal("Real speech tests require pinned model and Python paths; no fixture fallback")
		}
		return c
	}
	tts := &PythonBackend{Config: config("tts")}
	defer tts.Close()
	stt := &PythonBackend{Config: config("stt")}
	defer stt.Close()
	for _, voice := range Voices {
		t.Run(voice, func(t *testing.T) {
			v := request("real-" + voice)
			v.Text = ptr("The answer is four.")
			v.ProfileId = ptr("kokoro-v1/" + voice)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			started := time.Now()
			audio, e := tts.Process(ctx, Work{Request: v})
			must(t, e)
			if len(audio.PCM) < 1000 || len(audio.PCM) > MaxOutputBytes || audio.SampleRate != 24000 {
				t.Fatal("invalid real audio")
			}
			hash := sha256.Sum256(audio.PCM)
			t.Logf("voice=%s generation_ms=%d bytes=%d sha256=%x", voice, time.Since(started).Milliseconds(), len(audio.PCM), hash)
			if voice != "af_heart" {
				return
			}
			// Fixture resampling only. Clients must supply 16 kHz PCM to the API.
			pcm := make([]byte, (len(audio.PCM)/2)*2/3*2)
			for i := 0; i < len(pcm)/2; i++ {
				source := float64(i) * 1.5
				j := int(source)
				a := int16(binary.LittleEndian.Uint16(audio.PCM[j*2:]))
				b := a
				if j+1 < len(audio.PCM)/2 {
					b = int16(binary.LittleEndian.Uint16(audio.PCM[(j+1)*2:]))
				}
				value := float64(a) + (float64(b)-float64(a))*(source-float64(j))
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(value)))
			}
			started = time.Now()
			result, e := stt.Process(ctx, Work{Input: pcm})
			must(t, e)
			text := strings.ToLower(result.Transcript)
			if !strings.Contains(text, "four") && !strings.Contains(text, "4") {
				t.Fatal(fmt.Sprintf("Unexpected fixture transcript: %q", text))
			}
			t.Logf("transcription_ms=%d transcript=%q", time.Since(started).Milliseconds(), result.Transcript)
		})
	}
}
