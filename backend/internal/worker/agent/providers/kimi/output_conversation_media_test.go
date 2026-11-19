package kimi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationMediaKeepsTheNativeFileAndOrderedResultOutsideDispatch(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	id, data := kimiMediaTestFile("current native image")
	entered := make(chan string, 1)
	released := make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	defer release()
	rig.agent.api = newKimiTestClient(t, func(w http.ResponseWriter, request *http.Request) {
		entered <- request.URL.Path
		<-released
		w.Header().Set("Content-Type", "image/png")
		_, err := w.Write(data)
		assert.NoError(t, err)
	})
	rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"turn-1","item":{"type":"tool_call","item_id":"read-image","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"ReadMediaFile","stage":"executing","input":{"path":"original.png"}}}`)
	result := fmt.Sprintf(`{"type":"item.ended","turn_id":"turn-1","item":%s}`, kimiMediaFixtureItem(t, "read-image", id))
	packet := kimiConversationTestFrame(t, rig.sessionID(), "main", result)
	dispatched := make(chan struct{})
	go func() {
		rig.agent.HandleOutput(packet)
		close(dispatched)
	}()
	assert.Equal(t, "/api/v1/sessions/session_1/media/"+id, awaitKimiMediaRequest(t, entered))
	select {
	case <-dispatched:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the media read held native event dispatch")
	}
	require.Len(t, rig.sink.Messages(), 1, "the closing result waits at its reserved position")
	rig.agent.HandleOutput([]byte(`{"type":"interaction","session_id":"session_1","agent_id":"main","interaction_id":"question_1","kind":"question","status":"pending","tool_call_id":"question-call","event_created_at":"2026-11-18T00:00:00Z","request":{"questions":[{"id":"q_0","question":"Continue?","options":[{"id":"yes","label":"Yes"}]}]}}`))
	assert.Equal(t, 1, rig.sink.PublishedControlCount())
	rig.conversationFrame(t, `{"type":"turn.ended","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
	assert.Len(t, rig.sink.Messages(), 1, "the divider cannot overtake the reserved image result")
	release()
	waitFor(t, func() bool { return len(rig.sink.Messages()) == 3 }, "the image result and following divider persist")
	rows := rig.sink.Messages()
	view, err := decodeKimiConversationRecord(rows[1].Content)
	require.NoError(t, err)
	assert.Equal(t, id, view.item.Media[0].FileID)
	assert.NotContains(t, string(rows[1].Content), base64.StdEncoding.EncodeToString(data))
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rows[1].SupplementalContent, &supplement))
	assert.Contains(t, string(supplement["kimi_media"]), id)
	assert.Contains(t, string(supplement["kimi_media"]), base64.StdEncoding.EncodeToString(data))
	assert.True(t, rows[2].TurnEnd)
}
