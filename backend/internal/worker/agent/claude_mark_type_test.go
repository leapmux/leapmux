package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestClaudeUserEnvelopeMarkType covers the Claude `user`-envelope scroll-rail mark
// classifier. Only a self-displaying control answer (an AskUserQuestion / ExitPlanMode
// tool_result Claude re-emits into its own transcript) is marked at ingestion, as
// CONTROL_RESPONSE. Everything else -- including a human-typed prompt (which resolves no
// span, spanType "") -- is UNSPECIFIED, so ingestion never double-marks a send that the
// SendAgentMessage handler already dotted. spanType is the resolved tool name for a
// tool_result row (empty otherwise).
func TestClaudeUserEnvelopeMarkType(t *testing.T) {
	cases := []struct {
		name     string
		spanType string
		want     leapmuxv1.MarkType
	}{
		{
			name:     "AskUserQuestion tool_result is a control response",
			spanType: ToolNameAskUserQuestion,
			want:     leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE,
		},
		{
			name:     "ExitPlanMode tool_result is a control response",
			spanType: ToolNameExitPlanMode,
			want:     leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE,
		},
		{
			name:     "EnterPlanMode is not self-displaying, so unmarked",
			spanType: ToolNameEnterPlanMode,
			want:     leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED,
		},
		{
			name:     "an ordinary tool_result (Bash) is unmarked",
			spanType: "Bash",
			want:     leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED,
		},
		{
			name:     "a human-typed prompt (no resolved span) is unmarked, NOT USER_MESSAGE",
			spanType: "",
			want:     leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, claudeUserEnvelopeMarkType(tc.spanType))
		})
	}
}

// TestIsSelfDisplayingControlTool pins the provider-delegated set the rail's
// CONTROL_RESPONSE mark and the synthetic-display-row skip both consult, so they can't
// drift. Only Claude self-displays (its AskUserQuestion / ExitPlanMode tool_results);
// EnterPlanMode is deliberately NOT self-displaying (no user-answer tool_result), and an
// ordinary tool is not a control tool at all. Every other provider echoes no control
// answer, so it defers to the synthesized display row -> false.
func TestIsSelfDisplayingControlTool(t *testing.T) {
	assert.True(t, claudeProvider{}.IsSelfDisplayingControlTool(ToolNameAskUserQuestion))
	assert.True(t, claudeProvider{}.IsSelfDisplayingControlTool(ToolNameExitPlanMode))
	assert.False(t, claudeProvider{}.IsSelfDisplayingControlTool(ToolNameEnterPlanMode))
	assert.False(t, claudeProvider{}.IsSelfDisplayingControlTool("Bash"))
	assert.False(t, claudeProvider{}.IsSelfDisplayingControlTool(""))

	// Non-Claude providers never self-display -- they rely on the synthesized row.
	assert.False(t, codexProvider{}.IsSelfDisplayingControlTool(ToolNameAskUserQuestion))
	assert.False(t, piProvider{}.IsSelfDisplayingControlTool(ToolNameExitPlanMode))
	assert.False(t, acpProvider{}.IsSelfDisplayingControlTool(ToolNameExitPlanMode))
	assert.False(t, noopProvider{}.IsSelfDisplayingControlTool(ToolNameAskUserQuestion))
}

// TestSyntheticInterruptNotice pins the provider-delegated decision the raw-message handler
// consults instead of a hardcoded `== CODEX` switch, including the exact display text. Only Codex
// consumes its interrupt silently (turn/interrupt resolves internally with no transcript row), so
// only it returns the synthetic "[Request interrupted by user]" row text; every other provider's
// interrupt surfaces in its own transcript and returns "" (ACP via the noopProvider embedding).
func TestSyntheticInterruptNotice(t *testing.T) {
	assert.Equal(t, "[Request interrupted by user]", codexProvider{}.SyntheticInterruptNotice())

	assert.Empty(t, claudeProvider{}.SyntheticInterruptNotice())
	assert.Empty(t, piProvider{}.SyntheticInterruptNotice())
	assert.Empty(t, acpProvider{}.SyntheticInterruptNotice())
	assert.Empty(t, noopProvider{}.SyntheticInterruptNotice())
}

// TestPlanModeControl pins provider-owned tool-name interpretation. Shared service code
// consumes only these provider-neutral classifications, so provider wire names do not leak
// back into service-level plan-mode policy.
func TestPlanModeControl(t *testing.T) {
	assert.Equal(t, PlanModeControlPrompt, codexProvider{}.PlanModeControl(ToolNameCodexPlanModePrompt))
	assert.Equal(t, PlanModeControlNone, codexProvider{}.PlanModeControl(ToolNameEnterPlanMode))

	assert.Equal(t, PlanModeControlEnter, claudeProvider{}.PlanModeControl(ToolNameEnterPlanMode))
	assert.Equal(t, PlanModeControlExit, claudeProvider{}.PlanModeControl(ToolNameExitPlanMode))
	assert.Equal(t, PlanModeControlNone, claudeProvider{}.PlanModeControl(ToolNameAskUserQuestion))

	assert.Equal(t, PlanModeControlNone, piProvider{}.PlanModeControl(ToolNameExitPlanMode))
	assert.Equal(t, PlanModeControlNone, acpProvider{}.PlanModeControl(ToolNameExitPlanMode))
	assert.Equal(t, PlanModeControlNone, noopProvider{}.PlanModeControl(ToolNameCodexPlanModePrompt))
}
