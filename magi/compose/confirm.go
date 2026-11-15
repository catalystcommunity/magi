package compose

// Confirmation of durable actions in tainted turns (risk-remediation task
// 1.3, requirement 3).
//
// In a turn that read untrusted text, a call of remember, change_knowledge
// or start_code_change whose values have no known origin (taint.go labels
// them untrusted) does not run. Magi keeps the call as it is (a pending
// action), and the reply of the turn ends with a view that Magi itself
// writes: the exact bytes that would be written or sent, and the source of
// each value. The model does not write the view and does not see the code.
//
// The action runs only when the person writes "confirm <code>" in a later
// message of the same stream. The check reads the author and the kind of the
// utterance, which the conversation service sets from the token of the
// caller, so a model, a page, or another member cannot write it. What runs is
// the stored call: the bytes are the ones in the view, not a new composition
// by a model. A pending action is used once, and it ends after confirmTTL.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	conversationapi "github.com/catalystcommunity/magi/primitives/conversation/api"
	workflowapi "github.com/catalystcommunity/magi/primitives/workflow/api"
)

const (
	confirmTTL     = 30 * time.Minute
	maxPendingEach = 3 // for one person in one stream; the oldest ends first
)

var confirmWords = regexp.MustCompile(`(?i)^\s*(?:yes[,.]?\s+)?confirm\s+([0-9a-f]{6})\s*[.!]?\s*$`)

// pendingAction is a call that waits for the person.
type pendingAction struct {
	code      string
	owner     string
	stream    string
	utterance string // the message of the turn that made it
	persona   string
	tool      string
	args      map[string]string // a copy; these are the bytes of the view
	view      string
	made      time.Time
}

// confirmStore holds the pending actions. The zero value is ready.
type confirmStore struct {
	mu   sync.Mutex
	list []*pendingAction
	now  func() time.Time // tests
}

func (c *confirmStore) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// prune drops ended actions. The caller holds mu.
func (c *confirmStore) prune() {
	t := c.clock()
	c.list = slices.DeleteFunc(c.list, func(p *pendingAction) bool { return t.Sub(p.made) > confirmTTL })
}

func (c *confirmStore) add(p *pendingAction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune()
	p.made = c.clock()
	// The same call again in the same message is one action.
	c.list = slices.DeleteFunc(c.list, func(q *pendingAction) bool {
		return q.owner == p.owner && q.stream == p.stream && q.utterance == p.utterance && q.tool == p.tool && slices.Equal(argList(q.args), argList(p.args))
	})
	c.list = append(c.list, p)
	n := 0
	for _, q := range c.list {
		if q.owner == p.owner && q.stream == p.stream {
			n++
		}
	}
	for i := 0; n > maxPendingEach && i < len(c.list); {
		if c.list[i].owner == p.owner && c.list[i].stream == p.stream {
			c.list = slices.Delete(c.list, i, i+1)
			n--
			continue
		}
		i++
	}
}

// take removes and returns the pending action with code, for this person in this stream.
func (c *confirmStore) take(owner, stream, code string) *pendingAction {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune()
	for i, p := range c.list {
		if p.owner == owner && p.stream == stream && p.code == strings.ToLower(code) {
			c.list = slices.Delete(c.list, i, i+1)
			return p
		}
	}
	return nil
}

// views returns the views of the actions that the turn of utterance made.
func (c *confirmStore) views(utterance string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune()
	var out []string
	for _, p := range c.list {
		if p.utterance == utterance {
			out = append(out, p.view)
		}
	}
	return out
}

func argList(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"\x00"+v)
	}
	slices.Sort(out)
	return out
}

// needsConfirmation says that a rejected call is one that the person can confirm.
func needsConfirmation(err error) bool {
	se, ok := err.(*sinkError)
	return ok && se.Source == SourceUntrusted
}

