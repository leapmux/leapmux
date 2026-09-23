package goose

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Goose sends these notifications after the client advertises customNotifications.
const (
	gooseSessionUpdateMethod = "_goose/unstable/session/update"
	gooseUpdateUsage         = "usage_update"
	gooseUpdateStatusMessage = "status_message"
	// A notice is transcript content. A progress update is live status.
	gooseStatusNotice = "notice"
)

type gooseOutputState struct {
	sequence uint64
	bytes    int64
	// bytesAreMinimum says the COUNT is a lower bound, which only Goose's own
	// truncation flag establishes: every chunk's length is added before the tail is
	// cut, so a local cap never makes the total approximate.
	bytesAreMinimum bool
	// tailLostBytes says the retained TAIL dropped earlier output, which both the
	// local cap and Goose's truncation cause. The two were one field, so trimming
	// an 8 KiB display window made the worker report an exact byte total as "at
	// least N" -- and `ProgressCounter` aggregates that flag across the agent, so
	// one long Goose call marked every concurrent tool's count approximate.
	tailLostBytes bool
	// tail is the text the chunks carried, joined. Goose sends a live_output
	// notification per CHUNK, so only this agent knows where the earlier ones
	// ended -- and the running row draws what the call has printed so far.
	tail string
}

// The longest live text one Goose call keeps between notifications.
//
// The sink caps the tail again before it broadcasts. This cap is here so the
// joined string cannot grow without limit inside the agent, for a command that
// prints for minutes.
const gooseLiveOutputLimit = 8192

// captureSteerRunID reads the two steering fields Goose states on a
// `session_info_update`, and claims the update so nothing else has to read it.
//
// `activeRunId` is the run a steer must address; it arrives when a turn opens and
// again as null when the turn ends.
//
// `queuedSteer` ACKNOWLEDGES a steer LeapMux sent: a live `goose acp` probe recorded
// it as `{"messageId":"steer_...","runId":"run_..."}`, on its own update with no
// `activeRunId` beside it. It changes nothing here, because the steer request itself
// already answered and LeapMux knows the steer was accepted from that answer. It is
// READ so the update is claimed rather than falling through to be persisted as a row
// the browser then hides, and so the next reader finds the shape recorded rather than
// only the field name.
func (a *Agent) captureSteerRunID(updateType string, metadata map[string]json.RawMessage) bool {
	if updateType != "session_info_update" {
		return false
	}
	var goose map[string]json.RawMessage
	if json.Unmarshal(metadata[gooseSteerNamespace], &goose) != nil {
		return false
	}
	if queued, ok := goose["queuedSteer"]; ok {
		var steer struct {
			MessageID string `json:"messageId"`
			RunID     string `json:"runId"`
		}
		if json.Unmarshal(queued, &steer) == nil {
			slog.Debug("goose queued a steer", "agent_id", a.AgentID(), "message_id", steer.MessageID, "run_id", steer.RunID)
		}
		return true
	}
	rawRunID, ok := goose["activeRunId"]
	if !ok {
		return false
	}
	var runID string
	if string(rawRunID) != "null" && json.Unmarshal(rawRunID, &runID) != nil {
		return false
	}
	a.SetSteerRunID(runID)
	return true
}

func (a *Agent) clearGooseToolOutput(toolCallID string) {
	a.Mu.Lock()
	delete(a.gooseOutput, toolCallID)
	a.Mu.Unlock()
}

// gooseToolNotification is the `_meta.toolNotification` envelope every live update
// of a running Goose call rides in.
//
// Goose declares FOUR types (`tool_notifications.rs`): `live_output` carries the
// shell text, `progress` carries a step count with the runtime's own sentence,
// `platform_event` carries an extension's announcement, and `message` carries a log
// line. Each arrives on a `tool_call_update` whose status is `in_progress`, so the
// shared classifier hides the row -- which is right for a row, and is also why the
// text inside it reaches the reader only when a hook below takes it.
type gooseToolNotification struct {
	Type   string `json:"type"`
	Params struct {
		// live_output
		Sequence  uint64 `json:"sequence"`
		Truncated bool   `json:"truncated"`
		Chunks    []struct {
			Output string `json:"output"`
		} `json:"chunks"`
		// progress
		Progress *float64 `json:"progress"`
		Total    *float64 `json:"total"`
		Message  string   `json:"message"`
		// platform_event
		Extension string `json:"extension"`
		EventType string `json:"event_type"`
	} `json:"params"`
}

