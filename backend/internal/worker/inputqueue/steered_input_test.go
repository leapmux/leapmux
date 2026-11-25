package inputqueue

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// steerAgentMessage queues an agent message of claudeSender with text and
// attachments, starts a steerable turn, and prepares its steer. The queue row
// is then DISPATCHING, as while the provider takes the steer.
func steerAgentMessage(t *testing.T, store *Store, text string, attachments []Attachment) PreparedDispatch {
	t.Helper()
	_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	_, err = store.Enqueue(t.Context(), NewItem{
		ID: "from-agent", AgentID: "agent-1", Kind: userMessageKind, Sender: claudeSender(),
		Text: text, Attachments: attachments,
	})
	require.NoError(t, err)
	prepared, _, err := store.PrepareSteer(t.Context(), "agent-1", "from-agent", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	return *prepared
}

// While the steer is in flight, the queue row holds the input, with the title
// of its sender.
func TestStoreSteeredInputReadsTheQueueRowOfASteerInFlight(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	steerAgentMessage(t, store, "Run the tests", nil)

	steered, err := store.SteeredInput(t.Context(), "agent-1", "from-agent")
	require.NoError(t, err)
	assert.Equal(t, SteeredInput{Sender: claudeSender(), Text: "Run the tests"}, steered)
}

// A steer that waits for its echo is in flight too.
func TestStoreSteeredInputReadsTheQueueRowOfASteerThatAwaitsItsEcho(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "echo-1")

	steered, err := store.SteeredInput(t.Context(), "agent-1", "echo-1")
	require.NoError(t, err)
	assert.Equal(t, SteeredInput{Text: "echo-1"}, steered)
}

// Once Accept moved the input to its transcript row, the row holds it: the
// sender's tab and provider, without a title, and the sender's text.
func TestStoreSteeredInputReadsTheTranscriptRowOfAnAcceptedSteer(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		text        string
		attachments []Attachment
	}{
		{name: "a message with text", text: "Run the tests"},
		{name: "a message of attachments alone", attachments: []Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("body")}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			prepared := steerAgentMessage(t, store, test.text, test.attachments)
			_, _, err := store.Accept(t.Context(), prepared, DispatchResult{StartsTurn: true, Steering: true})
			require.NoError(t, err)

			steered, err := store.SteeredInput(t.Context(), "agent-1", "from-agent")
			require.NoError(t, err)
			assert.Equal(t, SteeredInput{
				Sender: &AgentSender{TabID: "sender-tab", Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE},
				Text:   test.text,
			}, steered)
		})
	}
}

// A user message states no sender in either place.
func TestStoreSteeredInputStatesNoSenderForAUserMessage(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()
	_, _, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "typed", AgentID: "agent-1", Kind: userMessageKind, Text: "Also check the tests."})
	require.NoError(t, err)
	prepared, _, err := store.PrepareSteer(ctx, "agent-1", "typed", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)

	inFlight, err := store.SteeredInput(ctx, "agent-1", "typed")
	require.NoError(t, err)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, Steering: true})
	require.NoError(t, err)
	accepted, err := store.SteeredInput(ctx, "agent-1", "typed")
	require.NoError(t, err)

	want := SteeredInput{Text: "Also check the tests."}
	assert.Equal(t, want, inFlight)
	assert.Equal(t, want, accepted)
}

// The queue keeps an input that it holds for a delivery of its own, and the
// row of an input that the user removed.
func TestStoreSteeredInputHoldsAnInputThatTheQueueKeeps(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()
	for _, id := range []string{"queued", "removed"} {
		_, err := store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: userMessageKind, Sender: claudeSender(), Text: id})
		require.NoError(t, err)
	}
	_, err := store.Delete(ctx, "agent-1", "removed")
	require.NoError(t, err)

	for _, id := range []string{"queued", "removed"} {
		steered, err := store.SteeredInput(ctx, "agent-1", id)
		require.NoError(t, err)
		assert.Equal(t, SteeredInput{Held: true}, steered, id)
	}
}

