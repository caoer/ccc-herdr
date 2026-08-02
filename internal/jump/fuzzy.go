package jump

import "strings"

// Score rates query against text. The query is split on whitespace; every
// word must match for the whole query to match. Word scores add up.
// Matching is case-insensitive. Higher is better.
func Score(query, text string) (int, bool) {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return 0, true
	}
	t := strings.ToLower(text)
	total := 0
	for _, word := range strings.Fields(q) {
		s, ok := scoreWord(word, t)
		if !ok {
			return 0, false
		}
		total += s
	}
	return total, true
}

// scoreWord scores one query word. An exact substring dominates; otherwise
// the word must appear as an in-order subsequence, with bonuses for matches
// on word boundaries and for consecutive runs.
func scoreWord(word, text string) (int, bool) {
	if idx := strings.Index(text, word); idx >= 0 {
		score := 100 + 2*len(word)
		if idx == 0 || isBoundary(rune(text[idx-1])) {
			score += 20
		}
		return score, true
	}

	runes := []rune(text)
	score, pos, lastMatch := 0, 0, -2
	for _, wr := range word {
		found := false
		for i := pos; i < len(runes); i++ {
			if runes[i] != wr {
				continue
			}
			switch {
			case i == lastMatch+1:
				score += 6
			case i == 0 || isBoundary(runes[i-1]):
				score += 4
			default:
				score++
			}
			lastMatch, pos, found = i, i+1, true
			break
		}
		if !found {
			return 0, false
		}
	}
	return score, true
}

func isBoundary(r rune) bool {
	switch r {
	case ' ', '/', '-', '_', '.', ':', '|', '·', ',', '(', ')', '[', ']':
		return true
	}
	return false
}
