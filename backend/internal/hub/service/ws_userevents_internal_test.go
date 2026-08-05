package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The connect's query parameters are the one part of a 300-line handler that is
// a pure function of the request, and the two rejections in it are contract:
// each is a client bug reported as a 400 rather than silently degraded to a
// full-snapshot reconnect, because degradation masks the bug and lets a broken
// client limp along forever.
//
// Reaching them through the handler means httptest.NewServer plus a real
// websocket.Dial per case; as a function each is two lines.
func TestParseUserEventsRequest(t *testing.T) {
	t.Parallel()

	parse := func(t *testing.T, query string) (userEventsRequest, error) {
		t.Helper()
		return parseUserEventsRequest(httptest.NewRequest("GET", "/ws/userevents?"+query, nil))
	}
	cursor := channelwire.EncodeResumeHLC(&leapmuxv1.HLC{
		Physical: time.Now().UnixMilli(), Logical: 3, ClientId: "c1",
	})

	// A first connect: no filter, no cursor. SubscribeWithACL reads the nil
	// cursor as FALLBACK and answers with a full snapshot.
	t.Run("a bare connect narrows nothing and resumes nothing", func(t *testing.T) {
		t.Parallel()
		req, err := parse(t, "")
		require.NoError(t, err)
		assert.Empty(t, req.workspaceIDs)
		assert.Nil(t, req.requested, "an unnarrowed filter must be nil, which is what the manager reads as 'no narrowing'")
		assert.Nil(t, req.resumeCursor)
		assert.Zero(t, req.resumeEpoch)
	})

	t.Run("a narrowed connect carries the filter in both shapes", func(t *testing.T) {
		t.Parallel()
		// Whitespace and empty segments are the shape a client's join produces
		// when one of its ids is missing; they must not become filter entries
		// that match no workspace and silently narrow the subscription to
		// nothing.
		req, err := parse(t, "workspace_ids=ws1,%20ws2%20,,ws3")
		require.NoError(t, err)
		assert.Equal(t, []string{"ws1", "ws2", "ws3"}, req.workspaceIDs)
		assert.Equal(t, map[string]bool{"ws1": true, "ws2": true, "ws3": true}, req.requested)
	})

	t.Run("a resume carries its cursor and epoch", func(t *testing.T) {
		t.Parallel()
		req, err := parse(t, "resume_after_hlc="+cursor+"&resume_epoch=7")
		require.NoError(t, err)
		require.NotNil(t, req.resumeCursor)
		assert.Equal(t, int64(3), req.resumeCursor.GetLogical())
		assert.Equal(t, int64(7), req.resumeEpoch)
		assert.Nil(t, req.requested)
	})

	// A malformed cursor is a genuine client bug, not a legacy client.
	t.Run("a malformed cursor is refused rather than degraded", func(t *testing.T) {
		t.Parallel()
		_, err := parse(t, "resume_after_hlc=not-a-cursor&resume_epoch=1")
		assert.Error(t, err, "a broken cursor must not quietly become a full-snapshot reconnect")
	})

	// An epoch alone says nothing to resume from, so the pairing is malformed.
	t.Run("an epoch without a cursor is refused", func(t *testing.T) {
		t.Parallel()
		_, err := parse(t, "resume_epoch=1")
		assert.Error(t, err)
	})

	// The narrow-mint / wide-replay invariant, enforced here rather than in one
	// client out of three: the persisted cursor is per-USER and cross-TAB, so
	// one minted under a narrow filter can miss ops when replayed under a wider
	// one.
	t.Run("a cursor under a narrowed filter is refused", func(t *testing.T) {
		t.Parallel()
		_, err := parse(t, "workspace_ids=ws1&resume_after_hlc="+cursor+"&resume_epoch=1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workspace_ids",
			"the message has to name the pairing, or the client author cannot act on the 400")

		// The narrowing alone is still perfectly legal: the guard is on the
		// pairing, not on narrowing.
		req, err := parse(t, "workspace_ids=ws1")
		require.NoError(t, err)
		assert.Equal(t, []string{"ws1"}, req.workspaceIDs)
	})
}
