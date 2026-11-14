package server

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	api "github.com/catalystcommunity/magi/primitives/models/api"
)

// Merge returns the default of a task with the given fields of over in
// place of its fields.
func Merge(def, over api.Selector) api.Selector {
	out := def
	if over.Name != nil {
		out.Name = over.Name
	}
	if over.All != nil {
		out.All = over.All
	}
	if over.Any != nil {
		out.Any = over.Any
	}
	if over.None != nil {
		out.None = over.None
	}
	if over.Attributes != nil {
		out.Attributes = over.Attributes
	}
	if over.MaxInputCost != nil {
		out.MaxInputCost = over.MaxInputCost
	}
	if over.MaxOutputCost != nil {
		out.MaxOutputCost = over.MaxOutputCost
	}
	if over.RankBy != nil {
		out.RankBy = over.RankBy
	}
	if over.OnlyName != nil {
		out.OnlyName = over.OnlyName
	}
	return out
}

// mismatch tells why m does not match s, or "" when it matches. The name is
// not a filter.
func mismatch(m api.Model, s api.Selector) string {
	if !m.Enabled {
		return "disabled"
	}
	for _, t := range s.All {
		if !slices.Contains(m.Tags, t) {
			return "no tag " + t
		}
	}
	if len(s.Any) > 0 && !slices.ContainsFunc(s.Any, func(t string) bool { return slices.Contains(m.Tags, t) }) {
		return "none of the tags " + strings.Join(s.Any, ", ")
	}
	for _, t := range s.None {
		if slices.Contains(m.Tags, t) {
			return "tag " + t
		}
	}
	for k, v := range s.Attributes {
		if m.Attributes[k] != v {
			return fmt.Sprintf("attribute %s is not %s", k, v)
		}
	}
	if s.MaxInputCost != nil && m.Cost.InputPerMtok > *s.MaxInputCost {
		return "input cost"
	}
	if s.MaxOutputCost != nil && m.Cost.OutputPerMtok > *s.MaxOutputCost {
		return "output cost"
	}
	return ""
}

// Select orders the models for s:
//
//  1. The named model, when it is enabled. It need not match the other
//     fields (a name is an explicit choice), except with strict.
//  2. With only_name, nothing more. Else the models that match all the
//     other fields: with a rank_by score, the higher score first and the
//     models with no score after them; then the lower cost; then the name.
func Select(models []api.Model, s api.Selector, strict bool) []api.Candidate {
	var out []api.Candidate
	name := ""
	if s.Name != nil {
		name = *s.Name
	}
	if name != "" {
		for _, m := range models {
			if m.Name == name && m.Enabled && (!strict || mismatch(m, s) == "") {
				out = append(out, api.Candidate{Model: m, Reason: "named"})
			}
		}
		if s.OnlyName != nil && *s.OnlyName {
			return out
		}
	}
	rank := ""
	if s.RankBy != nil {
		rank = *s.RankBy
	}
	var rest []api.Model
	for _, m := range models {
		if m.Name != name && mismatch(m, s) == "" {
			rest = append(rest, m)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		a, b := rest[i], rest[j]
		if rank != "" {
			sa, oka := a.Scores[rank]
			sb, okb := b.Scores[rank]
			if oka != okb {
				return oka
			}
			if oka && sa != sb {
				return sa > sb
			}
		}
		ca, cb := a.Cost.InputPerMtok+a.Cost.OutputPerMtok, b.Cost.InputPerMtok+b.Cost.OutputPerMtok
		if ca != cb {
			return ca < cb
		}
		return a.Name < b.Name
	})
	for _, m := range rest {
		r := "matches"
		if rank != "" {
			if v, ok := m.Scores[rank]; ok {
				r += fmt.Sprintf("; %s %.3f", rank, v)
			} else {
				r += "; no " + rank + " score"
			}
		}
		if c := m.Cost.InputPerMtok + m.Cost.OutputPerMtok; c == 0 {
			r += "; free"
		}
		out = append(out, api.Candidate{Model: m, Reason: r})
	}
	return out
}
