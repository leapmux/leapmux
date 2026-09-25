package ohmypi

// The omp vocabulary that ONLY the worker reads. The words both sides read are
// generated from contracts/ohmypi-protocol.json.

// omp RPC command types: the `type` field of a command the worker writes to stdin.
// omp echoes the command id on its `{type:"response"}` frame. The `compact` command
// is contracts.OhMyPiCommandCompact, because the browser reads its response too.
const (
	CommandNegotiateProtocol       = "negotiate_protocol"
	CommandSetSubagentSubscription = "set_subagent_subscription"
	CommandPrompt                  = "prompt"
	CommandAbort                   = "abort"
	CommandNewSession              = "new_session"
	CommandGetState                = "get_state"
	CommandGetSessionStats         = "get_session_stats"
	CommandGetAvailableModels      = "get_available_models"
	CommandSetModel                = "set_model"
	CommandSetThinkingLevel        = "set_thinking_level"
	CommandHostToolResult          = "host_tool_result"
	CommandHostURIResult           = "host_uri_result"
)

// rpcProtocolVersion is the RPC protocol version the worker negotiates. Version 1
// shrinks a frame above 1 MiB by eliding strings, which loses data; version 2
// splits it into rpc_chunk frames instead.
const rpcProtocolVersion = 2

// subagentSubscriptionEvents is the subscription level that streams every event
// of every subagent as a subagent_event frame. The default level, "off", sends no
// subagent frame at all.
const subagentSubscriptionEvents = "events"

// Values of the `streamingBehavior` field of a prompt. omp queues a prompt that
// states one while a run streams, and starts a run for it otherwise. A prompt that
// states none FAILS while a run streams.
const (
	streamingBehaviorSteer    = "steer"
	streamingBehaviorFollowUp = "followUp"
)

// Labels that omp's `ask` tool adds to its select dialogs. The question bridge
// answers the dialog chain with them, and the browser never sees them: it answers
// the whole question set at once (see ask.go).
const (
	// askOtherOption is the last option of every question dialog. Choosing it
	// opens an editor dialog for the reader's own text.
	askOtherOption = "Other (type your own)"
	// askSelectedPrefixOpen and askSelectedPrefixClose frame the count that opens
	// the title of a multi-select dialog after its first toggle:
	// "(2 selected) Which languages?".
	askSelectedPrefixOpen  = "("
	askSelectedPrefixClose = " selected) "
)

// omp's subagent lifecycle statuses, from the `status` field of a
// subagent_lifecycle frame.
const (
	subagentStatusStarted   = "started"
	subagentStatusCompleted = "completed"
	subagentStatusFailed    = "failed"
	subagentStatusAborted   = "aborted"
)

// omp's goal statuses, from the `status` field of the goal a goal_updated frame
// carries.
const (
	goalStatusActive        = "active"
	goalStatusPaused        = "paused"
	goalStatusBudgetLimited = "budget-limited"
	goalStatusComplete      = "complete"
	goalStatusDropped       = "dropped"
)

// asyncJobTypeBash is the `type` of a background job that a `bash` call started,
// in the `details.async` of the call's result and in the job list of an
// async-result message.
const asyncJobTypeBash = "bash"

// contentBlockText is the `type` of a text content block in a message or a tool
// result.
const contentBlockText = "text"

// contentBlockThinking is the `type` of a thinking content block in an assistant
// message. Its text is in the block's `thinking` field.
const contentBlockThinking = "thinking"

// contentBlockToolCall is the `type` of a tool-call content block in an assistant
// message.
const contentBlockToolCall = "toolCall"
