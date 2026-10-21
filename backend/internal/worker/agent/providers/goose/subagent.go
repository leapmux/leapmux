package goose

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Goose's subagent lifecycle words, from crates/goose's subagent_handler and
// state_machine/ops_subagent. The delegate ack text and these notices are the
// only lifecycle carriers on the ACP wire: the adapter drops the tool-result
// metadata that states subagent_session_id and foreground_subagent.
const (
	gooseDelegatedPrefix       = "Delegated to subagent "
	gooseRunningSubagentPrefix = "Running subagent "
	gooseSubagentPrefix        = "Subagent "
	// Each suffix follows the session id, whose cut consumes the space before it.
	gooseSubagentFailedWith = "failed: "
	// The export route that states a finished child's own conversation, and
	// the built-in tool whose arguments are that child's final output.
	gooseSessionExportMethod = "_goose/unstable/session/export"
	gooseFinalOutputTool     = "recipe__final_output"
)

// gooseSubagentFromToolCall detects Goose's spawn tool_call by the structured
// _meta.goose.toolCall marker {toolName:"delegate", extensionName:"summon"}.
// This is the spawn detector, not title guessing. The spawn creates the child
// transcript, and later tool-request updates add its live activity.
func gooseSubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	if len(tc.Meta) == 0 {
		return nil
	}
	var meta struct {
		Goose struct {
			ToolCall struct {
				ToolName      string `json:"toolName"`
				ExtensionName string `json:"extensionName"`
			} `json:"toolCall"`
		} `json:"goose"`
	}
	if err := json.Unmarshal(tc.Meta, &meta); err != nil {
		return nil
	}
	tc2 := meta.Goose.ToolCall
	if tc2.ToolName != contracts.GooseSubagentTool || tc2.ExtensionName != contracts.GooseSubagentExtension {
		return nil
	}
	title := tc.Title
	if title == "" {
		title = "Goose subagent"
	}
	return &acp.SubagentObservation{
		RowKey: tc.ToolCallID,
		Title:  title,
		Status: bgtask.StatusRunning,
		// Reserve the child by the spawn span alone: the delegate ack adopts
		// the native session id as the child's provider key later, and a key
		// adopted here would refuse that adoption as an identity clash.
		ChildSpawnSpanID: tc.ToolCallID,
		Spawns:           true,
		// Goose's delegate tool puts its task text in `instructions`, not `prompt`
		// (crates/goose/src/agents/platform_extensions/summon.rs).
		Prompt: gooseDelegateInstructions(tc.RawInput),
	}
}

// gooseDelegateInstructions pulls the delegate call's task text out of the
// tool_call's rawInput. Goose fills raw_input from the tool arguments
// (acp/server/tool_calls/conversion.rs), so the delegate arguments arrive
// verbatim. Returns "" when absent.
func gooseDelegateInstructions(rawInput json.RawMessage) string {
	if len(rawInput) == 0 {
		return ""
	}
	var in struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return ""
	}
	return in.Instructions
}

// gooseSubagentToolRequestType is the `data.type` that marks one subagent tool
// request inside Goose's logging metadata.
const gooseSubagentToolRequestType = "subagent_tool_request"

// gooseSubagentRequestedTool reads the name of the tool one subagent request asks
// for, or the empty string when the request states none.
//
// It indexes the `data` object with the generated constant instead of taking the key
// from a struct tag, which cannot hold one. Goose owns the word, and the browser
// draws the request row from the same object through the same contract table. A tag
// here would therefore keep the old word after a Goose release moved it: every
// request would fall back to the neutral activity line, with no build error and no
// log line.
func gooseSubagentRequestedTool(data json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return ""
	}
	var call struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(fields[contracts.GooseSubagentRequestToolCall], &call) != nil {
		return ""
	}
	return call.Name
}

