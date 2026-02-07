package lexorank

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFirst(t *testing.T) {
	f := First()
	assert.NotEmpty(t, f)
}

func TestAfter(t *testing.T) {
	a := First()
	b := After(a)
	assert.Greater(t, b, a)

	c := After(b)
	assert.Greater(t, c, b)
}

func TestMid(t *testing.T) {
	tests := []struct {
		name string
		a, b string
	}{
		{"both empty", "", ""},
		{"a empty", "", "n"},
		{"b empty", "n", ""},
		{"simple", "a", "z"},
		{"adjacent", "a", "b"},
		{"close", "a", "c"},
		{"mid range", "m", "o"},
		{"multi char", "abc", "abd"},
		{"different lengths", "a", "bb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Mid(tt.a, tt.b)
			assert.NotEmpty(t, result)
			if tt.a != "" {
				assert.Greater(t, result, tt.a)
			}
			if tt.b != "" {
				assert.Less(t, result, tt.b)
			}
		})
	}
}

func TestMidSequential(t *testing.T) {
	// Generate a sequence of ranks by repeatedly inserting at the end.
	ranks := []string{First()}
	for i := 0; i < 20; i++ {
		next := After(ranks[len(ranks)-1])
		assert.Greater(t, next, ranks[len(ranks)-1], "iteration %d", i)
		ranks = append(ranks, next)
	}

	// Verify all ranks are strictly increasing.
	for i := 1; i < len(ranks); i++ {
		assert.Greater(t, ranks[i], ranks[i-1], "ranks[%d]=%q should be > ranks[%d]=%q", i, ranks[i], i-1, ranks[i-1])
	}
}

func TestMidInsertBetween(t *testing.T) {
	// Create two ranks and repeatedly insert between them.
	a := "b"
	b := "y"
	for i := 0; i < 20; i++ {
		m := Mid(a, b)
		assert.Greater(t, m, a, "iteration %d", i)
		assert.Less(t, m, b, "iteration %d", i)
		// Alternate inserting before and after the midpoint.
		if i%2 == 0 {
			b = m
		} else {
			a = m
		}
	}
}

func TestBefore(t *testing.T) {
	s := "n"
	b := before(s)
	assert.Less(t, b, s)

	// Repeatedly insert before.
	for i := 0; i < 10; i++ {
		prev := before(b)
		assert.Less(t, prev, b, "iteration %d", i)
		b = prev
	}
}
