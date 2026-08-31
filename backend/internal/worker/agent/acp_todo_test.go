package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acpExtract(t *testing.T, content string) (todoevents.Event, bool) {
	t.Helper()
	return acpProvider{}.ExtractTodoEvent("", []byte(content), nil)
}

func TestACPExtractTodoEvent_PlanSnapshot(t *testing.T) {
	t.Parallel()

	ev, ok := acpExtract(t, `{
		"sessionUpdate": "plan",
		"entries": [
			{"content": "one", "status": "pending"},
			{"content": "two", "status": "completed"}
		]
	}`)
	require.True(t, ok)
	require.Equal(t, todoevents.KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "one", ev.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusPending, ev.Snapshot[0].Status)
	assert.Equal(t, todoevents.StatusCompleted, ev.Snapshot[1].Status)
}

// The pretty-printed form puts a space between the key and the value, which is why
// the two byte searches are independent rather than one key/value search.
func TestACPExtractTodoEvent_ReadsThePrettyPrintedForm(t *testing.T) {
	t.Parallel()

	ev, ok := acpExtract(t, "{\n  \"sessionUpdate\": \"plan\",\n  \"entries\": [\n    {\"content\": \"one\"}\n  ]\n}")
	require.True(t, ok)
	require.Len(t, ev.Snapshot, 1)
	assert.Equal(t, "one", ev.Snapshot[0].Content)
}

func TestACPExtractTodoEvent_EmptyPlanIsASnapshot(t *testing.T) {
	t.Parallel()

	ev, ok := acpExtract(t, `{"sessionUpdate": "plan", "entries": []}`)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, ev.Kind)
	assert.Empty(t, ev.Snapshot)
}

// The span type is not a discriminator. Every ACP handler persists its plan with no
// span today, but the plan is recognized wherever it arrives -- so a plan that later
// rides inside a tool call still reaches the sidebar, instead of returning an empty one
// with no log.
func TestACPExtractTodoEvent_ASpanTypeDoesNotSuppressAPlan(t *testing.T) {
	t.Parallel()

	ev, ok := acpProvider{}.ExtractTodoEvent("Read",
		[]byte(`{"sessionUpdate":"plan","entries":[{"content":"x","status":"pending"}]}`), nil)
	require.True(t, ok)
	require.Len(t, ev.Snapshot, 1)
	assert.Equal(t, "x", ev.Snapshot[0].Content)
}

// The exact sessionUpdate check, not the span type, is what refuses a look-alike. Each
// case here is fed with NO span type, so only that check can reject it.
func TestACPExtractTodoEvent_IgnoresEverythingElse(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"another session update": `{"sessionUpdate":"agent_message_chunk","content":{"text":"hi"}}`,
		"empty content":          ``,
		"not json":               `not json`,
		// Both markers are present and the discriminator still says something else, so
		// the byte search alone must not decide.
		"the word plan elsewhere": `{"sessionUpdate":"tool_call","title":"plan the work"}`,
		"another provider":        `{"method":"turn/plan/updated","params":{"plan":[{"step":"x"}]}}`,
		"a Claude todo write":     `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}}]}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := acpExtract(t, content)
			assert.False(t, ok)
		})
	}
}

// Every ACP provider shares the shape, so every one of them reads it.
func TestACPExtractTodoEvent_ServesEveryACPProvider(t *testing.T) {
	t.Parallel()

	const plan = `{"sessionUpdate":"plan","entries":[{"content":"one","status":"pending"}]}`
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	} {
		ev, ok := ProviderFor(provider).ExtractTodoEvent("", []byte(plan), nil)
		require.True(t, ok, "provider %s", provider)
		require.Len(t, ev.Snapshot, 1)
	}
}

// Pi's CLI states no to-do list, so its plugin reports none rather than guessing at
// another provider's shape.
func TestPiExtractTodoEvent_ReportsNothing(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		`{"sessionUpdate":"plan","entries":[{"content":"one"}]}`,
		`{"method":"turn/plan/updated","params":{"plan":[{"step":"x"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}}]}}`,
	} {
		_, ok := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI).
			ExtractTodoEvent("TodoWrite", []byte(content), nil)
		assert.False(t, ok)
	}
}
