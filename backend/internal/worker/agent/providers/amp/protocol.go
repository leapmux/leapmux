package amp

// Amp's own words that only the worker reads. The words both sides read are in
// contracts/amp-protocol.json. These stay here under the contracts rule's
// one-side-only exemption.

// systemSubtypeInit is the subtype of the `system` line that states the thread
// id. Amp prints it late, when the first inference of the process starts.
const systemSubtypeInit = "init"

// stopReasonEndTurn ends a turn. Amp states it on the assistant message that
// closes the turn, and it prints no other turn end.
const stopReasonEndTurn = "end_turn"

// Content block and image source words of a user line that the worker writes to
// Amp's stdin.
const (
	blockTypeImage    = "image"
	imageSourceBase64 = "base64"
	roleUser          = "user"
)

// toolAskUserChoice is Amp's question tool. In stream-JSON mode Amp rejects the
// question and ENDS the session, so the generated settings disable the tool.
const toolAskUserChoice = "ask_user_choice"

// Amp's four built-in agent modes, the positions of what Amp calls the Dial.
// Each one selects a model, a reasoning effort, a system prompt and a tool set,
// and a thread keeps the mode of its first message.
const (
	agentModeLow    = "low"
	agentModeMedium = "medium"
	agentModeHigh   = "high"
	agentModeUltra  = "ultra"
)

// The environment variables of the Amp CLI and of the helper that its
// `delegate` permission rule starts.
const (
	// envSettingsFile moves Amp's default settings file. The generated settings
	// merge the file it states.
	envSettingsFile = "AMP_SETTINGS_FILE"
	// envSkipUpdateCheck turns Amp's update check off. Execute mode does not
	// start the update service, so this is a second guard.
	envSkipUpdateCheck = "AMP_SKIP_UPDATE_CHECK"
	// envXDGConfigHome moves the directory of Amp's default settings file.
	envXDGConfigHome = "XDG_CONFIG_HOME"
	// envToolName is the tool that a helper run asks about. Amp sets it only for
	// a `delegate` rule, so a run without it did not come from Amp.
	envToolName = "AGENT_TOOL_NAME"
	// envThreadID is the thread of the call that a helper run asks about.
	envThreadID = "AMP_THREAD_ID"
)

// The settings keys that the generated settings file writes. Every Amp setting
// takes the `amp.` prefix in the file.
const (
	settingToolsDisable        = "amp.tools.disable"
	settingUpdatesMode         = "amp.updates.mode"
	settingPermissions         = "amp.permissions"
	settingDangerouslyAllowAll = "amp.dangerouslyAllowAll"
	// settingGuardedFilesAllowlist lists the paths that Amp does not guard.
	settingGuardedFilesAllowlist = "amp.guardedFiles.allowlist"
)

// guardedFilesCatchAll is the allowlist entry that matches every local file.
// Amp's matcher prefixes a pattern path with file:// and turns ** into .*, so
// the entry matches every file URI (file:///..., on Windows file:///c%3A/...).
const guardedFilesCatchAll = "/**"

// The permission rule words of `amp.permissions`.
const (
	ruleActionAsk      = "ask"
	ruleActionAllow    = "allow"
	ruleActionDelegate = "delegate"
	ruleToolAll        = "*"
	updatesDisabled    = "disabled"
)

// helperPermission is the name the permission helper takes in the registration.
const helperPermission = "permission"
