package compose

import (
	"fmt"

	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
	"testing"
	"time"
)

func TestAgeNote(t *testing.T) {
	old := zone
	zone = time.UTC
	defer func() { zone = old }()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct{ rel, v, want string }{
		{"birthday", "February 21, 2019", "7 years ago; the next one is Sunday 21 February 2027, 8 years"},
		{"birthday", "2016-09-03", "10 years ago; the next one is Friday 3 September 2027, 11 years"},
		{"married-on", "September 27, 2014", "12 years ago; the next one is Monday 27 September 2027, 13 years"},
		{"birthday", "2016-11-09", "10 years ago; the next one is Monday 9 November 2026, 10 years"},
		{"birthday", "April 2", ""},
		{"vet", "2016-09-03", ""},
	} {
		if got := ageNote(c.rel, c.v, now); got != c.want {
			t.Errorf("%s %s: %q", c.rel, c.v, got)
		}
	}
}

func TestKnowledgeItemOrder(t *testing.T) {
	ptrS := func(s string) *string { return &s }
	ms := func(v uint64) *uint64 { return &v }
	a := knowledgeapi.AboutResponse{Entity: knowledgeapi.Entity{Id: "ent_me", Label: "Jamie"}}
	a.Claims = append(a.Claims, knowledgeapi.Claim{Id: "clm_old", Subject: "ent_me", Relation: "lives-in", Value: ptrS("Pine Street"), Status: "ended", ValidFromMs: ms(1)})
	for i := 0; i < 30; i++ {
		a.Claims = append(a.Claims, knowledgeapi.Claim{Id: fmt.Sprintf("clm_n%d", i), Subject: "ent_me", Relation: "lunch-with", Value: ptrS("someone"), Status: "current", ValidFromMs: ms(uint64(10 + i))})
	}
	a.Claims = append(a.Claims, knowledgeapi.Claim{Id: "clm_new", Subject: "ent_me", Relation: "lives-in", Value: ptrS("Zuni Street"), Status: "current", ValidFromMs: ms(100)})
	it := knowledgeItem(a, "user:a", nil, "where do I live")
	if len(it.Facts) != 20 || it.Facts[0].Id != "clm_new" || it.Facts[1].Id != "clm_old" {
		t.Errorf("order: %v %v", it.Facts[0].Id, it.Facts[1].Id)
	}
}
