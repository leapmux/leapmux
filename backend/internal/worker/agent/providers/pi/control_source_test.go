package pi

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiQuestionSourceConformance(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "pi_question_source_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name          string           `json:"name"`
			Dialog        piQuestionDialog `json:"dialog"`
			Args          json.RawMessage  `json:"args"`
			ExpectedIndex *int             `json:"expectedIndex"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	for _, item := range fixture.Cases {
		t.Run(item.Name, func(t *testing.T) {
			index, matched := piQuestionIndex(item.Dialog, item.Args)
			assert.Equal(t, item.ExpectedIndex != nil, matched)
			if item.ExpectedIndex != nil {
				assert.Equal(t, *item.ExpectedIndex, index)
			}
		})
	}
}

func TestPiQuestionIndexRejectsDifferentAndAmbiguousDialogs(t *testing.T) {
	t.Parallel()
	args := json.RawMessage(`{"questions":[{"question":"Choose","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}`)
	for _, dialog := range []piQuestionDialog{
		{Method: "select", Title: "Different", Options: []string{"1. A — first", "2. B — second", "3. Other"}},
		{Method: "select", Title: "Choose", Options: []string{"1. A — changed", "2. B — second", "3. Other"}},
		{Method: "select", Title: "Choose", Options: []string{"1. A — first", "2. B — second"}},
		{Method: "confirm", Title: "Choose"},
	} {
		_, matches := piQuestionIndex(dialog, args)
		assert.False(t, matches)
	}
	duplicate := json.RawMessage(`{"questions":[{"question":"Choose","options":[{"label":"A","description":"first"}]},{"question":"Choose","options":[{"label":"A","description":"first"}]}]}`)
	_, matches := piQuestionIndex(piQuestionDialog{Method: "select", Title: "Choose", Options: []string{"1. A — first", "2. Other"}}, duplicate)
	assert.False(t, matches)
}

func TestPiQuestionIndexMatchesMultiSelectAndCustomInput(t *testing.T) {
	t.Parallel()
	args := json.RawMessage(`{"questions":[{"question":"Choose","header":"Layout","multiSelect":true,"options":[{"label":"A","description":"first"}]},{"question":"Explain","options":[{"label":"B","description":"second"}]}]}`)
	index, matches := piQuestionIndex(piQuestionDialog{Method: "input", Title: "[Layout] Choose\n\n1. A — first\n\nLocalized instructions", Placeholder: "1,3"}, args)
	assert.True(t, matches)
	assert.Equal(t, 0, index)
	index, matches = piQuestionIndex(piQuestionDialog{Method: "input", Title: "Explain\n\nLocalized custom answer prompt"}, args)
	assert.True(t, matches)
	assert.Equal(t, 1, index)
}

func TestPiControlSourceRejectsAmbiguousAndChangedTools(t *testing.T) {
	for _, change := range []string{"second tool", "completed", "changed arguments"} {
		t.Run(change, func(t *testing.T) {
			a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
			handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"mcp","toolName":"mcp__probe__read","args":{}}`)))
			switch change {
			case "second tool":
				handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"other","toolName":"read","args":{"path":"sample.py"}}`)))
			case "completed":
				handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_end","toolCallId":"mcp","toolName":"mcp__probe__read","result":{"content":[]}}`)))
			case "changed arguments":
				a.toolStates["mcp"].Args = json.RawMessage(`{"different":true}`)
			}
			assert.Zero(t, a.piControlSourceSeq(nil))
		})
	}
}
