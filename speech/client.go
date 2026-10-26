package speech

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	w "github.com/catalystcommunity/magi/generated/go"
	"io"
	"os"
	"strings"
	"time"
)

type Profile struct {
	Endpoint   string           `json:"endpoint"`
	Actor      w.ApplicationRef `json:"actor"`
	Recipient  w.ApplicationRef `json:"recipient"`
	Owner      w.ScopeRef       `json:"owner"`
	KeyID      string           `json:"key_id"`
	PrivateKey []byte           `json:"private_key"`
}
type Client struct {
	Profile Profile
	RPC     *w.SpeechClient
}

func LoadProfile(path string) (Profile, error) {
	var out Profile
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 16384 {
		return out, errors.New("profile must be a private regular file")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return out, e
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || dec.Decode(new(any)) != io.EOF {
		return out, ErrInvalid
	}
	return out, nil
}
func NewClient(profile Profile) (*Client, error) {
	if !validActor(profile.Actor) || !validActor(profile.Recipient) || !validScope(profile.Owner) || len(profile.PrivateKey) != ed25519.PrivateKeySize || profile.KeyID == "" {
		return nil, ErrInvalid
	}
	transport, e := NewHTTPTransport(profile.Endpoint)
	if e != nil {
		return nil, e
	}
	return &Client{Profile: profile, RPC: w.NewSpeechClient(transport)}, nil
}
func (c *Client) sign(op string, body []byte) (w.SignedRecord, error) {
	return Sign(op, body, c.Profile.Actor, c.Profile.Recipient, c.Profile.KeyID, ed25519.PrivateKey(c.Profile.PrivateKey), time.Now().UTC().Truncate(time.Millisecond))
}
func (c *Client) Submit(ctx context.Context, v w.SpeechSubmit) (w.SpeechStatus, error) {
	packet, e := c.sign("submit", w.EncodeSpeechSubmit(v))
	if e != nil {
		return w.SpeechStatus{}, e
	}
	return c.RPC.Submit(ctx, packet)
}
func (c *Client) Read(ctx context.Context, ref w.SpeechRef) (w.SpeechStatus, error) {
	packet, e := c.sign("read", w.EncodeSpeechRef(ref))
	if e != nil {
		return w.SpeechStatus{}, e
	}
	return c.RPC.Read(ctx, packet)
}
func (c *Client) Cancel(ctx context.Context, ref w.SpeechRef) (w.SpeechStatus, error) {
	packet, e := c.sign("cancel", w.EncodeSpeechRef(ref))
	if e != nil {
		return w.SpeechStatus{}, e
	}
	return c.RPC.Cancel(ctx, packet)
}
func (c *Client) Evidence(ctx context.Context, ref w.SpeechRef) (w.SpeechEvidence, error) {
	packet, e := c.sign("read-evidence", w.EncodeSpeechRef(ref))
	if e != nil {
		return w.SpeechEvidence{}, e
	}
	return c.RPC.ReadEvidence(ctx, packet)
}
func (c *Client) Capabilities(ctx context.Context) (w.SpeechCapabilities, error) {
	packet, e := c.sign("capabilities", w.EncodeScopeRef(c.Profile.Owner))
	if e != nil {
		return w.SpeechCapabilities{}, e
	}
	return c.RPC.Capabilities(ctx, packet)
}
func (c *Client) Upload(ctx context.Context, ref w.SpeechRef, pcm []byte) error {
	if len(pcm) == 0 || len(pcm) > MaxInputSamples*2 || len(pcm)%2 != 0 {
		return ErrInvalid
	}
	for offset := 0; offset < len(pcm); offset += MaxChunk {
		packet, e := c.sign("upload", w.EncodeSpeechUpload(w.SpeechUpload{Ref: ref, Offset: uint64(offset), Pcm: pcm[offset:min(offset+MaxChunk, len(pcm))]}))
		if e != nil {
			return e
		}
		status, e := c.RPC.Upload(ctx, packet)
		if e != nil {
			return e
		}
		if status.State == "failed" {
			return errors.New(status.ErrorCode)
		}
	}
	return nil
}
func (c *Client) Wait(ctx context.Context, ref w.SpeechRef) (w.SpeechStatus, error) {
	interval := 250 * time.Millisecond
	if strings.HasPrefix(c.Profile.Endpoint, "https://") {
		interval = 5 * time.Second
	}
	for {
		status, e := c.Read(ctx, ref)
		if e != nil {
			return status, e
		}
		if terminal(status.State) {
			if status.State != "completed" {
				return status, errors.New("speech request ended: " + status.ErrorCode)
			}
			return status, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}
func (c *Client) Audio(ctx context.Context, status w.SpeechStatus) ([]byte, error) {
	if status.State != "completed" || status.Kind != "tts" || status.OutputBytes == 0 || status.OutputBytes > MaxOutputBytes || status.OutputDigest == nil {
		return nil, ErrInvalid
	}
	out := make([]byte, 0, int(status.OutputBytes))
	for uint64(len(out)) < status.OutputBytes {
		packet, e := c.sign("read-audio", w.EncodeSpeechReadAudio(w.SpeechReadAudio{Ref: status.Ref, Offset: uint64(len(out))}))
		if e != nil {
			return nil, e
		}
		chunk, e := c.RPC.ReadAudio(ctx, packet)
		if e != nil {
			return nil, e
		}
		if chunk.Ref != status.Ref || chunk.Offset != uint64(len(out)) || chunk.TotalBytes != status.OutputBytes || !bytes.Equal(chunk.Digest, *status.OutputDigest) || len(chunk.Pcm) == 0 || len(chunk.Pcm) > MaxChunk || uint64(len(out)+len(chunk.Pcm)) > status.OutputBytes {
			return nil, ErrInvalid
		}
		out = append(out, chunk.Pcm...)
	}
	hash := sha256.Sum256(out)
	if !bytes.Equal(hash[:], *status.OutputDigest) {
		return nil, ErrInvalid
	}
	return out, nil
}
