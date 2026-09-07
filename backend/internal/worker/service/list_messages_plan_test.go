package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestResolveMessagePage covers the pure anchor -> query-plan routing and the
// cursor/limit clamps without a DB, complementing the DB-integration coverage in
// TestListAgentMessages_AnchorPaging.
func TestResolveMessagePage(t *testing.T) {
	t.Parallel()

	const (
		latest = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST
		oldest = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST
		before = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE
		after  = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
		unspec = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_UNSPECIFIED
	)

	cases := []struct {
		name      string
		anchor    leapmuxv1.MessagePageAnchor
		cursorSeq int64
		limit     int64
		want      messagePagePlan
		// wantReverse asserts the derived reverse-direction (mode.descending()), so the
		// mode->reverse mapping stays covered now that the bool isn't stored on the plan.
		wantReverse bool
	}{
		{
			name:   "latest reverses, ignores cursor",
			anchor: latest, cursorSeq: 42, limit: 10,
			want: messagePagePlan{mode: messagePageLatest, bound: 0, limit: 10}, wantReverse: true,
		},
		{
			name:   "unspecified resolves to latest",
			anchor: unspec, cursorSeq: 0, limit: 10,
			want: messagePagePlan{mode: messagePageLatest, bound: 0, limit: 10}, wantReverse: true,
		},
		{
			name:   "unknown anchor resolves to latest",
			anchor: leapmuxv1.MessagePageAnchor(999), cursorSeq: 0, limit: 10,
			want: messagePagePlan{mode: messagePageLatest, bound: 0, limit: 10}, wantReverse: true,
		},
		{
			name:   "oldest scans ascending from 0, ignores cursor",
			anchor: oldest, cursorSeq: 99, limit: 10,
			want: messagePagePlan{mode: messagePageAscending, bound: 0, limit: 10}, wantReverse: false,
		},
		{
			name:   "after scans ascending from cursor",
			anchor: after, cursorSeq: 7, limit: 10,
			want: messagePagePlan{mode: messagePageAscending, bound: 7, limit: 10}, wantReverse: false,
		},
		{
			name:   "before scans descending from cursor and reverses",
			anchor: before, cursorSeq: 7, limit: 10,
			want: messagePagePlan{mode: messagePageBefore, bound: 7, limit: 10}, wantReverse: true,
		},
		{
			name:   "negative cursor clamps to 0 (before)",
			anchor: before, cursorSeq: -5, limit: 10,
			want: messagePagePlan{mode: messagePageBefore, bound: 0, limit: 10}, wantReverse: true,
		},
		{
			name:   "negative cursor clamps to 0 (after)",
			anchor: after, cursorSeq: -1, limit: 10,
			want: messagePagePlan{mode: messagePageAscending, bound: 0, limit: 10}, wantReverse: false,
		},
		{
			name:   "zero limit clamps to the page limit",
			anchor: latest, cursorSeq: 0, limit: 0,
			want: messagePagePlan{mode: messagePageLatest, bound: 0, limit: contracts.MessagePageLimit}, wantReverse: true,
		},
		{
			name:   "negative limit clamps to the page limit",
			anchor: after, cursorSeq: 3, limit: -10,
			want: messagePagePlan{mode: messagePageAscending, bound: 3, limit: contracts.MessagePageLimit}, wantReverse: false,
		},
		{
			name:   "over-cap limit clamps to the page limit",
			anchor: after, cursorSeq: 3, limit: 1000,
			want: messagePagePlan{mode: messagePageAscending, bound: 3, limit: contracts.MessagePageLimit}, wantReverse: false,
		},
		{
			name:   "limit at the cap is preserved",
			anchor: after, cursorSeq: 3, limit: contracts.MessagePageLimit,
			want: messagePagePlan{mode: messagePageAscending, bound: 3, limit: contracts.MessagePageLimit}, wantReverse: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMessagePage(tc.anchor, tc.cursorSeq, tc.limit)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantReverse, got.mode.descending())
		})
	}
}

// TestReplayPageAnchor covers the WatchEvents resume -> MessagePageAnchor routing:
// AFTER_CURSOR with a positive cursor pages forward (AFTER); everything else replays
// the LATEST page, including a malformed AFTER_CURSOR whose cursor is non-positive
// (which must NOT become AFTER, or resolveMessagePage would return the OLDEST page and
// splice a gap in front of the latest window).
func TestReplayPageAnchor(t *testing.T) {
	t.Parallel()

	const (
		latest  = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST
		after   = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
		mLatest = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST
		mAfter  = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR
		mCapped = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE
		mUnspec = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_UNSPECIFIED
	)
	assert.Equal(t, after, replayPageAnchor(mAfter, 5), "AFTER_CURSOR with a positive cursor pages forward")
	assert.Equal(t, after, replayPageAnchor(mCapped, 5), "AFTER_CURSOR_OR_NONE with a positive cursor pages forward")
	assert.Equal(t, latest, replayPageAnchor(mAfter, 0), "AFTER_CURSOR with cursor 0 falls back to LATEST")
	assert.Equal(t, latest, replayPageAnchor(mCapped, 0), "AFTER_CURSOR_OR_NONE with cursor 0 falls back to LATEST")
	assert.Equal(t, latest, replayPageAnchor(mAfter, -1), "AFTER_CURSOR with a negative cursor falls back to LATEST")
	assert.Equal(t, latest, replayPageAnchor(mLatest, 99), "LATEST ignores the cursor")
	assert.Equal(t, latest, replayPageAnchor(mUnspec, 99), "UNSPECIFIED defaults to LATEST")
}

func TestShouldSkipCatchUpReplay(t *testing.T) {
	t.Parallel()

	farTail := int64(10 + contracts.CatchUpGapLimit + 1)
	atLimitTail := int64(10 + contracts.CatchUpGapLimit)
	emptyTail := int64(0)
	// A resume cursor that LEADS the loaded window tail (frames observed but
	// dropped): the declared tail is the gap base, not the cursor.
	leadingCursor := int64(40)
	windowTail := int64(10)
	cases := []struct {
		name        string
		replay      leapmuxv1.WatchReplayMode
		cursorSeq   int64
		skipGapBase int64
		latestSeq   *int64
		want        bool
	}{
		{"unspecified mode", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_UNSPECIFIED, 10, 10, &farTail, false},
		{"latest mode", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, 10, 10, &farTail, false},
		{"after cursor mode", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, 10, 10, &farTail, false},
		{"capped mode with cursor zero", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, 0, 10, &farTail, false},
		{"capped mode with nil latest seq", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, 10, 10, nil, false},
		{"capped mode with empty latest seq", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, 10, 10, &emptyTail, false},
		{"capped mode at the limit", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, 10, 10, &atLimitTail, false},
		{"capped mode one past the limit", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, 10, 10, &farTail, true},
		{"capped mode skips on a window tail the cursor leads", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, leadingCursor, windowTail, &farTail, true},
		{"capped mode replays when the window-tail gap fits", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, leadingCursor, windowTail, &atLimitTail, false},
		{"capped mode with a declared empty window skips past the limit", leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR_OR_NONE, 5, 0, &farTail, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, shouldSkipCatchUpReplay(tc.replay, tc.cursorSeq, tc.skipGapBase, tc.latestSeq))
		})
	}
}
