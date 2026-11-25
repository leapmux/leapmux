package providerkit

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Claude-shaped control requests.
//
// Claude Code's stream-JSON protocol defined a control request that states a
// tool name and the tool's input in `request`, and CodeBuddy Code and Qoder
// CLI adopted it. The tool name decides what the request asks:
//
//   - AskUserQuestion asks the user questions.
//   - ExitPlanMode asks the user to approve a plan.
//   - Any other tool asks for permission to run.
//
// The readers below read `request.tool_name` and `request.input` in one pass
// with the same JSON-object fallback that agent.ControlToolInput applies, so a
// prompt shows the input that its answer sends back.

// The tool names that decide what a Claude-shaped control request asks.
const (
	claudeShapedToolAskUserQuestion = "AskUserQuestion"
	claudeShapedToolExitPlanMode    = "ExitPlanMode"
	// claudeShapedToolBash is the shell tool. Its permission prompt states the
	// command that the call runs.
	claudeShapedToolBash = "Bash"
)

func claudeShapedRequest(payload []byte) (toolName string, toolInput []byte) {
	request := RawFields(RawFields(payload)["request"])
	return ObjectString(request, "tool_name"), RawObjectOrEmpty(request["input"])
}

// ClaudeShapedQuestionPrompt reads a request of the AskUserQuestion tool: the
// `questions` of the tool input, as QuestionsFromWire reads them. It reports
// false for a request of another tool.
func ClaudeShapedQuestionPrompt(payload []byte) (*leapmuxv1.ControlPrompt, bool) {
	toolName, toolInput := claudeShapedRequest(payload)
	if toolName != claudeShapedToolAskUserQuestion {
		return nil, false
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: &leapmuxv1.QuestionPrompt{
		Questions: QuestionsFromWire(RawFields(toolInput)["questions"]),
	}}}, true
}

// ClaudeShapedToolPrompt reads a request of any tool other than
// AskUserQuestion.
//
//   - ExitPlanMode is a plan. The plan itself is in the transcript, so the
//     prompt states its kind alone.
//   - Any other tool is a permission. The prompt states the tool name as its
//     title, the tool input, and the options that the provider offers. A Bash
//     call also states its command, when the input holds one as text.
func ClaudeShapedToolPrompt(payload []byte, options []*leapmuxv1.PermissionOption) *leapmuxv1.ControlPrompt {
	toolName, toolInput := claudeShapedRequest(payload)
	if toolName == claudeShapedToolExitPlanMode {
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{}}}
	}
	permission := &leapmuxv1.PermissionPrompt{
		Title:     toolName,
		InputJson: string(toolInput),
		Options:   options,
	}
	if toolName == claudeShapedToolBash {
		permission.Command = ObjectString(RawFields(toolInput), "command")
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
}
