package cmd

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestListMessagesPageRequest covers the --anchor / --cursor-seq mapping onto
// MessagePageAnchor and the cursor-presence validation.
func TestListMessagesPageRequest(t *testing.T) {
	t.Run("default (empty anchor) is the latest page", func(t *testing.T) {
		req, err := listMessagesPageRequest("a-1", "", 0, 5)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, req.GetAnchor())
		assert.Zero(t, req.GetCursorSeq())
		assert.Equal(t, "a-1", req.GetAgentId())
		assert.Equal(t, int32(5), req.GetLimit())
	})

	t.Run("--anchor latest is the latest page", func(t *testing.T) {
		req, err := listMessagesPageRequest("a-1", "latest", 0, 5)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, req.GetAnchor())
	})

	t.Run("--anchor oldest selects the earliest page", func(t *testing.T) {
		req, err := listMessagesPageRequest("a-1", "oldest", 0, 5)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, req.GetAnchor())
		assert.Zero(t, req.GetCursorSeq())
	})

	t.Run("--anchor before --cursor-seq selects an older page", func(t *testing.T) {
		req, err := listMessagesPageRequest("a-1", "before", 42, 5)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE, req.GetAnchor())
		assert.Equal(t, int64(42), req.GetCursorSeq())
	})

	t.Run("--anchor after --cursor-seq selects a newer page", func(t *testing.T) {
		req, err := listMessagesPageRequest("a-1", "after", 42, 5)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, req.GetAnchor())
		assert.Equal(t, int64(42), req.GetCursorSeq())
	})

	t.Run("anchor parsing is case-insensitive and trims whitespace", func(t *testing.T) {
		req, err := listMessagesPageRequest("a-1", "  OLDEST ", 0, 5)
		require.NoError(t, err)
		assert.Equal(t, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, req.GetAnchor())
	})

	t.Run("an unknown anchor is rejected", func(t *testing.T) {
		_, err := listMessagesPageRequest("a-1", "sideways", 0, 5)
		require.Error(t, err)
	})

	t.Run("before/after require a positive cursor", func(t *testing.T) {
		_, err := listMessagesPageRequest("a-1", "before", 0, 5)
		require.Error(t, err)
		_, err = listMessagesPageRequest("a-1", "after", 0, 5)
		require.Error(t, err)
	})

	t.Run("latest/oldest reject a stray cursor rather than ignoring it", func(t *testing.T) {
		_, err := listMessagesPageRequest("a-1", "latest", 42, 5)
		require.Error(t, err)
		_, err = listMessagesPageRequest("a-1", "oldest", 42, 5)
		require.Error(t, err)
	})

	t.Run("a negative cursor is rejected, not silently coerced to latest", func(t *testing.T) {
		_, err := listMessagesPageRequest("a-1", "before", -1, 5)
		require.Error(t, err)
		_, err = listMessagesPageRequest("a-1", "latest", -5, 5)
		require.Error(t, err)
	})

	t.Run("an over-range positive limit saturates instead of wrapping past the hub cap", func(t *testing.T) {
		// int32(math.MaxInt32+1) would wrap to a small/negative value that could
		// slip past the hub's <=50 clamp; saturating keeps it out of [1,50].
		req, err := listMessagesPageRequest("a-1", "latest", 0, math.MaxInt32+1)
		require.NoError(t, err)
		assert.Equal(t, int32(math.MaxInt32), req.GetLimit())
	})

	t.Run("a non-positive limit is rejected, not silently clamped to the hub default", func(t *testing.T) {
		// A zero/negative --limit would be silently clamped to 50 by the hub;
		// reject it loudly so a typo fails the same way --cursor-seq and --anchor do.
		_, err := listMessagesPageRequest("a-1", "latest", 0, 0)
		require.Error(t, err)
		_, err = listMessagesPageRequest("a-1", "latest", 0, -5)
		require.Error(t, err)
		_, err = listMessagesPageRequest("a-1", "latest", 0, math.MinInt32-1)
		require.Error(t, err)
	})
}

// TestClampInt32 covers the saturating int->int32 conversion guarding the wire limit.
func TestClampInt32(t *testing.T) {
	assert.Equal(t, int32(5), clampInt32(5))
	assert.Equal(t, int32(0), clampInt32(0))
	assert.Equal(t, int32(-3), clampInt32(-3))
	assert.Equal(t, int32(math.MaxInt32), clampInt32(math.MaxInt32))
	assert.Equal(t, int32(math.MaxInt32), clampInt32(math.MaxInt32+1))
	assert.Equal(t, int32(math.MinInt32), clampInt32(math.MinInt32-1))
}

