package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestPageParams pins the conversion every paginated handler shares between
// the wire page and the store's page. An absent page is the proto3 default a
// caller sends by omitting the field, so it must read a full default page.
func TestPageParams(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		page *leapmuxv1.PageRequest
		want store.PageParams
	}{
		"an absent page reads the first default page": {nil, store.PageParams{Limit: contracts.DefaultPageLimit}},
		"a zero limit takes the default":              {&leapmuxv1.PageRequest{}, store.PageParams{Limit: contracts.DefaultPageLimit}},
		"a negative limit takes the default":          {&leapmuxv1.PageRequest{Limit: -5}, store.PageParams{Limit: contracts.DefaultPageLimit}},
		"one row is a legal page":                     {&leapmuxv1.PageRequest{Limit: 1}, store.PageParams{Limit: 1}},
		"the ceiling itself is allowed":               {&leapmuxv1.PageRequest{Limit: contracts.MaxPageLimit}, store.PageParams{Limit: contracts.MaxPageLimit}},
		"one past the ceiling is capped":              {&leapmuxv1.PageRequest{Limit: contracts.MaxPageLimit + 1}, store.PageParams{Limit: contracts.MaxPageLimit}},
		"the largest wire limit is capped":            {&leapmuxv1.PageRequest{Limit: math.MaxInt32}, store.PageParams{Limit: contracts.MaxPageLimit}},
		"the most negative wire limit takes the default": {
			&leapmuxv1.PageRequest{Limit: math.MinInt32}, store.PageParams{Limit: contracts.DefaultPageLimit},
		},
		"a cursor with no limit reads a default page": {
			&leapmuxv1.PageRequest{Cursor: "opaque-cursor"},
			store.PageParams{Limit: contracts.DefaultPageLimit, Cursor: "opaque-cursor"},
		},
		"the cursor passes through unchanged": {
			&leapmuxv1.PageRequest{Limit: 10, Cursor: "opaque-cursor"},
			store.PageParams{Limit: 10, Cursor: "opaque-cursor"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, pageParams(tc.page))
		})
	}
}

func TestPageResponse(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "next", pageResponse("next").GetNextCursor())
	// The final page still carries a page message, so a client reads the end
	// of the listing from an empty cursor, not from an absent field.
	last := pageResponse("")
	assert.NotNil(t, last)
	assert.Empty(t, last.GetNextCursor())
}
