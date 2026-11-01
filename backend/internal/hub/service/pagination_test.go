package service

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"

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
		"an absent page reads the first default page": {nil, store.PageParams{Limit: DefaultPageLimit}},
		"a zero limit takes the default":              {&leapmuxv1.PageRequest{}, store.PageParams{Limit: DefaultPageLimit}},
		"a negative limit takes the default":          {&leapmuxv1.PageRequest{Limit: -5}, store.PageParams{Limit: DefaultPageLimit}},
		"one row is a legal page":                     {&leapmuxv1.PageRequest{Limit: 1}, store.PageParams{Limit: 1}},
		"the ceiling itself is allowed":               {&leapmuxv1.PageRequest{Limit: MaxPageLimit}, store.PageParams{Limit: MaxPageLimit}},
		"one past the ceiling is capped":              {&leapmuxv1.PageRequest{Limit: MaxPageLimit + 1}, store.PageParams{Limit: MaxPageLimit}},
		"the largest wire limit is capped":            {&leapmuxv1.PageRequest{Limit: math.MaxInt32}, store.PageParams{Limit: MaxPageLimit}},
		"the most negative wire limit takes the default": {
			&leapmuxv1.PageRequest{Limit: math.MinInt32}, store.PageParams{Limit: DefaultPageLimit},
		},
		"a cursor with no limit reads a default page": {
			&leapmuxv1.PageRequest{Cursor: "opaque-cursor"},
			store.PageParams{Limit: DefaultPageLimit, Cursor: "opaque-cursor"},
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

// TestPageListError pins the one mapping from a listing failure to a Connect
// code: a cursor the store could not parse is the caller's input, and every
// other failure is a hub fault.
func TestPageListError(t *testing.T) {
	t.Parallel()

	badCursor := pageListError(fmt.Errorf("decode: %w", store.ErrInvalidCursor), "list things")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(badCursor))
	assert.ErrorIs(t, badCursor, store.ErrInvalidCursor, "the cause stays inspectable")
	assert.Contains(t, badCursor.Error(), "list things")
	assert.Contains(t, badCursor.Error(), "omit it to start from the first page",
		"the message states how to recover")

	cause := errors.New("disk full")
	fault := pageListError(cause, "list things")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(fault))
	assert.ErrorIs(t, fault, cause, "the hub fault keeps its cause")
	assert.Contains(t, fault.Error(), "list things", "the message identifies the listing")
	assert.NotContains(t, fault.Error(), "omit it to start from the first page",
		"a hub fault does not tell the caller to change the cursor")

	// storeConnectError answers a bad cursor with the same code, so the admin
	// handlers and the other listings agree.
	assert.Equal(t, connect.CodeInvalidArgument,
		connect.CodeOf(storeConnectError(fmt.Errorf("decode: %w", store.ErrInvalidCursor), "list users")))
}