// gooseSubagentFromToolCallUpdate observes Goose's subagent tool requests.
// Goose surfaces tool REQUESTS (never results) over ACP via a two-level-nested
// _meta payload: toolNotification.type is "message"; the discriminator is
// params.data.type == "subagent_tool_request". Each request carries the
// subagent_id and the tool_call name, so we upsert a running row with activity
// "tool: <name>" and persist the raw request to the child transcript.
//
// The registry row, the EnsureChildAgent linkage, and the lifecycle updates all
// key off ONE identity: the spawn toolCallId until the delegate ack states the
// native session id, and that session id afterwards.
func gooseSubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	// The final update on the spawn tool_call itself is the delegate ACK: Goose
	// returns "Delegated to subagent <session id>" before the child runs, so
	// this update must keep the row running and adopt the session id the ack
	// text states. ACP drops the tool-result metadata that carries
	// subagent_session_id and foreground_subagent, so the text is the only
	// source. The child's own lifecycle notices and delivery messages close the
	// row later (gooseSubagentNotice and gooseSubagentDelivery).
	if acp.StatusIsFinal(tcu.Status) {
		if sessionID, ok := gooseDelegatedSessionID(acp.ToolCallText(tcu.Content)); ok {
			return &acp.SubagentObservation{
				RowKey:        sessionID,
				RenameFrom:    tcu.ToolCallID,
				Status:        bgtask.StatusRunning,
				ChildAgentKey: sessionID,
			}
		}
		// A final spawn update that delegates no child ended the spawn itself.
		report := acp.ToolCallText(tcu.Content)
		return &acp.SubagentObservation{
			RowKey:   tcu.ToolCallID,
			Status:   acp.FinalStatus(tcu.Status),
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
			ReportID: tcu.ToolCallID,
			Report:   agent.SubagentReport{Text: report},
		}
	}
	if len(tcu.Meta) == 0 {
		return nil
	}
	var meta struct {
		ToolNotification struct {
			Type   string          `json:"type"`
			Params json.RawMessage `json:"params"`
		} `json:"toolNotification"`
	}
	if err := json.Unmarshal(tcu.Meta, &meta); err != nil || meta.ToolNotification.Type != "message" {
		return nil
	}
	var params struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(meta.ToolNotification.Params, &params); err != nil {
		return nil
	}
	var data struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(params.Data, &data); err != nil || data.Type != gooseSubagentToolRequestType {
		return nil
	}
	// Key the registry row, the child-agent linkage, AND the lifecycle updates
	// off the SPAWN toolCallId until the delegate ack renames it. One Goose
	// spawn = one child = one transcript; the per-request subagent_id is not
	// used as a registry key.
	activity := "tool request"
	if name := gooseSubagentRequestedTool(params.Data); name != "" {
		activity = "tool: " + name
	}
	// Persist the tool-request update to the child transcript (PersistChildMessage
	// via applySubagentObservation). Goose only ever ships requests, so this is
	// the live child activity. The payload is a tool_call_update-shaped envelope
	// carrying sessionUpdate + status + _meta so the shared ACP classifier
	// recognizes it and routes it to the subagent-tool-request renderer (a plain
	// re-marshal of the parsed struct drops sessionUpdate, leaving the classifier
	// no branch to match and the row renders as a raw-JSON dump).
	payload := gooseSubagentToolRequestPayload(tcu, meta.ToolNotification.Params)
	return &acp.SubagentObservation{
		RowKey: tcu.ToolCallID,
		Title:  "Goose subagent",
		// A request reaches the child that the spawn span reserved. No
		// provider key here: the delegate ack adopts the native session id as
		// that key, and an earlier different key would refuse the adoption.
		ChildSpawnSpanID:       tcu.ToolCallID,
		Activity:               activity,
		Status:                 bgtask.StatusRunning,
		ChildTranscriptPayload: payload,
	}
}