// TestFollowSelectorError covers the rejection of --follow combined with a
// backward/historical anchor (which previously replayed all history).
func TestFollowSelectorError(t *testing.T) {
	t.Run("--follow with the latest or after anchor is allowed", func(t *testing.T) {
		require.NoError(t, followSelectorError(true, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST))
		require.NoError(t, followSelectorError(true, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER))
	})

	t.Run("--follow with the oldest anchor is rejected", func(t *testing.T) {
		require.Error(t, followSelectorError(true, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST))
	})

	t.Run("--follow with the before anchor is rejected", func(t *testing.T) {
		require.Error(t, followSelectorError(true, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE))
	})

	t.Run("backward anchors without --follow are allowed", func(t *testing.T) {
		require.NoError(t, followSelectorError(false, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST))
		require.NoError(t, followSelectorError(false, leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE))
	})
}

// TestResumeCursorFor covers where the live tail resumes after page 1: the last
// message shown wins; an empty page honors an explicit AFTER cursor, else falls
// back to the response's authoritative latest_seq (never a spurious 0 that would
// drive the OLDEST full-history drain on a populated agent).
func TestResumeCursorFor(t *testing.T) {
	msgs := func(seqs ...int64) []*leapmuxv1.AgentChatMessage {
		out := make([]*leapmuxv1.AgentChatMessage, 0, len(seqs))
		for _, s := range seqs {
			out = append(out, &leapmuxv1.AgentChatMessage{Seq: s})
		}
		return out
	}
	req := func(anchor leapmuxv1.MessagePageAnchor, cursor int64) *leapmuxv1.ListAgentMessagesRequest {
		return &leapmuxv1.ListAgentMessagesRequest{Anchor: anchor, CursorSeq: cursor}
	}

	t.Run("non-empty page resumes after the last message, ignoring the anchor/cursor/latest_seq", func(t *testing.T) {
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, 5), msgs(10, 20, 30), 99)
		assert.Equal(t, int64(30), got)
	})

	t.Run("empty page with --anchor after resumes from the explicit cursor (over latest_seq)", func(t *testing.T) {
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, 42), nil, 99)
		assert.Equal(t, int64(42), got)
	})

	t.Run("empty latest page on a POPULATED agent resumes after latest_seq-1 (catches the raced tail, no OLDEST flood)", func(t *testing.T) {
		// A transient-empty page with a positive latest_seq is the page-read/tail-read
		// race: the agent's first/only message committed between the two reads. Resume
		// after latest_seq-1 so seq >= latest_seq is delivered (the message isn't
		// skipped), while staying a positive resume cursor (no OLDEST full drain).
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, 0), nil, 137)
		assert.Equal(t, int64(136), got)
	})

	t.Run("empty latest page on a truly-empty agent tails from now (latest_seq 0)", func(t *testing.T) {
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, 0), nil, 0)
		assert.Zero(t, got)
	})

	t.Run("a first-ever message at seq 1 (empty page, latest_seq 1) drops to the oldest drain of that single message", func(t *testing.T) {
		// latest_seq-1 == 0 -> the oldest drain, but that is exactly the one message
		// the empty page missed, not a history flood.
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, 0), nil, 1)
		assert.Zero(t, got)
	})

	t.Run("empty oldest page resumes after latest_seq-1 even with a stray cursor", func(t *testing.T) {
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST, 7), nil, 50)
		assert.Equal(t, int64(49), got)
	})

	t.Run("indeterminate latest_seq (-1) falls back to the request cursor, NOT the bogus tail", func(t *testing.T) {
		// The worker degrades latest_seq to -1 when it can't read the tail (a DB error).
		// The CLI must NOT resume after -1 (drainFetch treats afterSeq <= 0 as the OLDEST
		// full-history drain); it falls back to the request's own cursor instead.
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER, 88), nil, -1)
		assert.Equal(t, int64(88), got)
	})

	t.Run("indeterminate latest_seq (-1) on a latest page falls back to the request cursor", func(t *testing.T) {
		got := resumeCursorFor(req(leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST, 0), nil, -1)
		assert.Zero(t, got)
	})
}
