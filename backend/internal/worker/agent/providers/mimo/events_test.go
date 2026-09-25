package mimo

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvent(t *testing.T) {
	t.Parallel()

	data := []byte(`{"type":"session.status","properties":{"sessionID":"ses_test","status":{"type":"busy"}}}`)
	original := string(data)
	event, ok := parseEvent(data)
	require.True(t, ok)
	assert.Equal(t, contracts.MiMoEventSessionStatus, event.Type)
	assert.JSONEq(t, `{"sessionID":"ses_test","status":{"type":"busy"}}`, string(event.Properties))

	// The stream reader can reuse its buffer for the next event, and a tool call
	// keeps its last update for the turn end to store. So the event keeps its own
	// copy of the bytes.
	copy(data, `{"type":"XXXXXXX`)
	assert.Equal(t, original, string(event.raw))

	for _, data := range []string{``, `not json`, `[]`, `{}`, `{"type":""}`, `{"properties":{}}`, `{"type":7}`} {
		_, ok := parseEvent([]byte(data))
		assert.False(t, ok, "data %q", data)
	}
}

// A stop that discards the output drops every event that still arrives, before
// any handler reads it.
func TestDispatchEventDropsEveryEventWhileTheOutputIsDiscarded(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.DiscardOutput()

	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "Hello.", true),
		sessionErrorEvent(t, "APIError", "rate limited"),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
	)
	assert.Empty(t, sink.TurnActives())
	assert.Empty(t, sink.Messages())
	assert.Zero(t, sink.NotificationCount())
	assert.Empty(t, a.messages, "no handler recorded the message")
	assert.False(t, a.turnActive)
}

func TestMiMoMessageInfoIsCompactionSummary(t *testing.T) {
	t.Parallel()

	for summary, want := range map[string]bool{
		`true`: true,
		// A user message states its summary as an object: it is no compaction.
		`{"title":"Fix the parser","diffs":[]}`: false,
		`false`:                                 false,
		`null`:                                  false,
		``:                                      false,
	} {
		info := mimoMessageInfo{Summary: json.RawMessage(summary)}
		assert.Equal(t, want, info.isCompactionSummary(), "summary %q", summary)
	}
}

func TestMiMoPartEnded(t *testing.T) {
	t.Parallel()

	var part mimoPart
	assert.False(t, part.ended(), "a part that states no time did not end")
	require.NoError(t, json.Unmarshal([]byte(`{"time":{"start":1}}`), &part))
	assert.False(t, part.ended(), "a part with a start and no end still streams")
	require.NoError(t, json.Unmarshal([]byte(`{"time":{"start":1,"end":2}}`), &part))
	assert.True(t, part.ended())
}

func TestMiMoToolStateFinal(t *testing.T) {
	t.Parallel()

	var state *mimoToolState
	assert.False(t, state.final(), "a call with no state has no result")
	for status, want := range map[string]bool{
		contracts.MiMoToolStatusPending:   false,
		contracts.MiMoToolStatusRunning:   false,
		contracts.MiMoToolStatusCompleted: true,
		contracts.MiMoToolStatusError:     true,
		"":                                false,
	} {
		assert.Equal(t, want, (&mimoToolState{Status: status}).final(), "status %q", status)
	}
}
