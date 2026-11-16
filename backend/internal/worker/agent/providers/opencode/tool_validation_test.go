package opencode

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeToolMalformedStartsDoNotCreateRecords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ kind, fields string }{
		{"tool.input.started", `"assistantMessageID":"msg_1","id":"call_1"`},
		{"tool.input.ended", `"assistantMessageID":"msg_1","id":"call_1"`},
		{"tool.progress", `"assistantMessageID":"msg_1","id":"call_1"`},
		{"tool.unknown", `"assistantMessageID":"msg_1","id":"call_1"`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			streams := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(&agenttest.Sink{}))
			_, err := streams.observe(openCodeSessionEventForTest(t, tc.kind, tc.fields))
			require.Error(t, err)
			assert.Empty(t, streams.tools)
		})
	}
}

func TestOpenCodeToolMalformedUpdatesKeepTheLastValidRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, kind, fields string }{
		{"ended without text", "tool.input.ended", ``},
		{"called without executed", "tool.called", `,"input":{}`},
		{"called with array input", "tool.called", `,"input":[],"executed":false`},
		{"progress without metadata", "tool.progress", ``},
		{"progress with null metadata", "tool.progress", `,"metadata":null`},
		{"progress with array metadata", "tool.progress", `,"metadata":[]`},
		{"success without content", "tool.success", `,"executed":false`},
		{"success with empty content", "tool.success", `,"content":[],"executed":false`},
		{"success without executed", "tool.success", `,"content":[{"type":"text","text":"done"}]`},
		{"failure without error", "tool.failed", `,"executed":false`},
		{"failure with null error", "tool.failed", `,"error":null,"executed":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			sink.UpdateSessionID("ses_1")
			streams := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
			_, err := streams.observe(openCodeSessionEventForTest(t, "tool.input.started", `"assistantMessageID":"msg_1","id":"call_1","name":"shell"`))
			require.NoError(t, err)
			called := openCodeSessionEventForTest(t, "tool.called", `"assistantMessageID":"msg_1","id":"call_1","input":{"command":"held"},"executed":false`)
			_, err = streams.observe(called)
			require.NoError(t, err)
			_, err = streams.observe(openCodeSessionEventForTest(t, tc.kind, `"assistantMessageID":"msg_1","id":"call_1"`+tc.fields))
			require.Error(t, err)
			state := streams.tools[openCodeToolIdentity{messageID: "msg_1", callID: "call_1"}]
			require.NotNil(t, state)
			assert.Equal(t, string(called.Raw), string(state.latest))
			assert.False(t, state.closed)
			require.Len(t, sink.Messages(), 1)
		})
	}
}

func TestOpenCodeToolRecoveryRejectsMalformedStatesBeforeOpeningSpans(t *testing.T) {
	t.Parallel()
	for _, state := range []string{
		`{"status":"streaming"}`,
		`{"status":"streaming","input":{}}`,
		`{"status":"running","metadata":{}}`,
		`{"status":"running","input":[],"metadata":{}}`,
		`{"status":"running","input":{}}`,
		`{"status":"running","input":{},"metadata":null}`,
		`{"status":"completed","input":{}}`,
		`{"status":"completed","input":{},"content":[]}`,
		`{"status":"error","input":{}}`,
		`{"status":"error","input":{},"error":null}`,
		`{"status":"unknown","input":{}}`,
	} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			sink.UpdateSessionID("ses_1")
			streams := newOpenCodeToolStreams("ses_1", agent.NewProviderServices(sink))
			raw := fmt.Sprintf(`{"id":"msg_1","type":"assistant","content":[{"type":"tool","id":"call_1","name":"shell","state":%s}]}`, state)
			require.Error(t, streams.recover(storedAssistant(t, raw)))
			assert.Empty(t, streams.tools)
			assert.Empty(t, sink.Messages())
		})
	}
}