// observeGooseSubagentUpdate adapts the shared detector to this agent's child
// renames. The delegate ack renames a row from the spawn toolCallId to the
// native session id the ack text states; a tool request that arrives after
// that ack still names the spawn id, so it must reach the renamed row instead
// of opening a second one.
func (a *Agent) observeGooseSubagentUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	obs := gooseSubagentFromToolCallUpdate(tcu)
	if obs == nil {
		return nil
	}
	if obs.RenameFrom != "" {
		a.Mu.Lock()
		if a.gooseChildRenames == nil {
			a.gooseChildRenames = make(map[string]string)
		}
		a.gooseChildRenames[obs.RenameFrom] = obs.RowKey
		a.Mu.Unlock()
		return obs
	}
	a.Mu.Lock()
	renamed := a.gooseChildRenames[tcu.ToolCallID]
	a.Mu.Unlock()
	if renamed == "" || renamed == obs.RowKey {
		return obs
	}
	obs.RowKey = renamed
	obs.ChildAgentKey = renamed
	return obs
}

// gooseDelegatedSessionID reads the delegate ack text: "Delegated to subagent
// <session id>". A session id holds no spaces.
func gooseDelegatedSessionID(text string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), gooseDelegatedPrefix)
	if !ok {
		return "", false
	}
	sessionID, _, more := strings.Cut(rest, " ")
	if sessionID == "" || more {
		return "", false
	}
	return sessionID, true
}

// gooseSubagentSessionID reads the session id that one lifecycle sentence names,
// and returns the sentence's remainder after it.
func gooseSubagentSessionID(text, prefix string) (string, string, bool) {
	rest, ok := strings.CutPrefix(text, prefix)
	if !ok {
		return "", "", false
	}
	sessionID, remainder, _ := strings.Cut(rest, " ")
	if sessionID == "" || strings.ContainsAny(sessionID, ":\n") {
		return "", "", false
	}
	return sessionID, remainder, true
}

// gooseSubagentNotice reads one inline subagent lifecycle notice that Goose
// emits to the parent (`ops_subagent.rs`): "Running subagent <id> (<task>)"
// while the child runs, "Subagent <id> completed\n\n<summary>" or
// "Subagent <id> completed" at its end, and "Subagent <id> failed: <reason>"
// at a failure. A cancelled child sends no notice at all.
//
// The completed notice carries its summary only while other subagents still
// run, so a single child's report comes from the delivery message instead
// (gooseSubagentDelivery).
func gooseSubagentNotice(text string) *acp.SubagentObservation {
	if sessionID, _, ok := gooseSubagentSessionID(text, gooseRunningSubagentPrefix); ok {
		// No child linkage here: the delegate ack already linked the child, and
		// a second ensure with the session id as its own spawn span would read
		// as an identity clash and refuse every later update of the row.
		return &acp.SubagentObservation{
			RowKey: sessionID,
			Status: bgtask.StatusRunning,
		}
	}
	sessionID, rest, ok := gooseSubagentSessionID(text, gooseSubagentPrefix)
	if !ok {
		return nil
	}
	if summary, done := strings.CutPrefix(rest, "completed"); done {
		summary = strings.TrimPrefix(summary, "\n\n")
		return &acp.SubagentObservation{
			RowKey:   sessionID,
			Status:   bgtask.StatusSucceeded,
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
			ReportID: sessionID,
			Report:   agent.SubagentReport{Text: strings.TrimSpace(summary)},
		}
	}
	if reason, failed := strings.CutPrefix(rest, gooseSubagentFailedWith); failed {
		return &acp.SubagentObservation{
			RowKey:   sessionID,
			Status:   bgtask.StatusFailed,
			CloseRow: true,
			Mode:     acp.ModeCloseOnly,
			ReportID: sessionID,
			Report:   agent.SubagentReport{Text: reason},
		}
	}
	return nil
}

