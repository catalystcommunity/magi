package capture

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	wire "opal.local/experiments/knowledge/generated-capture/go"
)

// Spool is a private local outbox for an admitted capture component. It is not
// an upload API and does not authenticate caller-supplied attribution fields.
// The directory must have one trusted owner; hostile local writers are excluded.
type Spool struct {
	root       *os.Root
	checkpoint func(string)
}

const maxBatch = 1 << 20

func OpenSpool(path string) (*Spool, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		root.Close()
		return nil, ErrInvalid
	}
	return &Spool{root: root}, nil
}
func (s *Spool) Close() error { return s.root.Close() }
func batchName(attempt, stream string, sequence uint64) (string, error) {
	if !namePattern.MatchString(attempt) || !namePattern.MatchString(stream) || sequence == 0 {
		return "", ErrInvalid
	}
	key := fmt.Sprintf("%d:%s%d:%s/%d", len(attempt), attempt, len(stream), stream, sequence)
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]) + ".batch", nil
}

func bounds(segment wire.Segment) (uint64, uint64, error) {
	var start, end uint64
	switch v := segment.(type) {
	case wire.LiteralSegment:
		start, end = v.Start, v.Start+uint64(len(v.Value))
		if len(v.Value) == 0 {
			return 0, 0, ErrInvalid
		}
	case wire.SecretSegment:
		start, end = v.Start, v.End
		if !validBinding(v.Candidate.Binding) {
			return 0, 0, ErrInvalid
		}
		if _, err := transform([]byte("x"), v.Candidate.Encoding); err != nil {
			return 0, 0, err
		}
	case wire.AmbiguousSegment:
		start, end = v.Start, v.End
		if len(v.Candidates) < 1 || len(v.Candidates) > 128 || (v.Reason != "overlap" && v.Reason != "equal_values") || (v.Reason == "equal_values" && len(v.Candidates) < 2) {
			return 0, 0, ErrInvalid
		}
		for _, c := range v.Candidates {
			if !validBinding(c.Binding) {
				return 0, 0, ErrInvalid
			}
			if _, err := transform([]byte("x"), c.Encoding); err != nil {
				return 0, 0, err
			}
		}
	case wire.GapSegment:
		start, end = v.Start, v.End
		if v.Reason != "capture_limit" && v.Reason != "possible_secret_prefix" && v.Reason != "producer_lost" {
			return 0, 0, ErrInvalid
		}
	default:
		return 0, 0, ErrInvalid
	}
	if end <= start {
		return 0, 0, ErrInvalid
	}
	return start, end, nil
}
func validateBatch(batch wire.CaptureBatch) error {
	if _, err := batchName(batch.AttemptId, batch.StreamId, batch.Sequence); err != nil {
		return err
	}
	if batch.SchemaVersion != 1 || len(batch.Segments) > 4096 {
		return ErrInvalid
	}
	var end uint64
	estimate := 2048
	for n, segment := range batch.Segments {
		start, next, err := bounds(segment)
		if err != nil {
			return err
		}
		if n > 0 && start != end {
			return ErrInvalid
		}
		end = next
		if _, ok := segment.(wire.GapSegment); ok && batch.Complete {
			return ErrInvalid
		}
		switch v := segment.(type) {
		case wire.LiteralSegment:
			estimate += len(v.Value) + 32
		case wire.SecretSegment:
			estimate += 2048
		case wire.AmbiguousSegment:
			estimate += len(v.Candidates)*2048 + 128
		case wire.GapSegment:
			estimate += 128
		}
		if estimate > maxBatch {
			return ErrLimit
		}
	}
	return nil
}
func (s *Spool) syncDir() error {
	dir, err := s.root.Open(".")
	if err != nil {
		return ErrUnavailable
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Spool) point(name string) {
	if s.checkpoint != nil {
		s.checkpoint(name)
	}
}

// Put acknowledges only after file and directory sync. Link publishes a full
// immutable file without replacing another writer's sequence. Exact retries
// recover the same receipt after a lost response. No secret values are hashed.
func (s *Spool) Put(batch wire.CaptureBatch) (wire.CaptureReceipt, error) {
	var empty wire.CaptureReceipt
	if err := validateBatch(batch); err != nil {
		return empty, err
	}
	data := wire.EncodeCaptureBatch(batch)
	if len(data) > maxBatch {
		return empty, ErrLimit
	}
	name, _ := batchName(batch.AttemptId, batch.StreamId, batch.Sequence)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return empty, ErrUnavailable
	}
	temp := "pending-" + hex.EncodeToString(nonce)
	f, err := s.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return empty, ErrUnavailable
	}
	defer s.root.Remove(temp) // Only this invocation's exact new temporary file.
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return empty, ErrUnavailable
	}
	s.point("file_synced")
	if err = s.root.Link(temp, name); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return empty, ErrUnavailable
		}
		existing, err := s.readBytes(name)
		if err != nil {
			return empty, err
		}
		if !bytes.Equal(existing, data) {
			return empty, ErrConflict
		}
	}
	s.point("published")
	if err = s.syncDir(); err != nil {
		return empty, err
	}
	s.point("directory_synced")
	digest := sha256.Sum256(data)
	return wire.CaptureReceipt{AttemptId: batch.AttemptId, StreamId: batch.StreamId, Sequence: batch.Sequence, Digest: digest[:]}, nil
}
func (s *Spool) readBytes(name string) ([]byte, error) {
	info, err := s.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxBatch {
		return nil, ErrInvalid
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBatch+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	if len(data) > maxBatch {
		return nil, ErrLimit
	}
	return data, nil
}
func (s *Spool) Read(attempt, stream string, sequence uint64) (wire.CaptureBatch, error) {
	var empty wire.CaptureBatch
	name, err := batchName(attempt, stream, sequence)
	if err != nil {
		return empty, err
	}
	data, err := s.readBytes(name)
	if err != nil {
		return empty, err
	}
	batch, err := wire.DecodeCaptureBatch(data)
	if err != nil {
		return empty, ErrInvalid
	}
	if err = validateBatch(batch); err != nil {
		return empty, err
	}
	if batch.AttemptId != attempt || batch.StreamId != stream || batch.Sequence != sequence || !bytes.Equal(data, wire.EncodeCaptureBatch(batch)) {
		return empty, ErrInvalid
	}
	return batch, nil
}

type Manifest struct {
	Expected uint64
	Received []uint64
	Missing  []uint64
	Complete bool
}

// Inspect uses a caller-declared final sequence. It reports capture continuity,
// not whether every process operation was observed. Orphan pending files are
// not accepted records. This bounded view has no automatic deletion policy.
func (s *Spool) Inspect(attempt, stream string, last uint64) (Manifest, error) {
	m := Manifest{Expected: last, Complete: true, Received: []uint64{}, Missing: []uint64{}}
	if last == 0 || last > 4096 {
		return m, ErrLimit
	}
	var end uint64
	for seq := uint64(1); seq <= last; seq++ {
		batch, err := s.Read(attempt, stream, seq)
		if errors.Is(err, os.ErrNotExist) {
			m.Missing = append(m.Missing, seq)
			m.Complete = false
			continue
		}
		if err != nil {
			return m, err
		}
		m.Received = append(m.Received, seq)
		if !batch.Complete || batch.Final != (seq == last) {
			m.Complete = false
		}
		for _, segment := range batch.Segments {
			start, next, _ := bounds(segment)
			if start != end {
				m.Complete = false
			}
			end = next
		}
	}
	return m, nil
}
