package agent

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexExtract(t *testing.T, content string) (todoevents.Event, bool) {
	t.Helper()
	return codexProvider{}.ExtractTodoEvent("", []byte(content), nil)
}

func TestCodexExtractTodoEvent_PlanSnapshot(t *testing.T) {
	t.Parallel()

	ev, ok := codexExtract(t, `{
		"method": "turn/plan/updated",
		"params": {"plan": [
			{"step": "Investigate", "status": "in_progress"},
			{"step": "Write tests", "status": "pending"}
		]}
	}`)
	require.True(t, ok)
	require.Equal(t, todoevents.KindSnapshot, ev.Kind)
	require.Len(t, ev.Snapshot, 2)
	assert.Equal(t, "Investigate", ev.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusInProgress, ev.Snapshot[0].Status)
	assert.Equal(t, "Investigate", ev.Snapshot[0].ActiveForm,
		"Codex states no separate active form, and the step reads as one")
	assert.Equal(t, todoevents.StatusPending, ev.Snapshot[1].Status)
}

// Codex spells its in-progress status in camelCase, unlike every other provider.
func TestCodexExtractTodoEvent_ReadsTheCamelCaseStatus(t *testing.T) {
	t.Parallel()

	ev, ok := codexExtract(t, `{"method":"turn/plan/updated","params":{"plan":[{"step":"x","status":"inProgress"}]}}`)
	require.True(t, ok)
	require.Len(t, ev.Snapshot, 1)
	assert.Equal(t, todoevents.StatusInProgress, ev.Snapshot[0].Status)
}

// Codex emits a step with no text while it is still writing the plan; an empty row
// would render as a blank line in the sidebar.
func TestCodexExtractTodoEvent_DropsAStepWithNoText(t *testing.T) {
	t.Parallel()

	ev, ok := codexExtract(t, `{
		"method": "turn/plan/updated",
		"params": {"plan": [
			{"step": "", "status": "in_progress"},
			{"step": "real", "status": "pending"}
		]}
	}`)
	require.True(t, ok)
	require.Len(t, ev.Snapshot, 1)
	assert.Equal(t, "real", ev.Snapshot[0].Content)
}

// An emptied plan is a real snapshot: the turn has no steps left, and reporting
// nothing would leave the sidebar showing the previous turn's work.
func TestCodexExtractTodoEvent_EmptyPlanIsASnapshot(t *testing.T) {
	t.Parallel()

	ev, ok := codexExtract(t, `{"method":"turn/plan/updated","params":{"plan":[]}}`)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, ev.Kind)
	assert.Empty(t, ev.Snapshot)
}

// The span type is not a discriminator. Codex persists its plan with no span today,
// but the plan is recognized wherever it arrives -- so a plan that later rides inside a
// tool call still reaches the sidebar, instead of returning an empty one with no log.
func TestCodexExtractTodoEvent_ASpanTypeDoesNotSuppressAPlan(t *testing.T) {
	t.Parallel()

	ev, ok := codexProvider{}.ExtractTodoEvent("Bash",
		[]byte(`{"method":"turn/plan/updated","params":{"plan":[{"step":"x","status":"pending"}]}}`), nil)
	require.True(t, ok)
	require.Len(t, ev.Snapshot, 1)
	assert.Equal(t, "x", ev.Snapshot[0].Content)
}

// The exact method check, not the span type, is what refuses a look-alike. Each case
// here is fed with NO span type, so only that check can reject it.
func TestCodexExtractTodoEvent_IgnoresEverythingElse(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"another method":      `{"method":"turn/started","params":{}}`,
		"empty content":       ``,
		"not json":            `not json`,
		"the method as text":  `{"method":"log","params":{"message":"turn/plan/updated fired"}}`,
		"another provider":    `{"sessionUpdate":"plan","entries":[{"content":"x"}]}`,
		"a Claude todo write": `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[]}}]}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := codexExtract(t, content)
			assert.False(t, ok)
		})
	}
}

// Codex needs no paired tool_use, so it must never spend the database read.
func TestCodexExtractTodoEvent_NeverReadsThePairedUse(t *testing.T) {
	t.Parallel()

	read := false
	codexProvider{}.ExtractTodoEvent("", []byte(`{"method":"turn/plan/updated","params":{"plan":[{"step":"x"}]}}`),
		func() []byte {
			read = true
			return nil
		})
	assert.False(t, read)
}
