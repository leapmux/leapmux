package kimi

import (
	"fmt"
	"regexp"
)

// Kimi Code's kap-server vocabulary that only the worker reads.
//
// The words both sides spell -- event types, tool names, decisions, modes -- are
// in contracts/kimi-protocol.json. What is here is the transport: the REST
// routes, the envelope codes, the WebSocket control frames, and the fields of
// the requests the worker sends. No browser code reads any of them.

// kimiBinaryName is the program the provider launches. The legacy Python
// kimi-cli installs a program of the same name, which is why start.go checks
// the version before it starts the server.
const kimiBinaryName = "kimi"

// kimiMinimumVersion is the first Kimi Code release that ships the kap-server
// this provider speaks. Every earlier `kimi` is the legacy Python kimi-cli,
// whose `web` command is a different program.
var kimiMinimumVersion = kimiVersion{major: 2}

// kimiServerArgs start the kap-server.
//
//   - `--no-open` is required: the server opens a browser tab by default.
//   - `--port 0` binds a free port, and the server states the port it bound.
//   - `--log-level warn` makes the server print one ready line instead of its
//     multi-line colored banner. Its pino JSON logs at that level go to the same
//     stdout, which the reader logs and otherwise ignores.
var kimiServerArgs = []string{"web", "--no-open", "--host", "127.0.0.1", "--port", "0", "--log-level", "warn"}

// kimiReadyLine matches the one line with which the server states where it
// listens: `Kimi server: http://127.0.0.1:60565/#token=<token>`. The first group
// is the address, and the second the bearer token every request carries.
var kimiReadyLine = regexp.MustCompile(`^Kimi server: (http://[^/\s#]+)/?#token=(\S+)\s*$`)

// kimiAddressLine is the same line with one capture group, the address, which
// is the shape providerkit.ListenWaiter takes.
var kimiAddressLine = regexp.MustCompile(`^Kimi server: (http://[^/\s#]+)/?#token=\S+\s*$`)

// The REST routes. Every route is under /api/v1.
const (
	kimiAPIPrefix     = "/api/v1"
	kimiRouteMeta     = kimiAPIPrefix + "/meta"
	kimiRouteModels   = kimiAPIPrefix + "/models"
	kimiRouteConfig   = kimiAPIPrefix + "/config"
	kimiRouteSessions = kimiAPIPrefix + "/sessions"
	kimiRouteShutdown = kimiAPIPrefix + "/shutdown"
	kimiRouteWS       = kimiAPIPrefix + "/ws"
)

// kimiSafeID matches every id the server issues: a session, an approval, a
// question, a task, a prompt. A route is built by joining ids, so an id outside
// this set could reach a different route than the one the caller means.
var kimiSafeID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// kimiCheckID refuses an id that is not one the server issues.
func kimiCheckID(kind, id string) error {
	if !kimiSafeID.MatchString(id) {
		return fmt.Errorf("the %s id %q is not one Kimi Code issues", kind, id)
	}
	return nil
}

// kimiSessionPath returns the path of one session resource:
// `/api/v1/sessions/<id><suffix>`. suffix starts with `/` or `:`. The caller
// checked the id with kimiCheckID.
func kimiSessionPath(sessionID, suffix string) string {
	return kimiRouteSessions + "/" + sessionID + suffix
}

// kimiItemPath returns `/api/v1/sessions/<id>/<collection>/<item><action>`.
// action is empty or starts with `:`. The caller checked both ids.
func kimiItemPath(sessionID, collection, itemID, action string) string {
	return kimiSessionPath(sessionID, "/"+collection+"/"+itemID+action)
}

// The REST envelope codes the worker branches on. The server answers almost
// every request with HTTP 200 and states the outcome in `code`.
const (
	kimiCodeOK = 0
	// kimiCodeAlreadyResolved answers an approval that another path resolved.
	kimiCodeAlreadyResolved = 40902
	// kimiCodeQuestionDismissed is the code a SUCCESSFUL dismissal returns. It is
	// not an error, however it reads.
	kimiCodeQuestionDismissed = 40909
	// kimiCodeGoalExists refuses a goal while another one exists.
	kimiCodeGoalExists = 40913
	// kimiCodeGoalNotFound refuses a goal control for a session that has no goal.
	// The server removes a goal by itself once it completes.
	kimiCodeGoalNotFound = 40914
)