// fetchGooseSubagentReport reads a finished child's final output through the
// session export. A single child's completed notice states no summary, because
// Goose attaches the summary only while other subagents still run, and the
// hidden delivery message that carries the output to the parent model never
// reaches the ACP stream. The export is the one route that states the child's
// own conversation.
func (a *Agent) fetchGooseSubagentReport(sessionID string) {
	params, err := json.Marshal(map[string]string{"sessionId": sessionID, "format": "json"})
	if err != nil {
		return
	}
	if err := a.SendDetachedRequest(gooseSessionExportMethod, params, func(result json.RawMessage, err error) {
		if err != nil {
			slog.Debug("goose subagent export failed", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
			return
		}
		report, ok := gooseExportedSubagentReport(result)
		if !ok {
			return
		}
		a.ApplySubagentObservation(&acp.SubagentObservation{
			RowKey:   sessionID,
			Mode:     acp.ModeCloseOnly,
			ReportID: sessionID,
			Report:   agent.SubagentReport{Text: report},
		})
	}); err != nil {
		slog.Debug("goose subagent export request failed", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	}
}

// gooseExportedSubagentReport reads the child's last successful final-output
// call from one session export reply, with Goose's own summary rule.
func gooseExportedSubagentReport(result json.RawMessage) (string, bool) {
	var reply struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(result, &reply) != nil || reply.Data == "" {
		return "", false
	}
	var export struct {
		Conversation []struct {
			Content []struct {
				Type     string `json:"type"`
				ToolCall *struct {
					Status string `json:"status"`
					Value  struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"value"`
				} `json:"toolCall"`
			} `json:"content"`
		} `json:"conversation"`
	}
	if json.Unmarshal([]byte(reply.Data), &export) != nil {
		return "", false
	}
	for i := len(export.Conversation) - 1; i >= 0; i-- {
		blocks := export.Conversation[i].Content
		for j := len(blocks) - 1; j >= 0; j-- {
			call := blocks[j].ToolCall
			if blocks[j].Type != "toolRequest" || call == nil || call.Value.Name != gooseFinalOutputTool {
				continue
			}
			if call.Status != "" && call.Status != "success" {
				continue
			}
			return gooseReadableSubagentOutput(string(call.Value.Arguments)), true
		}
	}
	return "", false
}

// gooseReadableSubagentOutput applies Goose's own readable_output rule: the
// child's final output is the JSON arguments of its recipe__final_output call,
// and an object whose only member is a string "summary" reads as that summary.
func gooseReadableSubagentOutput(output string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &fields); err != nil || len(fields) != 1 {
		return output
	}
	summary, ok := providerkit.RawString(fields["summary"])
	if !ok {
		return output
	}
	return summary
}

// gooseSubagentToolRequestPayload builds the child-transcript payload for a
// Goose subagent tool-request update. It re-marshals the on-the-wire envelope
// (including sessionUpdate/status/_meta) rather than the parsed struct so the
// shared frontend ACP classifier recognizes the row as a tool_call_update and
// routes it to the subagent-tool-request renderer instead of falling through to
// the raw-JSON last resort.
func gooseSubagentToolRequestPayload(tcu acp.ToolCallUpdateEnvelope, notificationParams json.RawMessage) []byte {
	type toolRequestUpdate struct {
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Status        string          `json:"status"`
		Kind          string          `json:"kind,omitempty"`
		Meta          json.RawMessage `json:"_meta,omitempty"`
	}
	// Re-wrap the original _meta (which carries toolNotification.params.data
	// at _meta.toolNotification) verbatim so the renderer can read the tool
	// name from params.data.tool_call.name.
	meta := tcu.Meta
	if len(meta) == 0 {
		// Fall back to a synthesized _meta if the envelope somehow lost it.
		meta = []byte(`{"toolNotification":{"type":"message","params":` + string(notificationParams) + `}}`)
	}
	payload, err := json.Marshal(toolRequestUpdate{
		SessionUpdate: "tool_call_update",
		ToolCallID:    tcu.ToolCallID,
		Status:        tcu.Status,
		Kind:          tcu.Title,
		Meta:          meta,
	})
	if err != nil {
		slog.Warn("goose subagent marshal transcript payload failed", "error", err)
		return []byte{}
	}
	return payload
}
