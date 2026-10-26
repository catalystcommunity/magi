package speech

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestAudioContainerValidation(t *testing.T) {
	pcm := []byte{0, 1, 2, 3, 4, 5}
	wav, e := WAV(pcm, 16000)
	if e != nil {
		t.Fatal(e)
	}
	out, e := ParseWAV(wav)
	if e != nil || !bytes.Equal(out, pcm) {
		t.Fatal("round trip", e)
	}
	for _, change := range []func([]byte) []byte{func(b []byte) []byte { return b[:len(b)-1] }, func(b []byte) []byte { b[0] = 'X'; return b }, func(b []byte) []byte { binary.LittleEndian.PutUint32(b[24:], 24000); return b }, func(b []byte) []byte { binary.LittleEndian.PutUint16(b[22:], 2); return b }, func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:], 0xffffffff); return b }, func(b []byte) []byte { binary.LittleEndian.PutUint16(b[20:], 3); return b }} {
		if _, e = ParseWAV(change(bytes.Clone(wav))); e == nil {
			t.Fatal("accepted malformed audio")
		}
	}
	if _, e = WAV(pcm, 44100); e == nil {
		t.Fatal("unsupported rate")
	}
	if _, e = WAV(pcm[:5], 16000); e == nil {
		t.Fatal("odd PCM bytes")
	}
}
