package compose

import (
	"bufio"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// popularityFiles is the list: one gzip file for each 50,000 ranks, with
// one "rank,domain" in each line. tools/popularity-list.py writes them.
//
//go:embed popularity/*.csv.gz
var popularityFiles embed.FS

var (
	popularityOnce sync.Once
	popularityList *Popularity
	popularityErr  error
)

// popularity gives the list of the system: the list of a test, or the
// list in the binary. The first call reads the files (about one second).
func (s *System) popularity() (*Popularity, error) {
	if s.Popularity != nil {
		return s.Popularity, nil
	}
	popularityOnce.Do(func() { popularityList, popularityErr = loadPopularity() })
	return popularityList, popularityErr
}

func loadPopularity() (*Popularity, error) {
	files, err := popularityFiles.ReadDir("popularity")
	if err != nil {
		return nil, err
	}
	var parts []io.Reader
	for _, f := range files {
		r, err := popularityFiles.Open("popularity/" + f.Name())
		if err != nil {
			return nil, err
		}
		defer r.Close()
		z, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("popularity list %s: %w", f.Name(), err)
		}
		parts = append(parts, z)
	}
	return readPopularity(io.MultiReader(parts...))
}

// Popularity is a public list of the most visited domains with their
// ranks. The decision model of the automatic destination decision gets
// the rank of a host as evidence: an invented name and a name that looks
// like a known name are not in the list. The list is not a permission:
// many popular sites are not sources.
type Popularity struct {
	// domains are sorted; ranks has the rank of the domain with the same index.
	domains []string
	ranks   []uint32
}

func readPopularity(r io.Reader) (*Popularity, error) {
	type entry struct {
		domain string
		rank   uint32
	}
	var all []entry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		rank, domain, ok := strings.Cut(line, ",")
		n, err := strconv.ParseUint(rank, 10, 32)
		if !ok || err != nil || n == 0 || domain == "" {
			return nil, fmt.Errorf("popularity list: line %d is not \"rank,domain\"", len(all)+1)
		}
		all = append(all, entry{strings.ToLower(domain), uint32(n)})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("popularity list: no entry")
	}
	sort.Slice(all, func(i, j int) bool { return all[i].domain < all[j].domain })
	p := &Popularity{domains: make([]string, len(all)), ranks: make([]uint32, len(all))}
	for i, e := range all {
		p.domains[i], p.ranks[i] = e.domain, e.rank
	}
	return p, nil
}

// Len is the number of domains.
func (p *Popularity) Len() int { return len(p.domains) }

// rank gives the best rank of the domains of a host and that domain: the
// host and each parent with two or more labels. 0 is "not in the list".
func (p *Popularity) rank(host string) (string, uint32) {
	labels := strings.Split(strings.ToLower(host), ".")
	var best string
	var rank uint32
	for i := 0; i+1 < len(labels); i++ {
		d := strings.Join(labels[i:], ".")
		j := sort.SearchStrings(p.domains, d)
		if j < len(p.domains) && p.domains[j] == d && (rank == 0 || p.ranks[j] < rank) {
			best, rank = d, p.ranks[j]
		}
	}
	return best, rank
}

// line is the popularity line of the state of the host question. The
// decision model was trained with these two forms: do not change them
// without a new training (evals/egress).
func (p *Popularity) line(host string) string {
	if d, r := p.rank(host); r > 0 {
		return fmt.Sprintf("Popularity: the domain %s has rank %d in a public list of the 1,000,000 most visited domains.", d, r)
	}
	return "Popularity: no domain of this host is in a public list of the 1,000,000 most visited domains."
}
