package codebuddy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// browserAnswer is the neutral envelope that AnswerControlRequest builds.
func browserAnswer(t *testing.T, requestID, behavior, message string, updatedInput map[string]any) []byte {
	t.Helper()
	inner := map[string]any{"behavior": behavior}
	if message != "" {
		inner["message"] = message
	}
	if updatedInput != nil {
		inner["updatedInput"] = updatedInput
	}
	data, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": inner}})
	require.NoError(t, err)
	return data
}

// nativeAnswer reads CodeBuddy's own answer out of a translated envelope.
func nativeAnswer(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Response  struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(content, &envelope), string(content))
	assert.Equal(t, "control_response", envelope.Type)
	assert.Equal(t, "success", envelope.Response.Subtype)
	assert.Equal(t, envelope.RequestID, envelope.Response.RequestID)
	return envelope.Response.Response
}

func TestCodebuddyTranslateAllowWithoutAnUpdateStatesTheOriginalShape(t *testing.T) {
	t.Parallel()
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "allow", "", nil))
	require.True(t, ok)
	assert.Equal(t, map[string]any{"allowed": true}, nativeAnswer(t, translated))
}

func TestCodebuddyTranslateAllowOmitsAnEmptyUpdate(t *testing.T) {
	t.Parallel()
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:empty", "allow", "", map[string]any{}))
	require.True(t, ok)
	assert.Equal(t, map[string]any{"allowed": true}, nativeAnswer(t, translated))
}

func TestCodebuddyResolveControlResponseClassifiesPlanExit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool     string
		decision *leapmuxv1.ControlDecision
		want     agent.PlanModeControlKind
	}{
		{tool: "ExitPlanMode", decision: agenttest.PlanDecision(&leapmuxv1.PlanDecision{Approve: true}), want: agent.PlanModeControlExit},
		{tool: "Bash", decision: agenttest.PermissionDecision(contracts.ControlAnswerOptionAllow, ""), want: agent.PlanModeControlNone},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			payload := codebuddyToolPayload(t, tc.tool, map[string]any{})
			answer, err := agenttest.AnswerControl(codebuddyProvider{}, payload, codebuddyTestRequestID, tc.decision)
			require.NoError(t, err)
			resolution := agenttest.ResolveAnswer(codebuddyProvider{}, payload, codebuddyTestRequestID, answer)
			assert.False(t, resolution.Withhold)
			assert.Equal(t, tc.want, resolution.PlanModeControl)
			assert.Equal(t, true, nativeAnswer(t, resolution.Content)["allowed"])
		})
	}
}

// An allow carries the input that AnswerControlRequest folded the answers into. An
// AskUserQuestion reply is the whole tool input with `answers` added, and
// CodeBuddy merges that object into the tool call.
func TestCodebuddyTranslateAllowForwardsUpdatedInput(t *testing.T) {
	t.Parallel()
	updated := map[string]any{
		"questions": []any{map[string]any{"question": "Color?"}},
		"answers":   map[string]any{"Color?": "Red"},
		"count":     float64(0),
		"enabled":   false,
		"text":      "",
		"optional":  nil,
	}
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "allow", "", updated))
	require.True(t, ok)
	native := nativeAnswer(t, translated)
	assert.Equal(t, true, native["allowed"])
	require.IsType(t, map[string]any{}, native["updatedInput"])
	assert.Equal(t, updated, native["updatedInput"])
}

func TestCodebuddyTranslateDenyCarriesTheReason(t *testing.T) {
	t.Parallel()
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "deny", "Use a dry run.", nil))
	require.True(t, ok)
	assert.Equal(t, map[string]any{"allowed": false, "reason": "Use a dry run."}, nativeAnswer(t, translated))
}

// A bare deny states no typed reason. CodeBuddy's answer always carries one, so
// the placeholder fills it.
func TestCodebuddyTranslateBareDenyFillsThePlaceholder(t *testing.T) {
	t.Parallel()
	translated, ok := translateCanUseToolAnswer(browserAnswer(t, "approval:1", "deny", "", nil))
	require.True(t, ok)
	assert.Equal(t, map[string]any{"allowed": false, "reason": contracts.ControlAnswerMessageRejectedByUser}, nativeAnswer(t, translated))
}

func TestCodebuddyTranslateIgnoresAPayloadThatStatesNoDecision(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		``,
		`not json`,
		`{"response":{"request_id":"x","response":{"jsonrpc":"2.0"}}}`,
		`{"response":{"request_id":"x","response":{"outcome":"proceed_once"}}}`,
	} {
		_, ok := translateCanUseToolAnswer([]byte(content))
		assert.False(t, ok, content)
	}
}
