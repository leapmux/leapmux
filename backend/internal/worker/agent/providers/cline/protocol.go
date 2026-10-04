package cline

// Cline's own words that only the worker reads. The words both sides read are in
// contracts/cline-protocol.json; these stay here under the contracts rule's
// one-side-only exemption.

// hubProtocolVersion is the one hub protocol version this package speaks. The
// daemon states the range of client versions it accepts in its discovery
// record, and the worker refuses a daemon whose range leaves v1 out.
const hubProtocolVersion = "v1"

// hubAuthSubprotocolPrefix begins the WebSocket subprotocol that carries the
// daemon's auth token. The daemon reads the token from this subprotocol only.
const hubAuthSubprotocolPrefix = "cline-hub-auth."

// The kinds of a hub transport frame.
const (
	frameCommand           = "command"
	frameReply             = "reply"
	frameEvent             = "event"
	frameStreamSubscribe   = "stream.subscribe"
	frameStreamUnsubscribe = "stream.unsubscribe"
)

// The hub commands the worker sends.
const (
	commandClientRegister       = "client.register"
	commandSessionCreate        = "session.create"
	commandSessionDetach        = "session.detach"
	commandSessionGet           = "session.get"
	commandSessionMessages      = "session.messages"
	commandSessionList          = "session.list"
	commandSessionCompactionGet = "session.compaction.get"
	commandSessionSendInput     = "session.send_input"
	commandUpdateConnection     = "session.update_connection"
	commandRunAbort             = "run.abort"
	commandApprovalRespond      = "approval.respond"
	commandCapabilityRespond    = "capability.respond"
)

// The hub events the worker reads alone. The events that reach the transcript
// or the control channel are in the contract.
const (
	eventRunStarted             = "run.started"
	eventRunHeartbeat           = "run.heartbeat"
	eventIterationStarted       = "iteration.started"
	eventIterationFinished      = "iteration.finished"
	eventAssistantDelta         = "assistant.delta"
	eventReasoningDelta         = "reasoning.delta"
	eventToolUpdated            = "tool.updated"
	eventUsageUpdated           = "usage.updated"
	eventAgentDone              = "agent.done"
	eventApprovalResolved       = "approval.resolved"
	eventCapabilityResolved     = "capability.resolved"
	eventSessionUpdated         = "session.updated"
	eventPendingPromptSubmitted = "session.pending_prompt_submitted"
)

// The delivery words of session.send_input. A plain send starts a turn; a
// steer joins the running turn at its next step.
const deliverySteer = "steer"

// The session modes of Cline, which the permission-mode axis carries. `yolo`
// is Cline's third mode, for unattended automation; LeapMux offers no value
// for it (see settings.go).
const (
	sessionModePlan = "plan"
	sessionModeAct  = "act"
)

// The kinds of configuration that Cline loads into a session, as
// runtimeOptions.configExtensions states them (RUNTIME_CONFIG_EXTENSION_KINDS in
// sdk/packages/shared/src/session/runtime-config.ts of Cline 3.0.64). Rules,
// skills and workflows are text that the model reads. Cline also has `plugins`
// and `hooks`, which are code that Cline runs with no approval: a plugin when
// the session starts, and a hook at each of its events. A session that states
// no list loads every kind.
const (
	extensionRules     = "rules"
	extensionSkills    = "skills"
	extensionWorkflows = "workflows"
)

// clientType and clientDisplayName identify the worker in the daemon's client
// list.
const (
	clientType        = "leapmux"
	clientDisplayName = "LeapMux"
)

// clientTransport is the transport word of a client that speaks the native
// WebSocket framing.
const clientTransport = "native"

// capabilityApprovalRespond is the client capability that answers tool
// approvals. A client without it receives no approval requests.
const capabilityApprovalRespond = "approval.respond"

