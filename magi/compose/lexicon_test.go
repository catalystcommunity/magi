package compose

import (
	"context"
	"testing"

	"github.com/catalystcommunity/magi/lib/authn"
)

func TestCanEditLexicon(t *testing.T) {
	as := func(subject string) context.Context {
		return authn.With(context.Background(), authn.Principal{Subject: subject, Kind: authn.KindUser})
	}
	s := &System{Cfg: Config{Admins: []string{"user:tod@example.local"}}}
	if !s.canEditLexicon(as("user:tod@example.local")) || s.canEditLexicon(as("user:ada@example.local")) || s.canEditLexicon(context.Background()) {
		t.Fatal("admins must be the editors when LexiconEditors is empty")
	}
	s.Cfg.LexiconEditors = []string{"user:ada@example.local"}
	if s.canEditLexicon(as("user:tod@example.local")) || !s.canEditLexicon(as("user:ada@example.local")) {
		t.Fatal("LexiconEditors must replace the admins")
	}
}
