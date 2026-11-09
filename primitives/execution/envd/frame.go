package envd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The terminal stream (POST /v1/pty, after the upgrade) is a sequence of
// frames in the two directions. A frame is one type byte, a 4-byte
// big-endian payload length, and the payload:
//
//	+------+----------------+-------------+
//	| type | length (u32 BE) | payload ... |
//	+------+----------------+-------------+
//
// Types:
//
//	'd' data    client to envd: input bytes; envd to client: output bytes
//	'r' resize  client to envd: cols (u16 BE), rows (u16 BE)
//	'x' exit    envd to client: the exit status (i32 BE); the last frame
//	'e' error   to the client: UTF-8 text; the last frame
//	'k' ping    either direction: no payload; the receiver ignores it
//
// A receiver ignores frames of other types. A payload is at most MaxFrame
// bytes. Magi sends the same frames to a browser as WebSocket binary
// messages: the type byte and the payload, with no length.
const (
	FrameData   byte = 'd'
	FrameResize byte = 'r'
	FrameExit   byte = 'x'
	FrameError  byte = 'e'
	FramePing   byte = 'k'
)

// MaxFrame is the largest frame payload.
const MaxFrame = 64 << 10

// ErrFrameTooLarge is returned for a frame with a payload above MaxFrame.
var ErrFrameTooLarge = errors.New("envd: terminal frame larger than 64 KiB")

// WriteFrame writes one frame with a single Write call, so frames of two
// writers that use a lock do not mix.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return ErrFrameTooLarge
	}
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], uint32(len(payload)))
	copy(b[5:], payload)
	_, err := w.Write(b)
	return err
}

// ReadFrame reads one frame. The payload is a new slice.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:5])
	if n > MaxFrame {
		return 0, nil, ErrFrameTooLarge
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return h[0], p, nil
}

// ResizePayload is the payload of a resize frame.
func ResizePayload(cols, rows uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:2], cols)
	binary.BigEndian.PutUint16(b[2:4], rows)
	return b
}

// ParseResize reads a resize payload.
func ParseResize(p []byte) (cols, rows uint16, err error) {
	if len(p) != 4 {
		return 0, 0, fmt.Errorf("envd: resize payload of %d bytes, expected 4", len(p))
	}
	return binary.BigEndian.Uint16(p[0:2]), binary.BigEndian.Uint16(p[2:4]), nil
}

// ExitPayload is the payload of an exit frame.
func ExitPayload(code int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(code))
	return b
}

// ParseExit reads an exit payload.
func ParseExit(p []byte) (int32, error) {
	if len(p) != 4 {
		return 0, fmt.Errorf("envd: exit payload of %d bytes, expected 4", len(p))
	}
	return int32(binary.BigEndian.Uint32(p)), nil
}
