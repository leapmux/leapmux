package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// TestClaudeUserEnvelopeMarkType covers the Claude `user`-envelope scroll-rail mark
// classifier. Only a self-displaying control answer (an AskUserQuestion / ExitPlanMode
// tool_result Claude re-emits into its own transcript) is marked at ingestion, as
// CONTROL_RESPONSE. Everything else -- including a human-typed prompt (which resolves no
// span, spanType "") -- is UNSPECIFIED, so ingestion never duplicates a mark that queue
// acceptance wrote. spanType is the resolved tool name for a
// tool_result row (empty otherwise).
func TestClaudeUserEnvelopeMarkType(t *testing.T) {
	t.Parallel()

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
// ordinary tool is not a control tool at all. TestOnlyClaudeSelfDisplaysAControlTool
// pins that every other provider defers to the synthesized display row.
func TestIsSelfDisplayingControlTool(t *testing.T) {
	t.Parallel()

	assert.True(t, claudeProvider{}.IsSelfDisplayingControlTool(ToolNameAskUserQuestion))
	assert.True(t, claudeProvider{}.IsSelfDisplayingControlTool(ToolNameExitPlanMode))
	assert.False(t, claudeProvider{}.IsSelfDisplayingControlTool(ToolNameEnterPlanMode))
	assert.False(t, claudeProvider{}.IsSelfDisplayingControlTool("Bash"))
	assert.False(t, claudeProvider{}.IsSelfDisplayingControlTool(""))
}

// TestClaudePlanModeControl pins Claude's reading of its own plan-mode tool names.
// Shared service code consumes only these provider-neutral classifications, so
// provider wire names do not leak back into service-level plan-mode policy.
func TestClaudePlanModeControl(t *testing.T) {
	t.Parallel()

	assert.Equal(t, agent.PlanModeControlEnter, claudeProvider{}.PlanModeControl(ToolNameEnterPlanMode))
	assert.Equal(t, agent.PlanModeControlExit, claudeProvider{}.PlanModeControl(ToolNameExitPlanMode))
	assert.Equal(t, agent.PlanModeControlNone, claudeProvider{}.PlanModeControl(ToolNameAskUserQuestion))
}
