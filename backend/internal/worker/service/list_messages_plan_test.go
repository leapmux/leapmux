package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestResolveMessagePage covers the pure anchor -> query-plan routing and the
// cursor/limit clamps without a DB, complementing the DB-integration coverage in
// TestListAgentMessages_AnchorPaging.
func TestResolveMessagePage(t *testing.T) {
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
			name:   "zero limit clamps to 50",
			anchor: latest, cursorSeq: 0, limit: 0,
			want: messagePagePlan{mode: messagePageLatest, bound: 0, limit: 50}, wantReverse: true,
		},
		{
			name:   "negative limit clamps to 50",
			anchor: after, cursorSeq: 3, limit: -10,
			want: messagePagePlan{mode: messagePageAscending, bound: 3, limit: 50}, wantReverse: false,
		},
		{
			name:   "over-cap limit clamps to 50",
			anchor: after, cursorSeq: 3, limit: 1000,
			want: messagePagePlan{mode: messagePageAscending, bound: 3, limit: 50}, wantReverse: false,
		},
		{
			name:   "limit at the cap is preserved",
			anchor: after, cursorSeq: 3, limit: 50,
			want: messagePagePlan{mode: messagePageAscending, bound: 3, limit: 50}, wantReverse: false,
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
	const (
		latest  = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST
		after   = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
		mLatest = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST
		mAfter  = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR
		mUnspec = leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_UNSPECIFIED
	)
	assert.Equal(t, after, replayPageAnchor(mAfter, 5), "AFTER_CURSOR with a positive cursor pages forward")
	assert.Equal(t, latest, replayPageAnchor(mAfter, 0), "AFTER_CURSOR with cursor 0 falls back to LATEST")
	assert.Equal(t, latest, replayPageAnchor(mAfter, -1), "AFTER_CURSOR with a negative cursor falls back to LATEST")
	assert.Equal(t, latest, replayPageAnchor(mLatest, 99), "LATEST ignores the cursor")
	assert.Equal(t, latest, replayPageAnchor(mUnspec, 99), "UNSPECIFIED defaults to LATEST")
}
