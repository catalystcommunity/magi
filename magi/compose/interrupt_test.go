package compose

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"

	"github.com/catalystcommunity/magi/magi/model"
	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
)

const spokenReply = "The meeting is at three. Bring the notes from Tuesday. After that we can plan the trip to the coast."

func TestSentenceSpans(t *testing.T) {
	var got []string
	for _, sp := range sentenceSpans(spokenReply + "\nAnd one more") {
		got = append(got, string([]rune(spokenReply + "\nAnd one more")[sp.start:sp.end]))
	}
	want := []string{"The meeting is at three.", "Bring the notes from Tuesday.", "After that we can plan the trip to the coast.", "And one more"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestSentenceSpansList(t *testing.T) {
	text := "1. Boil water.\n2. Add the tea. 3. Wait."
	var got []string
	for _, sp := range sentenceSpans(text) {
		got = append(got, string([]rune(text)[sp.start:sp.end]))
	}
	if strings.Join(got, "|") != "1. Boil water.|2. Add the tea.|3. Wait." {
		t.Fatalf("got %q", got)
	}
}

func TestReadableUntilAndWordAt(t *testing.T) {
	// 4 words a second: in 1.5 s, 6 words.
	if at := readableUntil(spokenReply, 1500); string([]rune(spokenReply)[:at]) != "The meeting is at three. Bring " {
		t.Fatalf("readable: %q", string([]rune(spokenReply)[:at]))
	}
	if readableUntil(spokenReply, 600000) != len([]rune(spokenReply)) {
		t.Fatal("a long time reads it all")
	}
	w := wordAt(spokenReply, 30) // "...three. Bring|"
	if got := string([]rune(spokenReply)[w.start:w.end]); got != "Bring" {
		t.Fatalf("word at 30: %q", got)
	}
	if w := wordAt(spokenReply, 0); string([]rune(spokenReply)[w.start:w.end]) != "The" {
		t.Fatalf("word at 0: %+v", w)
	}
}

func TestClassify(t *testing.T) {
	ss := sentenceSpans(spokenReply)
	at := func(n int) *int { return &n }
	text := func(in interruption) string { return string([]rune(spokenReply)[in.start:in.end]) }
	// Stopped in the second sentence; the message answers the first.
	in := classify(spokenReply, delivery{Reason: "interrupted", SpokenUntil: at(30), ShownMs: 1000, StoppedMs: 3000}, ss, 0)
	if in.kind != "interrupts" || text(in) != "The meeting is at three." || in.read {
		t.Fatalf("interrupts: %+v", in)
	}
	// The message answers the third sentence, and the person had time to read it.
	in = classify(spokenReply, delivery{Reason: "typing", SpokenUntil: at(30), ShownMs: 1000, StoppedMs: 9000}, ss, 2)
	if in.kind != "reads-ahead" || !in.read || !strings.HasPrefix(text(in), "After that") {
		t.Fatalf("reads ahead: %+v", in)
	}
	// The same, but no time to read it: the last heard sentence.
	in = classify(spokenReply, delivery{Reason: "typing", SpokenUntil: at(30), ShownMs: 1000, StoppedMs: 2000}, ss, 2)
	if in.kind != "interrupts" || text(in) != "Bring the notes from Tuesday." {
		t.Fatalf("not readable: %+v", in)
	}
	// Nothing in the reply: cut off at the word where playback stopped.
	in = classify(spokenReply, delivery{Reason: "interrupted", SpokenUntil: at(30)}, ss, -1)
	if in.kind != "cuts-off" || text(in) != "Bring" {
		t.Fatalf("cuts off: %+v", in)
	}
}

// pickModel answers a fixed sentence.
type pickModel struct{ answer, prompt string }

func (p *pickModel) Chat(context.Context, []model.Message, []model.Tool, int) (model.Message, error) {
	return model.Message{}, nil
}

func (p *pickModel) ChatSchema(_ context.Context, msgs []model.Message, _ map[string]any, _ int) (model.Message, error) {
	p.prompt = msgs[0].Content
	return model.Message{Role: "assistant", Content: p.answer}, nil
}

// annConversation answers ListAnnotations from a list.
type annConversation struct {
	conversationapi.ConversationService
	anns []conversationapi.Annotation
}

func (f *annConversation) ListAnnotations(_ context.Context, r conversationapi.ListAnnotationsRequest) (conversationapi.ListAnnotationsResponse, error) {
	out := conversationapi.ListAnnotationsResponse{Annotations: []conversationapi.Annotation{}}
	for _, a := range f.anns {
		if a.Utterance == r.Utterance {
			out.Annotations = append(out.Annotations, a)
		}
	}
	return out, nil
}

func TestFindInterruption(t *testing.T) {
	ac := &annConversation{}
	pm := &pickModel{answer: `{"sentence": 0}`}
	sys := &System{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LibrarianModel: pm, Conversation: nil}
	sys.Conversation = conversationClient(t, ac)
	reply := conversationapi.Utterance{Id: "utt_r", AuthorKind: "persona", Modality: "voice", Text: spokenReply}
	msg := conversationapi.Utterance{Id: "utt_m", AuthorKind: "human", Text: "wait, at three what?"}
	hist := []conversationapi.Utterance{{Id: "utt_q", AuthorKind: "human"}, reply, msg}
	ctx := testCtx
	if in := sys.findInterruption(ctx, "user:a@example.local", msg, hist); in != nil {
		t.Fatalf("no delivery report: %+v", in)
	}
	// Another person's report does not count; the person's own does.
	ac.anns = []conversationapi.Annotation{
		{Utterance: "utt_r", Kind: annDelivery, Author: "user:b@example.local", Body: `{"reason":"interrupted","spoken_until":10,"stopped_ms":5}`},
		{Utterance: "utt_r", Kind: annDelivery, Author: "user:a@example.local", Body: `{"reason":"interrupted","spoken_until":30,"shown_ms":1,"stopped_ms":2000}`},
	}
	in := sys.findInterruption(ctx, "user:a@example.local", msg, hist)
	if in == nil || in.kind != "interrupts" || in.reply.Id != "utt_r" || in.heard != 30 || !strings.Contains(pm.prompt, "wait, at three what?") {
		t.Fatalf("interruption: %+v", in)
	}
	if h := in.heardText(); !strings.HasPrefix(h, "The meeting is at three. Bring [The person stopped you here") {
		t.Fatalf("heard text: %q", h)
	}
	// Played to the end: no interruption.
	ac.anns = append(ac.anns, conversationapi.Annotation{Utterance: "utt_r", Kind: annDelivery, Author: "user:a@example.local", Body: `{"reason":"done","spoken_until":101}`})
	if in := sys.findInterruption(ctx, "user:a@example.local", msg, hist); in != nil {
		t.Fatalf("done: %+v", in)
	}
	// A text reply is not spoken.
	reply.Modality = "text"
	if in := sys.findInterruption(ctx, "user:a@example.local", msg, []conversationapi.Utterance{reply, msg}); in != nil {
		t.Fatalf("text reply: %+v", in)
	}
}

var testCtx context.Context

// testToken is the service token of testCtx (for System.Cfg.ServiceToken).
var testToken string

// conversationClient serves fake behind the rpc server, and sets testCtx to
// a context with a service token.
func conversationClient(t *testing.T, fake conversationapi.ConversationService) *conversationapi.ConversationClient {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	iss := &authn.Issuer{Name: "test", KeyID: "k1", Key: priv}
	srv := &rpc.Server{Verifier: &authn.Verifier{Issuer: "test", Keys: map[string]ed25519.PublicKey{"k1": pub}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Register("ConversationService", conversationapi.ConversationServiceOps(fake))
	h := httptest.NewServer(rpc.Mux(srv, nil))
	t.Cleanup(h.Close)
	tok, _ := iss.Issue(authn.Principal{Subject: "service:magi", Kind: authn.KindService}, time.Hour)
	testCtx, testToken = rpc.WithToken(context.Background(), tok), tok
	return conversationapi.NewConversationClient(rpc.NewClient(h.URL, nil))
}