// fenceFor returns a code fence that the text cannot close.
func fenceFor(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// confirmationView is the text for the person: each value exactly as it
// would be written, with its source. It reads the labels and the arguments only.
func (p *provenance) confirmationView(tool, code string, args map[string]string) string {
	var b strings.Builder
	what := map[string]string{
		"remember":          "write a record to your memory",
		"change_knowledge":  "change a record in your memory",
		"start_code_change": "start a code change; the title and the instructions go word for word into the body of a public pull request",
	}[tool]
	fmt.Fprintf(&b, "\n\n---\nMagi did not do this yet. I read text of others in this turn, so I need you to check it. I would %s (%s).\n", what, tool)
	if len(p.reasons) > 0 {
		fmt.Fprintf(&b, "This turn read: %s.\n", strings.Join(p.reasons, ", "))
	}
	b.WriteString("These are the exact values:\n")
	for _, arg := range sensitiveArgs[tool] {
		v := args[arg]
		if strings.TrimSpace(v) == "" {
			continue
		}
		f := fenceFor(v)
		fmt.Fprintf(&b, "\n%s (from: %s)\n%s\n%s\n%s\n", arg, sourceWords(p.Label(v)), f, v, f)
	}
	for _, arg := range []string{"operation", "field"} {
		if v := args[arg]; v != "" && tool == "change_knowledge" {
			fmt.Fprintf(&b, "\n%s: %s\n", arg, v)
		}
	}
	fmt.Fprintf(&b, "\nTo allow it, write exactly: confirm %s\nTo drop it, ignore this. It ends in %d minutes.\n", code, int(confirmTTL.Minutes()))
	return b.String()
}

func sourceWords(s Source) string {
	switch s {
	case SourcePerson:
		return "you, in this conversation"
	case SourceNamed:
		return "a name that Magi is set up with"
	case SourceOwnRecord:
		return "your records"
	}
	return "unknown, it may come from text of others"
}

// newCode gives the code of a pending action.
func newCode() string {
	raw := make([]byte, 3)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// holdForConfirmation keeps the call and tells the model what to say.
func (s *System) holdForConfirmation(a workflowapi.Assignment, as conversationLens, utt conversationapi.Utterance, prov *provenance, tool string, args map[string]string) string {
	code := newCode()
	cp := make(map[string]string, len(args))
	for k, v := range args {
		cp[k] = v
	}
	s.confirms.add(&pendingAction{code: code, owner: a.Owner, stream: utt.Stream, utterance: utt.Id, persona: as.persona,
		tool: tool, args: cp, view: prov.confirmationView(tool, code, cp)})
	s.Log.Info("confirmation needed", "run", a.Run, "tool", tool, "utterance", utt.Id)
	return "error: not done yet. This turn read text that may come from others, so the person must confirm first. " +
		"Magi shows the person the exact content and how to confirm, at the end of your reply. " +
		"Say in one short sentence that you need their confirmation. Do not repeat the content, and do not call the tool again."
}

// runConfirmed runs the pending action that the message of the person
// confirms. ok is false when the message is not a confirmation of an action
// of this person in this stream. The call runs as it was kept: the sink
// check does not run again, because the person has seen the bytes and their
// sources.
func (s *System) runConfirmed(ctx context.Context, a workflowapi.Assignment, as conversationLens, utt conversationapi.Utterance) (tool string, args map[string]string, out string, ref *conversationapi.Ref, ok bool) {
	if utt.AuthorKind != "human" || utt.Author != a.Owner {
		return
	}
	m := confirmWords.FindStringSubmatch(utt.Text)
	if m == nil {
		return
	}
	p := s.confirms.take(a.Owner, utt.Stream, m[1])
	if p == nil {
		return "", nil, "", nil, false
	}
	// The origin of the work is the stream and persona of the pending action.
	as.persona = p.persona
	// The person wrote the confirmation: that is what lets a promote run, and
	// a write that was held because of untrusted text is stored as untrusted.
	ctx = withPersonConfirmed(ctx)
	if p.args["operation"] != "promote" {
		ctx = withWriteTrust(ctx, trustUntrusted)
	}
	out, ref = s.runPersonaTool(ctx, a, as, utt, p.tool, p.args)
	s.Log.Info("confirmed action", "run", a.Run, "tool", p.tool, "confirmed_by", utt.Id, "asked_in", p.utterance, "result_ok", !strings.HasPrefix(out, "error"))
	return p.tool, p.args, out, ref, true
}