func (a *Agent) gooseToolNotificationOf(update acp.ToolCallUpdateEnvelope) (gooseToolNotification, bool) {
	var meta struct {
		ToolNotification gooseToolNotification `json:"toolNotification"`
	}
	if json.Unmarshal(update.Meta, &meta) != nil || meta.ToolNotification.Type == "" {
		return gooseToolNotification{}, false
	}
	return meta.ToolNotification, true
}

// gooseProgressLine is the sentence one `progress` notification states, or "" when
// it states none.
//
// Goose sends the runtime's own `message` beside the counts, so the sentence leads
// and the counts qualify it -- `Scanned 3 of 10 directories (3/10)` reads worse than
// the sentence alone, which is why the counts are appended ONLY when the sentence is
// absent. A progress notification with neither says nothing a reader can use.
func gooseProgressLine(notification gooseToolNotification) string {
	if notification.Params.Message != "" {
		return notification.Params.Message
	}
	if notification.Params.Progress == nil {
		return ""
	}
	if notification.Params.Total != nil && *notification.Params.Total > 0 {
		return fmt.Sprintf("%s of %s",
			formatGooseCount(*notification.Params.Progress), formatGooseCount(*notification.Params.Total))
	}
	return formatGooseCount(*notification.Params.Progress)
}

