package compose

import (
	"context"
	"slices"

	"github.com/catalystcommunity/magi/lib/rpc"
	magiapi "github.com/catalystcommunity/magi/magi/api"
	speechapi "github.com/catalystcommunity/magi/primitives/speech/api"
)

// The household lexicon lives in the speech primitive. Magi decides who
// can change it (Config.LexiconEditors) and makes the change with its own
// credential, which has the scope speech:lexicon.

// canEditLexicon reports whether the caller is a lexicon editor.
func (s *System) canEditLexicon(ctx context.Context) bool {
	p, err := rpc.Caller(ctx)
	if err != nil {
		return false
	}
	editors := s.Cfg.LexiconEditors
	if len(editors) == 0 {
		editors = s.Cfg.Admins
	}
	return slices.Contains(editors, p.Subject)
}

func lexiconWord(e speechapi.LexiconEntry) magiapi.LexiconWord {
	h := e.HeardAs
	if h == nil {
		h = []string{}
	}
	return magiapi.LexiconWord{Written: e.Written, Spoken: e.Spoken, Ipa: e.Ipa, HeardAs: h, Note: e.Note,
		UpdatedBy: e.UpdatedBy, UpdatedMs: e.UpdatedMs}
}

func (m *Service) ListLexicon(ctx context.Context, _ magiapi.ListLexiconRequest) (magiapi.ListLexiconResponse, error) {
	res, err := m.S.Speech.ListLexicon(AsCaller(ctx), speechapi.ListLexiconRequest{})
	if err != nil {
		return magiapi.ListLexiconResponse{}, err
	}
	out := magiapi.ListLexiconResponse{Words: []magiapi.LexiconWord{}, CanEdit: m.S.canEditLexicon(ctx)}
	for _, e := range res.Entries {
		out.Words = append(out.Words, lexiconWord(e))
	}
	return out, nil
}

func (m *Service) PutLexiconWord(ctx context.Context, req magiapi.PutLexiconWordRequest) (magiapi.LexiconWord, error) {
	if !m.S.canEditLexicon(ctx) {
		return magiapi.LexiconWord{}, rpc.Denied("only a lexicon editor can change the lexicon")
	}
	p, _ := rpc.Caller(ctx)
	e, err := m.S.Speech.PutLexiconEntry(m.S.AsService(ctx), speechapi.PutLexiconEntryRequest{
		Written: req.Written, Spoken: req.Spoken, Ipa: req.Ipa, HeardAs: req.HeardAs, Note: req.Note, By: &p.Subject})
	if err != nil {
		return magiapi.LexiconWord{}, err
	}
	return lexiconWord(e), nil
}

func (m *Service) DeleteLexiconWord(ctx context.Context, req magiapi.DeleteLexiconWordRequest) (magiapi.DeleteLexiconWordResponse, error) {
	if !m.S.canEditLexicon(ctx) {
		return magiapi.DeleteLexiconWordResponse{}, rpc.Denied("only a lexicon editor can change the lexicon")
	}
	res, err := m.S.Speech.DeleteLexiconEntry(m.S.AsService(ctx), speechapi.DeleteLexiconEntryRequest{Written: req.Written})
	if err != nil {
		return magiapi.DeleteLexiconWordResponse{}, err
	}
	return magiapi.DeleteLexiconWordResponse{Deleted: res.Deleted}, nil
}

func (m *Service) Speak(ctx context.Context, req magiapi.SpeakRequest) (magiapi.SpeakResponse, error) {
	a, err := m.S.Speech.Synthesize(AsCaller(ctx), speechapi.SynthesizeRequest{Text: req.Text, Voice: req.Voice})
	if err != nil {
		return magiapi.SpeakResponse{}, err
	}
	return magiapi.SpeakResponse{Spoken: a.Spoken, Audio: a.Body, MediaType: a.MediaType}, nil
}
