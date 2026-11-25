package crdt

import (
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestCompareLayoutStringMatchesUTF16CodeUnitOrder(t *testing.T) {
	t.Parallel()

	samples := []string{
		"",
		"a",
		"ab",
		"b",
		"0",
		"10",
		"2",
		// BMP code point U+E000 sorts AFTER supplementary plane U+1F600 in UTF-16
		// (0xE000 > high surrogate 0xD83D), even though U+E000 < U+1F600 in UTF-8/runes.
		"\uE000",
		"\U0001F600",
		"\U0001F601",
		"\U00010000",
		"\uFFFD",
		"prefix-\U0001F600-a",
		"prefix-\U0001F600-b",
		"prefix-\uE000",
		string([]byte{0xff, 'a'}),
	}

	for _, a := range samples {
		for _, b := range samples {
			want := slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
			assert.Equal(t, want, compareLayoutString(a, b), "a=%q b=%q", a, b)
		}
	}
}

func TestGridChildIDsOrderTieBreakAndValidation(t *testing.T) {
	t.Parallel()

	node := func(pos string) *leapmuxv1.NodeRecord {
		return &leapmuxv1.NodeRecord{
			Position: &leapmuxv1.LWWString{Value: pos},
		}
	}
	nodes := map[string]*leapmuxv1.NodeRecord{
		"b":       node("0,1"),
		"a":       node("0,1"), // tie with "b": lower UTF-16 ID wins
		"bottom":  node("1,2"),
		"leading": node("00,0"), // non-canonical leading zero ignored
		"oob":     node("2,0"),  // out of row range ignored
		"bad":     node("1,x"),  // malformed ignored
		"space":   node(" 0,0"), // whitespace ignored
	}
	children := []string{"b", "a", "bottom", "leading", "oob", "bad", "space"}

	got := gridChildIDs(nodes, children, 2, 3)
	assert.Equal(t, []string{"", "a", "", "", "", "bottom"}, got)

	assert.Nil(t, gridChildIDs(nodes, children, MaxGridDimension+1, 1))
	assert.Empty(t, gridChildIDs(nodes, children, 0, 3))
}
