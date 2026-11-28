package pi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestPiPlanStatementTextUsesJavaScriptWhitespace(t *testing.T) {
	t.Parallel()
	// U+FEFF in args.plan is blank in JS .trim(), so it must fall through to result text.
	bomPlan := []byte(`{"type":"tool_execution_end","toolCallId":"c1","toolName":"plan_mode_complete","args":{"plan":"\uFEFF"},"result":{"content":[{"type":"text","text":"Fallback text"}]}}`)
	text, ok := (piProvider{}).ReplyText(agent.MessageContent{Original: bomPlan})
	assert.True(t, ok)
	assert.Equal(t, "Fallback text", text)

	// U+0085 in args.plan is visible text in JS .trim(), so it counts as a plan (not a text statement).
	nelPlan := []byte(`{"type":"tool_execution_end","toolCallId":"c2","toolName":"plan_mode_complete","args":{"plan":"\u0085"},"result":{"content":[{"type":"text","text":"Fallback text"}]}}`)
	text, ok = (piProvider{}).ReplyText(agent.MessageContent{Original: nelPlan})
	assert.False(t, ok)
	assert.Empty(t, text)
}

func TestPiPlanStatementTextAndToolResultJSWhitespaceAndFallbacks(t *testing.T) {
	t.Parallel()

	t.Run("details.plan BOM vs NEL", func(t *testing.T) {
		t.Parallel()
		// BOM in details.plan is blank in JS .trim() -> statesPlan is false -> falls through to result content.
		bomDetails := []byte(`{"type":"tool_execution_end","toolCallId":"c1","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"Result note"}],"details":{"plan":"\uFEFF"}}}`)
		text, ok := piPlanStatementText(bomDetails)
		assert.True(t, ok)
		assert.Equal(t, "Result note", text)

		// NEL in details.plan is non-blank in JS .trim() -> statesPlan is true -> returns ready notice when !isError.
		nelDetails := []byte(`{"type":"tool_execution_end","toolCallId":"c2","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"Result note"}],"details":{"plan":"\u0085"}}}`)
		text, ok = piPlanStatementText(nelDetails)
		assert.True(t, ok)
		assert.Equal(t, piPlanReadyNotice, text)

		// When isError is true, statesPlan does not override the error text.
		nelDetailsErr := []byte(`{"type":"tool_execution_end","toolCallId":"c3","toolName":"plan_mode_complete","isError":true,"result":{"content":[{"type":"text","text":"Plan rejected"}],"details":{"plan":"\u0085"}}}`)
		text, ok = piPlanStatementText(nelDetailsErr)
		assert.True(t, ok)
		assert.Equal(t, "Plan rejected", text)
	})

	t.Run("result.content BOM vs NEL", func(t *testing.T) {
		t.Parallel()
		bomContent := []byte(`{"type":"tool_execution_end","toolCallId":"c4","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"\uFEFF"}]}}`)
		text, ok := piPlanStatementText(bomContent)
		assert.False(t, ok)
		assert.Empty(t, text)

		nelContent := []byte(`{"type":"tool_execution_end","toolCallId":"c5","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"\u0085"}]}}`)
		text, ok = piPlanStatementText(nelContent)
		assert.True(t, ok)
		assert.Equal(t, "\u0085", text)
	})

	t.Run("partialResult fallback vs blank result", func(t *testing.T) {
		t.Parallel()
		// Absent result falls back to partialResult.
		partialOnly := []byte(`{"type":"tool_execution_end","toolCallId":"c6","toolName":"plan_mode_complete","partialResult":{"content":[{"type":"text","text":"Partial text"}]}}`)
		text, ok := piPlanStatementText(partialOnly)
		assert.True(t, ok)
		assert.Equal(t, "Partial text", text)

		// Explicit null result falls back to partialResult.
		nullResult := []byte(`{"type":"tool_execution_end","toolCallId":"c7","toolName":"plan_mode_complete","result":null,"partialResult":{"content":[{"type":"text","text":"\u0085"}]}}`)
		text, ok = piPlanStatementText(nullResult)
		assert.True(t, ok)
		assert.Equal(t, "\u0085", text)

		// Present blank result blocks fallback to partialResult.
		blankResult := []byte(`{"type":"tool_execution_end","toolCallId":"c8","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"\uFEFF"}]},"partialResult":{"content":[{"type":"text","text":"Should not appear"}]}}`)
		text, ok = piPlanStatementText(blankResult)
		assert.False(t, ok)
		assert.Empty(t, text)
	})

	t.Run("missing toolCallId is rejected", func(t *testing.T) {
		t.Parallel()
		noCallID := []byte(`{"type":"tool_execution_end","toolName":"plan_mode_complete","result":{"content":[{"type":"text","text":"Result note"}]}}`)
		text, ok := piPlanStatementText(noCallID)
		assert.False(t, ok)
		assert.Empty(t, text)
	})

	t.Run("piToolResultOf direct BOM vs NEL", func(t *testing.T) {
		t.Parallel()
		res, ok := piToolResultOf(json.RawMessage(`{"content":[{"type":"text","text":"\uFEFF"}],"details":{"plan":"\uFEFF"}}`))
		assert.True(t, ok)
		assert.False(t, res.statesPlan)
		assert.Equal(t, "\uFEFF", res.text)

		res, ok = piToolResultOf(json.RawMessage(`{"content":[{"type":"text","text":"\u0085"}],"details":{"plan":"\u0085"}}`))
		assert.True(t, ok)
		assert.True(t, res.statesPlan)
		assert.Equal(t, "\u0085", res.text)

		_, ok = piToolResultOf(json.RawMessage(`null`))
		assert.False(t, ok)
	})
}