// An input that neither place holds is not found. A message of another source,
// or of another agent, is never an input of this agent.
func TestStoreSteeredInputFindsNoInputThatNoPlaceHolds(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	var seq int64
	require.NoError(t, database.QueryRowContext(ctx, `
		UPDATE agents SET message_seq_hwm = message_seq_hwm + 1
		WHERE id = 'agent-1' RETURNING message_seq_hwm`).Scan(&seq))
	_, err := database.ExecContext(ctx, `
		INSERT INTO messages (id, agent_id, seq, source, content, content_compression)
		VALUES ('agent-reply', 'agent-1', ?, ?, '{"content":"done"}', ?)`,
		seq, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE)
	require.NoError(t, err)
	require.NoError(t, database.QueryRowContext(ctx, `
		UPDATE agents SET message_seq_hwm = message_seq_hwm + 1
		WHERE id = 'agent-1' RETURNING message_seq_hwm`).Scan(&seq))
	_, err = database.ExecContext(ctx, `
		INSERT INTO messages (id, agent_id, seq, source, content, content_compression, from_input_queue)
		VALUES ('provider-user', 'agent-1', ?, ?, '{"content":"recovered"}', ?, 0)`,
		seq, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE)
	require.NoError(t, err)
	prepared := steerAgentMessage(t, store, "Run the tests", nil)
	_, _, err = store.Accept(ctx, prepared, DispatchResult{StartsTurn: true, Steering: true})
	require.NoError(t, err)

	for _, test := range []struct{ name, agentID, inputID string }{
		{name: "an unknown input", agentID: "agent-1", inputID: "unknown"},
		{name: "a message of the agent", agentID: "agent-1", inputID: "agent-reply"},
		{name: "a provider user row not from the input queue", agentID: "agent-1", inputID: "provider-user"},
		{name: "an input of another agent", agentID: "agent-2", inputID: "from-agent"},
	} {
		_, err := store.SteeredInput(ctx, test.agentID, test.inputID)
		assert.ErrorIs(t, err, ErrNotFound, test.name)
	}
}

func TestStoreSteeredInputRefusesAnEmptyIdentity(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	for _, ids := range [][2]string{{"", "from-agent"}, {"agent-1", ""}} {
		_, err := store.SteeredInput(t.Context(), ids[0], ids[1])
		assert.ErrorIs(t, err, ErrInvalidInput, ids)
	}
}

func TestManagerSteeredInputRefusesAfterTheManagerStopped(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	manager := NewManager(store, nil, nil)
	<-manager.Stop()
	_, err := manager.SteeredInput(context.Background(), "agent-1", "from-agent")
	assert.ErrorIs(t, err, ErrManagerStopped)
}

// transcriptText reads what transcriptContent writes, and an empty text is a
// text, not an absent one.
func TestTranscriptTextReadsWhatTranscriptContentWrites(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"Run the tests", ""} {
		content, err := transcriptContent(StoredItem{Text: text}, []Attachment{{Filename: "notes.txt", MimeType: "text/plain"}})
		require.NoError(t, err)
		got, err := transcriptText(content)
		require.NoError(t, err)
		assert.Equal(t, text, got)
	}
	_, err := transcriptText([]byte(`{"attachments":[]}`))
	require.ErrorContains(t, err, "holds no text")
	_, err = transcriptText([]byte(`not json`))
	require.Error(t, err)
}

// agentMessageSenderOf reads what withAgentMessageSender writes. It never
// returns a sender that the queue could not store.
func TestAgentMessageSenderOfReadsWhatWithAgentMessageSenderWrites(t *testing.T) {
	t.Parallel()
	supplement, err := withAgentMessageSender(nil, claudeSender())
	require.NoError(t, err)
	sender, err := agentMessageSenderOf(supplement)
	require.NoError(t, err)
	assert.Equal(t, &AgentSender{TabID: "sender-tab", Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}, sender,
		"the row states no title")

	sender, err = agentMessageSenderOf(nil)
	require.NoError(t, err)
	assert.Nil(t, sender, "a row without a sender states none")

	for name, member := range map[string]string{
		"no tab":      `{"provider":"AGENT_PROVIDER_CLAUDE_CODE"}`,
		"no provider": `{"tabId":"sender-tab"}`,
		"not a proto": `{"tabId":7}`,
	} {
		supplement := []byte(`{"` + contracts.MessageSupplementFieldMetadata + `":{"` + contracts.MessageMetadataFieldAgentMessageSender + `":` + member + `}}`)
		_, err := agentMessageSenderOf(supplement)
		assert.Error(t, err, name)
	}
}
