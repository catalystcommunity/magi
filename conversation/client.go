package conversation

import (
	"context"
	"crypto/ed25519"
	w "github.com/catalystcommunity/magi/generated/go"
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
	RPC     *w.ConversationsClient
}

func NewClient(profile Profile) (*Client, error) {
	if !validActor(profile.Actor) || !validActor(profile.Recipient) || !validScope(profile.Owner) || len(profile.PrivateKey) != ed25519.PrivateKeySize || profile.KeyID == "" {
		return nil, ErrInvalid
	}
	transport, e := NewHTTPTransport(profile.Endpoint)
	if e != nil {
		return nil, e
	}
	return &Client{Profile: profile, RPC: w.NewConversationsClient(transport)}, nil
}
func (c *Client) sign(op string, body []byte) (w.SignedRecord, error) {
	return Sign(op, body, c.Profile.Actor, c.Profile.Recipient, c.Profile.KeyID, ed25519.PrivateKey(c.Profile.PrivateKey), time.Now().UTC().Truncate(time.Millisecond))
}
func (c *Client) Append(ctx context.Context, v w.ConversationAppend) (w.ConversationReceipt, error) {
	packet, e := c.sign("append", w.EncodeConversationAppend(v))
	if e != nil {
		return w.ConversationReceipt{}, e
	}
	return c.RPC.Append(ctx, packet)
}
func (c *Client) Read(ctx context.Context, ref w.RecordRef) (w.ConversationStored, error) {
	packet, e := c.sign("read", w.EncodeConversationRead(w.ConversationRead{Owner: c.Profile.Owner, Ref: ref}))
	if e != nil {
		return w.ConversationStored{}, e
	}
	return c.RPC.Read(ctx, packet)
}
func (c *Client) History(ctx context.Context, session string, after uint64) (w.ConversationPage, error) {
	return c.history(ctx, w.ConversationHistory{Owner: c.Profile.Owner, SessionId: session, AfterSequence: after, Limit: 4})
}

// Before returns records in descending admission order, below a fixed boundary.
func (c *Client) Before(ctx context.Context, session string, before uint64) (w.ConversationPage, error) {
	return c.history(ctx, w.ConversationHistory{Owner: c.Profile.Owner, SessionId: session, BeforeSequence: &before, Limit: 4})
}
func (c *Client) history(ctx context.Context, request w.ConversationHistory) (w.ConversationPage, error) {
	packet, e := c.sign("history", w.EncodeConversationHistory(request))
	if e != nil {
		return w.ConversationPage{}, e
	}
	return c.RPC.History(ctx, packet)
}
