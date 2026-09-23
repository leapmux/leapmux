package cursor

import (
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

// Agent manages a single Cursor CLI ACP process.
type Agent struct {
	acp.Base

	// taskToolCalls holds the toolCallId of every `task` tool call the spawn
	// hook claimed. The closing hook needs it: Cursor reports "this ran in the
	// background" only in the final update's rawOutput, and the tool's identity
	// only in rawInput, which that update does not always carry. Without the
	// note, a backgrounded task and a backgrounded shell are the same wire
	// shape.
	//
	// Guarded by Base's Mu, which neither handleToolCall nor
	// handleToolCallUpdate holds while it calls a hook, so the hooks can take it.
	// An entry is dropped on the final update. A `task` call that never reaches
	// one keeps its entry -- one bool and one id -- for the life of the agent,
	// which matches how Base's subagentPrompts holds a spawn's prompt.
	taskToolCalls map[string]bool
	// taskReports joins the live cursor/task extension with the local-store
	// result. Cursor replays the store record but not the extension, so only a
	// state that saw both can publish a report. Guarded by Base.Mu and capped
	// in tool_transcript.go.
	taskReports map[string]cursorTaskReportState

	// transcript is the sink that configure installed, held under its own type so
	// the extension handler can reach EnrichToolSpan. The transcript is the single
	// writer of a row's supplemental content, and the `cursor/*` frames land on a
	// row that its store pass also enriches -- see EnrichToolSpan for what a second
	// writer would destroy. It is written once, in configure, before the reader
	// goroutine starts.
	transcript *tooltranscript.Transcript
}

// This assertion makes a missing Agent method a compile error.
var _ agent.Agent = (*Agent)(nil)
