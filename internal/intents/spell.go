package intents

import (
	"strings"
	"sync"
)

var (
	vocabOnce sync.Once
	vocab     map[string]bool
	vocabFreq map[string]int
	vocabList []string
)

// Words used by handlers that never appear in catalogue examples.
var extraVocab = []string{
	"room", "rooms", "guest", "guests", "user", "users", "rate", "rates", "report", "reports",
	"arrival", "arrivals", "departure", "departures", "today", "tomorrow", "tonight", "weekend",
	"available", "availability", "vacant", "occupied", "dirty", "clean", "list", "show", "all",
	"who", "what", "which", "where", "when", "how", "many", "much", "am", "is", "are",
}

func buildVocab() {
	vocab = map[string]bool{}
	vocabFreq = map[string]int{}
	add := func(s string) {
		for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
			if len(w) >= 2 {
				vocab[w] = true
				vocabFreq[w]++
			}
		}
	}
	for _, it := range Catalog {
		add(strings.ReplaceAll(it.ID, "_", " "))
		add(it.Module)
		for _, ex := range it.Examples {
			add(ex)
		}
	}
	for _, w := range extraVocab {
		vocab[w] = true
	}
	vocabList = make([]string, 0, len(vocab))
	for w := range vocab {
		vocabList = append(vocabList, w)
	}
}

func CorrectSpelling(message string) (string, bool) {
	vocabOnce.Do(buildVocab)
	changed := false
	out := wordRe.ReplaceAllStringFunc(strings.ToLower(message), func(w string) string {
		if len(w) < 4 || vocab[w] {
			return w
		}
		if isNumeric(w) {
			return w
		}
		// A known singular or plural is not a typo.
		if vocab[strings.TrimSuffix(w, "s")] || vocab[w+"s"] {
			return w
		}
		best, bestDist := "", 3
		for _, cand := range vocabList {
			// Same first letter only, so names and ids are left alone.
			if cand[0] != w[0] || abs(len(cand)-len(w)) > 2 {
				continue
			}
			d := editDistance(w, cand)
			// Tie: closer length ("roomw" → "rooms"), then the more common catalogue word, then the longer one ("cheking" → "checking", not "checkin").
			better := d < bestDist
			if d == bestDist && best != "" {
				cl, bl := abs(len(cand)-len(w)), abs(len(best)-len(w))
				cf, bf := vocabFreq[cand], vocabFreq[best]
				switch {
				case cl != bl:
					better = cl < bl
				case cf != bf:
					better = cf > bf
				default:
					better = len(cand) > len(best)
				}
			}
			if better {
				best, bestDist = cand, d
			}
		}
		limit := 1
		if len(w) >= 6 {
			limit = 2
		}
		if best == "" || bestDist > limit {
			return w
		}
		changed = true
		return best
	})
	return out, changed
}

func isNumeric(w string) bool {
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Adjacent transposition counts as one edit.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	prev2 := make([]int, lb+1)
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				if v := prev2[j-2] + 1; v < cur[j] {
					cur[j] = v
				}
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