// The WebSocket control frames.
const (
	kimiFrameServerHello = "server_hello"
	kimiFramePing        = "ping"
	kimiFramePong        = "pong"
	kimiFrameAck         = "ack"
	kimiFrameSubscribe   = "subscribe"
	kimiFrameUnsubscribe = "unsubscribe"
)

// kimiMainAgentID identifies the session's main agent in every event. Each
// subagent has its own id, `agent-0`, `agent-1`, and so on.
const kimiMainAgentID = "main"

// The `agent_config` fields of POST /sessions/{id}/profile.
const (
	kimiConfigModel          = "model"
	kimiConfigThinking       = "thinking"
	kimiConfigPermissionMode = "permission_mode"
	kimiConfigPlanMode       = "plan_mode"
	kimiConfigSwarmMode      = "swarm_mode"
	kimiConfigGoalObjective  = "goal_objective"
	kimiConfigGoalControl    = "goal_control"
)

// The `goal_control` words.
const (
	kimiGoalControlPause  = "pause"
	kimiGoalControlResume = "resume"
	kimiGoalControlCancel = "cancel"
)

// The thinking words a model without an effort ladder takes.
const (
	kimiThinkingOn  = "on"
	kimiThinkingOff = "off"
)

// The capabilities a model states.
const (
	kimiCapabilityThinking       = "thinking"
	kimiCapabilityAlwaysThinking = "always_thinking"
	kimiCapabilityImageIn        = "image_in"
)

// The session action that cancels the main agent's turn, and the ones that
// compact the context.
const (
	kimiActionAbort   = ":abort"
	kimiActionCompact = ":compact"
	kimiActionDismiss = ":dismiss"
	kimiActionCancel  = ":cancel"
)

// The kinds of `task.started.info`.
const (
	kimiTaskKindProcess  = "process"
	kimiTaskKindAgent    = "agent"
	kimiTaskKindQuestion = "question"
)

// The final statuses of a background task, as `task.terminated.info.status`
// states them.
const (
	kimiTaskStatusRunning   = "running"
	kimiTaskStatusCompleted = "completed"
	kimiTaskStatusFailed    = "failed"
	kimiTaskStatusTimedOut  = "timed_out"
	kimiTaskStatusKilled    = "killed"
	kimiTaskStatusLost      = "lost"
)

// The `kind` words of an item of GET /sessions/{id}/tasks. The route maps the
// task kinds onto its own words: a process is `bash`, an agent is `subagent`,
// and a question is `tool`.
const (
	kimiWireTaskKindBash     = "bash"
	kimiWireTaskKindSubagent = "subagent"
)

// The `status` words of a task item and of a subagent of the snapshot's roster.
// The task route folds timed_out and lost into failed, and killed into
// cancelled.
const (
	kimiWireStatusRunning   = "running"
	kimiWireStatusCompleted = "completed"
	kimiWireStatusFailed    = "failed"
	kimiWireStatusCancelled = "cancelled"
)

// The `subagent_phase` words of the snapshot's roster that decide a subagent's
// turn flag. A working subagent runs its turn. A queued or suspended one runs
// none: it waits to start, or for the swarm to retry it.
const (
	kimiSubagentPhaseQueued    = "queued"
	kimiSubagentPhaseWorking   = "working"
	kimiSubagentPhaseSuspended = "suspended"
)

// The `tool.progress.update.kind` words that carry a command's output.
const (
	kimiProgressStdout = "stdout"
	kimiProgressStderr = "stderr"
)

// The `goal.updated.snapshot.status` words.
const (
	kimiGoalActive   = "active"
	kimiGoalPaused   = "paused"
	kimiGoalBlocked  = "blocked"
	kimiGoalComplete = "complete"
)

// kimiFeatureGoal is the engine feature, as GET /meta lists it, that the goal
// routes need.
const kimiFeatureGoal = "goal"

// kimiQuestionMethod is the answer method the worker states for every question
// answer: the reader clicked an option in LeapMux's own control.
const kimiQuestionMethod = "click"

// kimiRawAbortFrame is the raw frame with which a caller of SendAgentRawMessage
// interrupts the agent. Kimi Code has no stdin protocol, so the frame is
// LeapMux's own, and it takes its word from the session action it maps to:
// POST /sessions/{id}:abort.
const kimiRawAbortAction = "abort"