// formatGooseCount drops the fraction of a whole number. Goose sends a float, and
// `3` reads better than `3.0` for a step count.
func formatGooseCount(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// observeGooseToolNotification reads the two live notifications that are neither
// shell output nor a log line, and reports whether it claimed the update.
//
// `progress` is the runtime's own sentence about a call that still runs, so it goes
// to the SAME ephemeral tail the shell output uses: the row draws it while the call
// runs and drops it when the result lands. Copilot's `progressMessage` takes that
// route for the same reason. Persisting one would write a row per tick.
//
// `platform_event` is an extension announcing that something happened outside this
// call -- an app created, a resource registered. That outlives the call, so it is a
// notification rather than a tail, and it is the one of the four that reaches the
// transcript.
func (a *Agent) observeGooseToolNotification(update acp.ToolCallUpdateEnvelope) bool {
	meta, ok := a.gooseToolNotificationOf(update)
	if !ok {
		return false
	}
	switch meta.Type {
	case "progress":
		line := gooseProgressLine(meta)
		if line == "" || update.ToolCallID == "" {
			return true
		}
		a.Sink().ReportProgress(agent.OutputTailProgress(update.ToolCallID, line, false))
		return true
	case "platform_event":
		if line := goosePlatformEventLine(meta); line != "" {
			a.Sink().PersistLeapMuxNotification(map[string]interface{}{
				contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
				contracts.NotificationFieldText: line,
			})
		}
		return true
	}
	return false
}

// goosePlatformEventLine states which extension announced what.
//
// Goose puts `extension` and `event_type` in every platform event and lets the
// extension add its own fields, which this does NOT read: their names are the
// extension's, not Goose's, so a reader of this file cannot know what they mean.
func goosePlatformEventLine(meta gooseToolNotification) string {
	extension, event := meta.Params.Extension, meta.Params.EventType
	switch {
	case extension != "" && event != "":
		return fmt.Sprintf("%s: %s", extension, strings.ReplaceAll(event, "_", " "))
	case event != "":
		return strings.ReplaceAll(event, "_", " ")
	case extension != "":
		return fmt.Sprintf("%s sent an event", extension)
	default:
		return ""
	}
}

// gooseToolOutput reads one `live_output` notification into the count and the text
// it leaves behind -- ONE observation, under ONE lock acquisition, so the byte count
// and the tail a row draws can never come from two different states.
func (a *Agent) gooseToolOutput(update acp.ToolCallUpdateEnvelope) (acp.ToolOutputObservation, bool) {
	meta, ok := a.gooseToolNotificationOf(update)
	if !ok || meta.Type != "live_output" {
		return acp.ToolOutputObservation{}, false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.gooseOutput == nil {
		a.gooseOutput = make(map[string]gooseOutputState)
	}
	state := a.gooseOutput[update.ToolCallID]
	if meta.Params.Sequence <= state.sequence {
		return acp.ToolOutputObservation{}, false
	}
	state.sequence = meta.Params.Sequence
	for _, chunk := range meta.Params.Chunks {
		state.bytes = agent.SaturatingAdd(state.bytes, int64(len([]byte(chunk.Output))))
		state.tail += chunk.Output
	}
	// Keep the END, which is what a reader watches, and say that the start is gone.
	var clipped bool
	state.tail, clipped = agent.ClipTailBytes(state.tail, gooseLiveOutputLimit)
	if clipped {
		state.tailLostBytes = true
	}
	if meta.Params.Truncated {
		state.bytesAreMinimum = true
		state.tailLostBytes = true
	}
	a.gooseOutput[update.ToolCallID] = state
	return acp.ToolOutputObservation{
		Total: state.bytes, TotalIsMinimum: state.bytesAreMinimum,
		Tail: state.tail, TailLost: state.tailLostBytes,
	}, true
}

// handleGooseExtraMethod claims the notifications Goose sends outside the Agent
// Client Protocol's own methods. It answers true for the ones it read, so the
// shared dispatcher does not persist them as raw rows.
func (a *Agent) handleGooseExtraMethod(line *providerkit.ParsedLine) bool {
	if line.Method != gooseSessionUpdateMethod {
		return false
	}
	a.handleGooseSessionUpdate(line.Params)
	return true
}

// handleGooseSessionUpdate reads one `_goose/unstable/session/update`.
//
// The three variants report different things and take different paths. A usage
// update is the session's own context and cost totals, which the meter reads. A
// status NOTICE is a sentence the reader must see, so it becomes a notification.
// Everything else is live chrome: Goose's own schema says a status message "is
// not conversation transcript content, and should not be persisted or replayed
// as history", and a per-message usage row states a cost the cumulative total
// beside it already carries -- a replay resends every one of them, so adding
// them up would count the turn twice.
func (a *Agent) handleGooseSessionUpdate(params json.RawMessage) {
	var notif struct {
		Update struct {
			SessionUpdate string   `json:"sessionUpdate"`
			Used          *int64   `json:"used"`
			ContextLimit  *int64   `json:"contextLimit"`
			InputTokens   int64    `json:"accumulatedInputTokens"`
			OutputTokens  int64    `json:"accumulatedOutputTokens"`
			Cost          *float64 `json:"accumulatedCost"`
			Status        struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"status"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &notif); err != nil {
		slog.Warn("goose session update unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	update := notif.Update
	switch update.SessionUpdate {
	case gooseUpdateUsage:
		info := map[string]interface{}{}
		if update.Used != nil && *update.Used >= 0 {
			// Through the shared projection, which is the ONE place the four count
			// keys are written. Goose measures neither cache half, and both rows need
			// a ZERO rather than an absent key: an absent one blanks the row in the
			// breakdown instead of showing that the provider counted none. This site
			// spelled the keys itself and omitted both of them.
			usage := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{
				Input:  update.InputTokens,
				Output: update.OutputTokens,
			})
			// The two conditional keys stay at the site, as providerkit.ContextTokenCounts states.
			usage[contracts.ContextUsageFieldContextTokens] = *update.Used
			if update.ContextLimit != nil && *update.ContextLimit > 0 {
				usage[contracts.ContextUsageFieldContextWindow] = *update.ContextLimit
			}
			info[contracts.SessionInfoKeyContextUsage] = usage
		}
		// The ACCUMULATED cost, which is a total rather than a delta -- a replay
		// resends the same total and cannot double it.
		if update.Cost != nil {
			info[contracts.SessionInfoKeyTotalCostUsd] = *update.Cost
		}
		if len(info) > 0 {
			a.Sink().BroadcastSessionInfo(info)
		}
	case gooseUpdateStatusMessage:
		if update.Status.Type != gooseStatusNotice || update.Status.Message == "" {
			return
		}
		a.Sink().PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
			contracts.NotificationFieldText: update.Status.Message,
		})
	}
}
