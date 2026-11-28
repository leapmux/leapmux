package amp

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The names under which Amp's executor runs a shell call, beside
// contracts.AmpShellToolShellCommand: the executor runs `shell_command` as
// `async_shell_command`, and the current registration of the shell tool is
// `Bash`. The browser's Amp plugin spells the same two names in its
// AMP_TOOL_NAME table.
const (
	ampToolAsyncShellCommand = "async_shell_command"
	ampToolBash              = "Bash"
)

// ampUnnamedToolTitle is the title of a request that states no tool name.
const ampUnnamedToolTitle = "Tool"

// isAmpShellTool reports whether a call of toolName runs a shell command.
func isAmpShellTool(toolName string) bool {
	switch toolName {
	case contracts.AmpShellToolShellCommand, ampToolAsyncShellCommand, ampToolBash:
		return true
	default:
		return false
	}
}

// ReadControlRequest reads the permission request that the Worker publishes
// for each call that Amp's `delegate` permission rule hands to the LeapMux
// helper: the tool, its arguments, and the call's id. Amp offers no options,
// so the prompt states LeapMux's allow and deny. A shell call also states its
// command and its working directory.
//
// A payload that is not this envelope reads as the generic prompt.
func (ampProvider) ReadControlRequest(input agent.ControlRequestInput) *leapmuxv1.ControlPrompt {
	permission, ok := ampPermissionPrompt(input.Payload)
	if !ok {
		return agent.GenericPermissionPrompt()
	}
	return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: permission}}
}

// ampPermissionPrompt reads the Worker's permission envelope, and reports
// false for any other payload.
func ampPermissionPrompt(payload []byte) (*leapmuxv1.PermissionPrompt, bool) {
	fields, ok := providerkit.RawObject(payload)
	if !ok || providerkit.ObjectString(fields, contracts.AmpPermissionRequestFieldType) != contracts.AmpPermissionRequestTypeRequest {
		return nil, false
	}
	toolName := providerkit.ObjectString(fields, contracts.AmpPermissionRequestFieldToolName)
	toolInput := providerkit.RawObjectOrEmpty(fields[contracts.AmpPermissionRequestFieldInput])
	permission := &leapmuxv1.PermissionPrompt{
		Title:     toolName,
		InputJson: string(toolInput),
		Options:   agent.AllowDenyOptions(),
	}
	if permission.Title == "" {
		permission.Title = ampUnnamedToolTitle
	}
	if isAmpShellTool(toolName) {
		arguments, _ := providerkit.RawObject(toolInput)
		permission.Command = ampShellCommand(arguments)
		permission.WorkingDirectory = ampShellWorkingDirectory(arguments)
	}
	return permission, true
}

// ampShellCommand reads the command of one shell call: `shell_command`
// states it as `command`, and the current `Bash` tool as `cmd`.
func ampShellCommand(arguments map[string]json.RawMessage) string {
	if command := providerkit.ObjectString(arguments, "command"); command != "" {
		return command
	}
	return providerkit.ObjectString(arguments, "cmd")
}

// ampShellWorkingDirectory reads the working directory of one shell call:
// `shell_command` states it as `workdir`, and the current `Bash` tool as
// `cwd`.
func ampShellWorkingDirectory(arguments map[string]json.RawMessage) string {
	if directory := providerkit.ObjectString(arguments, "workdir"); directory != "" {
		return directory
	}
	return providerkit.ObjectString(arguments, "cwd")
}

// AnswerControlRequest answers with the neutral envelope, which the agent's
// SendRawInput turns into the helper's answer:
//
//   - Allow sends the request's input back (agent.AnswerAllowDeny). Amp's
//     envelope states no `request.input`, so the input is `{}`.
//   - Deny carries the user's feedback, which reaches the model as the
//     reason, or the placeholder that Amp turns into its own wording.
//
// Allow takes no feedback. Every Amp prompt is a permission, so a decision
// of another kind fails.
func (ampProvider) AnswerControlRequest(input agent.ControlAnswerInput) (agent.ControlAnswer, error) {
	return agent.AnswerAllowDeny(input)
}
