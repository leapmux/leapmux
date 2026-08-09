package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestParseSplitDirection_AcceptsCanonicalAndShortForm pins the
// aliases `tile split --direction` recognises. The h/v one-letter
// forms exist so the flag is ergonomic on the command line; if either
// is dropped a script relying on the short form silently falls
// through to the invalid_request path.
func TestParseSplitDirection_AcceptsCanonicalAndShortForm(t *testing.T) {
	cases := map[string]leapmuxv1.SplitDirection{
		"horizontal": leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL,
		"h":          leapmuxv1.SplitDirection_SPLIT_DIRECTION_HORIZONTAL,
		"vertical":   leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL,
		"v":          leapmuxv1.SplitDirection_SPLIT_DIRECTION_VERTICAL,
	}
	for in, want := range cases {
		got, ok := parseSplitDirection(in)
		assert.True(t, ok, "parseSplitDirection(%q) should succeed", in)
		assert.Equal(t, want, got, "parseSplitDirection(%q)", in)
	}
}

// TestParseSplitDirection_UnknownReturnsFalse covers the rejection
// path: the handler must surface invalid_request rather than silently
// defaulting to UNSPECIFIED, otherwise the CRDT op writes a
// zero-valued direction that makes the SPLIT node ambiguous.
func TestParseSplitDirection_UnknownReturnsFalse(t *testing.T) {
	for _, in := range []string{"", "diagonal", "VERTICAL", "Horizontal"} {
		_, ok := parseSplitDirection(in)
		assert.False(t, ok, "parseSplitDirection(%q) must fail closed", in)
	}
}
