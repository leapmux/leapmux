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
	hasLimitPrefix := strings.HasPrefix(value, limitedOutputPrefix)
	limited = limited || hasLimitPrefix
	visible := strings.TrimPrefix(value, limitedOutputPrefix)
	if c.previous == "" {
		c.total = int64(len(visible))
	} else if strings.HasPrefix(visible, c.previous) {
		c.total = saturatingAdd(c.total, int64(len(visible)-len(c.previous)))
	} else if limited {
		overlap := suffixPrefixOverlap(c.previous, visible)
		if overlap > 0 {
			c.total = saturatingAdd(c.total, int64(len(visible)-overlap))
		} else {
			c.minimum = true
		}
	} else {
		c.minimum = true
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