// The client contributions the worker makes at session.create. The question
// executor makes Cline offer `ask_question`, which the hub then asks the worker
// to answer. The plan tool is a custom tool that exists only in Plan mode.
const (
	contributionToolExecutor   = "toolExecutor"
	contributionTool           = "tool"
	executorAskQuestion        = "askQuestion"
	capabilitySwitchToActMode  = "custom_tool.switch_to_act_mode"
	switchToActModeDescription = "Switch from plan mode to act mode. Switching to act mode immediately starts executing the plan, so only call this after the user has explicitly approved the plan in a message sent AFTER you presented it (e.g. 'looks good', 'go ahead', 'switch to act mode'). Never call this in the same turn you present a plan, never call it proactively, and never treat the original task request as approval."
)

// switchToActModeResult is what the worker answers the plan tool with. The
// words are Cline's own, from its interactive runtime, so the model reads the
// switch as Cline states it.
const switchToActModeResult = "You successfully switched to act mode, proceed with the plan. You now have access to editing files and running commands. (The switch_to_act_mode tool is only available in plan mode.)"

// actModeContinuationPrompt is the message the worker sends after a switch to
// Act mode. Cline's own interactive runtime sends the same words after the
// model calls switch_to_act_mode, so the model continues the approved plan.
const actModeContinuationPrompt = "The user approved switching to act mode. Continue with the approved plan now."

// The environment variables of the Cline daemon. The worker sets each one for
// the daemon it starts.
const (
	// envRunAsHubDaemon selects the daemon personality of the `cline` binary.
	envRunAsHubDaemon = "CLINE_RUN_AS_HUB_DAEMON"
	// envHubDiscoveryPath moves the daemon's discovery record, and with it the
	// daemon's instance lock, into the agent's private directory.
	envHubDiscoveryPath = "CLINE_HUB_DISCOVERY_PATH"
	// envHubPort is the port that Cline's own components resolve for "the
	// hub". The worker sets it to the private daemon's port.
	envHubPort = "CLINE_HUB_PORT"
	// envSessionBackendMode keeps a Cline process on its local runtime: it
	// neither attaches to a running hub nor starts a detached one. The daemon
	// does not read it. A `cline` that a tool runs inherits it, and the session
	// listing sets it also.
	envSessionBackendMode = "CLINE_SESSION_BACKEND_MODE"
	// envNoAutoUpdate turns off the npm update check of a `cline` that a tool
	// runs, and of the session listing. The daemon itself runs no update check.
	envNoAutoUpdate = "CLINE_NO_AUTO_UPDATE"
	// envTasksDBPath moves the daemon's agenda task database into the agent's
	// private directory (see connection.go).
	envTasksDBPath = "CLINE_TASKS_DB_PATH"
)

// sessionBackendLocal is the backend mode of a process that must never reach
// another hub.
const sessionBackendLocal = "local"

// localRuntimeEnv keeps a Cline process on its local session backend and off
// the npm update check. The shell wrapper sets it after the user's profile
// runs, because a profile export would otherwise replace it: with the backend
// mode `auto`, a `cline` can attach to the user's own hub or start a detached
// one.
var localRuntimeEnv = []string{
	envSessionBackendMode + "=" + sessionBackendLocal,
	envNoAutoUpdate + "=1",
}

// The daemon's command-line flags. They are internal to Cline: `cline hub
// start` detaches the daemon, so the worker could not own its lifetime. The
// daemon's own launcher adds `--cline-hub-daemon`, which the daemon ignores
// (CLINE_RUN_AS_HUB_DAEMON selects it), and the worker leaves it out: `cline
// doctor fix` kills every process whose command line holds it.
const (
	flagCwd          = "--cwd"
	flagHost         = "--host"
	flagPort         = "--port"
	flagPathname     = "--pathname"
	flagNoConnectors = "--no-connectors"
)

// hubPathname is the path of the private daemon's WebSocket.
const hubPathname = "/hub"

// userInputOpen and userInputClose wrap each user message that Cline stores:
// `<user_input mode="act">text</user_input>`. The session picker strips them
// from a title.
const (
	userInputOpen  = "<user_input"
	userInputClose = "</user_input>"
)
