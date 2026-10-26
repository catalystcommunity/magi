// Package protocol applies Magi's bounded wire profile before generated codecs.
package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"regexp"
	"time"
	"unicode/utf8"
)

const MaxFrame = 256 * 1024
const MaxBlob = 128 * 1024
const MaxText = 16 * 1024
const MaxCollection = 256
const MaxDepth = 24
const MaxNodes = 8192
const MaxInteger = 9007199254740991
const Timeout = 10 * time.Second

var ErrWire = errors.New("invalid or unsupported wire value")
var timestampPattern = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,3})?Z$`)

type tag struct {
	number uint64
	value  any
}
type scanner struct {
	b          []byte
	pos, nodes int
	integer64  bool
}

func (s *scanner) take(n uint64) ([]byte, error) {
	if n > uint64(len(s.b)-s.pos) {
		return nil, ErrWire
	}
	start := s.pos
	s.pos += int(n)
	return s.b[start:s.pos], nil
}
func (s *scanner) item(depth int, decimal bool) (any, error) {
	s.nodes++
	if depth > MaxDepth || s.nodes > MaxNodes {
		return nil, ErrWire
	}
	p, err := s.take(1)
	if err != nil {
		return nil, err
	}
	major, small := p[0]>>5, p[0]&31
	n := uint64(small)
	if small >= 24 {
		var size uint64
		switch small {
		case 24:
			size = 1
		case 25:
			size = 2
		case 26:
			size = 4
		case 27:
			size = 8
		default:
			return nil, ErrWire
		}
		p, err = s.take(size)
		if err != nil {
			return nil, err
		}
		switch size {
		case 1:
			n = uint64(p[0])
		case 2:
			n = uint64(binary.BigEndian.Uint16(p))
		case 4:
			n = uint64(binary.BigEndian.Uint32(p))
		case 8:
			n = binary.BigEndian.Uint64(p)
		}
		if major != 7 && (n < 24 || size > 1 && n <= 255 || size > 2 && n <= 65535 || size > 4 && n <= math.MaxUint32) {
			return nil, ErrWire
		}
	}
	switch major {
	case 0:
		if !decimal && !s.integer64 && n > MaxInteger {
			return nil, ErrWire
		}
		return n, nil
	case 1:
		if n > math.MaxInt64 || !decimal && !s.integer64 && n >= MaxInteger {
			return nil, ErrWire
		}
		return -1 - int64(n), nil
	case 2, 3:
		limit := uint64(MaxBlob)
		if major == 3 {
			limit = MaxText
		}
		if n > limit {
			return nil, ErrWire
		}
		p, err := s.take(n)
		if err != nil {
			return nil, err
		}
		if major == 2 {
			return p, nil
		}
		if !utf8.Valid(p) {
			return nil, ErrWire
		}
		return string(p), nil
	case 4:
		if n > MaxCollection {
			return nil, ErrWire
		}
		a := make([]any, 0, int(n))
		for range n {
			v, e := s.item(depth+1, decimal)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		return a, nil
	case 5:
		if n > MaxCollection {
			return nil, ErrWire
		}
		m := make(map[string]any, int(n))
		for range n {
			k, e := s.item(depth+1, false)
			if e != nil {
				return nil, e
			}
			key, ok := k.(string)
			if !ok {
				return nil, ErrWire
			}
			if _, ok = m[key]; ok {
				return nil, ErrWire
			}
			v, e := s.item(depth+1, false)
			if e != nil {
				return nil, e
			}
			m[key] = v
		}
		return m, nil
	case 6:
		if n != 0 && n != 2 && n != 3 && n != 4 && n != 24 {
			return nil, ErrWire
		}
		v, e := s.item(depth+1, n == 4)
		if e != nil {
			return nil, e
		}
		switch n {
		case 0:
			text, ok := v.(string)
			if !ok || !timestampPattern.MatchString(text) {
				return nil, ErrWire
			}
			t, e := time.Parse(time.RFC3339Nano, text)
			if e != nil {
				return nil, ErrWire
			}
			return tag{0, t}, nil
		case 2, 3:
			b, ok := v.([]byte)
			if !ok || len(b) == 0 || len(b) > 64 || b[0] == 0 {
				return nil, ErrWire
			}
		case 4:
			a, ok := v.([]any)
			if !ok || len(a) != 2 {
				return nil, ErrWire
			}
			switch x := a[0].(type) {
			case int64:
				if x < -128 {
					return nil, ErrWire
				}
			case uint64:
				if x > 128 {
					return nil, ErrWire
				}
			default:
				return nil, ErrWire
			}
			switch x := a[1].(type) {
			case int64, uint64:
			case tag:
				if x.number != 2 && x.number != 3 {
					return nil, ErrWire
				}
			default:
				return nil, ErrWire
			}
		case 24:
			if _, ok := v.([]byte); !ok {
				return nil, ErrWire
			}
		}
		return tag{n, v}, nil
	case 7:
		switch small {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22:
			return nil, nil
		}
	}
	return nil, ErrWire
}

func parse(b []byte) (any, error) {
	if len(b) == 0 || len(b) > MaxFrame {
		return nil, ErrWire
	}
	s := scanner{b: b}
	v, e := s.item(0, false)
	if e != nil || s.pos != len(b) {
		return nil, ErrWire
	}
	return v, nil
}

// CheckFrame rejects malformed, duplicate, indefinite, deep, or large values.
// Opaque byte payloads need their own typed validation at the consumer boundary.
func CheckFrame(b []byte) error { _, err := parse(b); return err }

// CheckInteger64Frame is for foreign Go protocols with exact integer64 fields,
// such as Corndogs nanosecond timestamps. It does not change Magi's wire profile
// or permit these values in JavaScript-number clients.
func CheckInteger64Frame(b []byte) error {
	if len(b) == 0 || len(b) > MaxFrame {
		return ErrWire
	}
	s := scanner{b: b, integer64: true}
	if _, e := s.item(0, false); e != nil || s.pos != len(b) {
		return ErrWire
	}
	return nil
}

// Decode keeps generated permissive decoding behind exact known-field checks.
// It compares values, not reconstructed bytes. Timestamp spelling may differ.
func Decode[T any](b []byte, decode func([]byte) (T, error), encode func(T) []byte) (T, error) {
	var zero T
	before, err := parse(b)
	if err != nil {
		return zero, ErrWire
	}
	v, err := decode(b)
	if err != nil {
		return zero, ErrWire
	}
	after, err := parse(encode(v))
	if err != nil || !reflect.DeepEqual(before, after) {
		return zero, ErrWire
	}
	return v, nil
}

func SameBytes(a, b []byte) bool { return bytes.Equal(a, b) }
