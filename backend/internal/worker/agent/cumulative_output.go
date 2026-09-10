package agent

import "strings"

const limitedOutputPrefix = "...\n\n"

// CumulativeOutputObservation is the known byte total for a cumulative stream.
type CumulativeOutputObservation struct {
	Total   int64
	Minimum bool
}

// CumulativeOutputCounter counts append-only snapshots and limited output tails.
type CumulativeOutputCounter struct {
	previous string
	total    int64
	minimum  bool
}

func (c *CumulativeOutputCounter) Observe(value string, limited bool) CumulativeOutputObservation {
	visible := value
	if c.previous == "" {
		c.total = int64(len(visible))
	} else if limited {
		if strings.HasPrefix(visible, c.previous) {
			c.total = saturatingAdd(c.total, int64(len(visible)-len(c.previous)))
		} else if overlap := suffixPrefixOverlap(c.previous, visible); overlap > 0 {
			c.total = saturatingAdd(c.total, int64(len(visible)-overlap))
		} else {
			c.total = max(c.total, int64(len(visible)))
			c.minimum = true
		}
	} else if len(visible) <= len(c.previous) && visible != c.previous {
		c.minimum = true
		c.total = max(c.total, int64(len(visible)))
	} else {
		// Providers specify a non-limited update as the complete append-only
		// snapshot. Its length is the exact total, so a growing update needs no
		// scan of all prior bytes.
		c.total = max(c.total, int64(len(visible)))
	}
	c.minimum = c.minimum || limited
	c.previous = visible
	return CumulativeOutputObservation{Total: c.total, Minimum: c.minimum}
}

func suffixPrefixOverlap(previous, next string) int {
	if previous == "" || next == "" {
		return 0
	}
	prefix := make([]int, len(next))
	for index, matched := 1, 0; index < len(next); index++ {
		for matched > 0 && next[index] != next[matched] {
			matched = prefix[matched-1]
		}
		if next[index] == next[matched] {
			matched++
		}
		prefix[index] = matched
	}
	matched := 0
	start := max(0, len(previous)-len(next))
	for index := start; index < len(previous); index++ {
		for matched > 0 && previous[index] != next[matched] {
			matched = prefix[matched-1]
		}
		if previous[index] == next[matched] {
			matched++
		}
		if matched == len(next) && index != len(previous)-1 {
			matched = prefix[matched-1]
		}
	}
	return matched
}
