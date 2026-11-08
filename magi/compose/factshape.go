package compose

import (
	"context"
	"regexp"
	"strings"

	knowledgeapi "github.com/catalystcommunity/magi/primitives/knowledge/api"
)

// Small models give the facts of remember in shapes that the graph cannot
// use: "Dr. Okafor at Birch Lane Animal Clinic" as one person, "my work
// calendar" as a name, a date as a thing, and "household Nextcloud" next to
// "Nextcloud". shapeFact and sameEntity give the graph one entity for each
// thing and one claim for each fact (evals/knowledge, round0-v3).

// possessive starts a name that the person said about their own things.
var possessive = regexp.MustCompile(`^(?i:my|our|me|your)\s+|^the\s+`)

// cleanName removes the possessive words at the start of a name. "the" goes
// only in lower case, so that "The Home Depot" stays.
func cleanName(name string) string {
	name = strings.TrimSpace(name)
	for {
		n := possessive.ReplaceAllString(name, "")
		if n == name || strings.TrimSpace(n) == "" {
			return name
		}
		name = strings.TrimSpace(n)
	}
}

// atPlace is "<a name> at <a place>", such as "Dr. Rivera at Maple Street
// Dental". Both parts start with a capital letter.
var atPlace = regexp.MustCompile(`^((?:Dr|Mr|Mrs|Ms|Prof)\.?\s+\S.*?|\p{Lu}\S*(?:\s+\p{Lu}\S*)*)\s+at\s+(\p{Lu}.*)$`)

var month = `(?i:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`

// dateLike is a date or a number, which is a value and not a thing.
var dateLike = regexp.MustCompile(`^(?:` + month + `\.?\s+\d{1,2}(?:st|nd|rd|th)?(?:,?\s+\d{4})?|\d{1,2}(?:st|nd|rd|th)?\s+` + month + `(?:\s+\d{4})?|\d{4}-\d{2}-\d{2}|\d{1,2}/\d{1,2}(?:/\d{2,4})?|[\d.,]+)$`)

// shapeFact gives the fact in the shape of the graph, and the facts that
// the object held too ("X at Y": X works-at Y).
func shapeFact(f fact) (fact, []fact) {
	if !isSelf(f.subject) {
		f.subject = cleanName(f.subject)
	}
	f.object = strings.TrimSpace(f.object)
	if f.object != "" && !isSelf(f.object) {
		f.object = cleanName(f.object)
	}
	if f.object != "" && f.value == "" && dateLike.MatchString(f.object) {
		f.object, f.objectKind, f.value = "", "", f.object
	}
	// "family calendar located-in value: household Nextcloud": a thing
	// relation points to a thing, so that the graph links it.
	if f.object == "" && thingRelation[f.relation] && f.value != "" && !dateLike.MatchString(strings.TrimSpace(f.value)) {
		f.object, f.value = cleanName(f.value), ""
	}
	// "me job Brightline Logistics": a job with an organization is where
	// the person works ("Where do I work?" finds works-at).
	if f.object != "" && employer[f.relation] {
		f.relation = "works-at"
	}
	var more []fact
	if m := atPlace.FindStringSubmatch(f.object); m != nil && f.relation != "works-at" && f.relation != "located-in" {
		f.object = m[1]
		if f.objectKind == "" {
			f.objectKind = "person"
		}
		more = append(more, fact{subject: m[1], subjectKind: f.objectKind, relation: "works-at", object: m[2], objectKind: "organization"})
	}
	return f, more
}

func isSelf(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, s := range selfNames {
		if n == s {
			return true
		}
	}
	return false
}

// coreName is a name without the lower-case words before its proper name:
// "household Nextcloud" is "nextcloud". A name with no proper name has no
// core ("family calendar").
func coreName(name string) string {
	ws := strings.Fields(name)
	for i, w := range ws {
		r := []rune(w)
		if len(r) > 0 && r[0] >= 'A' && r[0] <= 'Z' || len(r) > 0 && r[0] > 127 && strings.ToUpper(string(r[0])) == string(r[0]) {
			if i == 0 {
				return strings.ToLower(name)
			}
			return strings.ToLower(strings.Join(ws[i:], " "))
		}
	}
	return ""
}

// sameEntity finds a current entity of the person that names the same
// proper name with other words before it ("household Nextcloud" and
// "Nextcloud"). It gives nil when there is none.
func (s *System) sameEntity(ctx context.Context, owner, name string) *knowledgeapi.Entity {
	core := coreName(name)
	if core == "" || len([]rune(core)) < 3 {
		return nil
	}
	res, err := s.Knowledge.Search(ctx, knowledgeapi.SearchRequest{Text: &core, Types: []string{"entity"}, Limit: ptr(uint64(10)), ActFor: &owner})
	if err != nil {
		return nil
	}
	for _, e := range res.Entities {
		if strings.EqualFold(e.Label, name) {
			return nil // the store gives this one
		}
	}
	for i, e := range res.Entities {
		if coreName(e.Label) == core {
			return &res.Entities[i]
		}
		for _, a := range e.Aliases {
			if coreName(a) == core {
				return &res.Entities[i]
			}
		}
	}
	return nil
}

// employer relations with an object are works-at.
var employer = map[string]bool{"job": true, "employer": true, "works-for": true, "work": true, "works": true, "employed-by": true, "employed-at": true, "job-at": true}
