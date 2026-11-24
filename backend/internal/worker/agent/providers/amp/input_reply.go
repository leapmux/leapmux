package amp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var streamInputNamespace = uuid.MustParse("f040eb8b-36a2-49e4-868e-1b9424d00b25")

// ampNativeInputID computes the ID that Amp's stream input sends to the
// actor. The capture from Amp 0.0.1795176040 confirms the namespace and
// encoding. An ID has authority only after it appears in the thread export.
func ampNativeInputID(requestID string) string {
	const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	native := uuid.NewSHA1(streamInputNamespace, []byte("cli-stream-json-input:"+requestID))
	value := new(big.Int).SetBytes(native[:])
	base, remainder := big.NewInt(62), new(big.Int)
	var encoded [22]byte
	for i := len(encoded) - 1; i >= 0; i-- {
		value.QuoRem(value, base, remainder)
		encoded[i] = digits[remainder.Int64()]
	}
	return "M-" + string(encoded[:])
}

// ampInputReply retains the live transcript candidates while its turn runs.
// The agent mutex protects the capture until endTurn takes it.
type ampInputReply struct {
	receipt         agent.NativeInputReceipt
	steers          []agent.NativeInputReceipt
	startsWithSteer bool
	frames          []ampReplyFrame
	unproved        bool
	settledHistory  *ampReplyHistory
}

type ampReplyFrame struct {
	digest   [sha256.Size]byte
	textKeys []string
}

func ampReplyFrameDigest(blocks []ampBlock) ([sha256.Size]byte, error) {
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return agent.JSONCanonicalDigest(encoded)
}

func (a *Agent) captureReplyFrame(threadID string) (*ampInputReply, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := a.turn.reply
	if reply == nil || reply.unproved {
		return nil, 0
	}
	if reply.receipt.SessionID != threadID {
		reply.unproved = true
		return nil, 0
	}
	return reply, len(reply.frames)
}

func (a *Agent) retainReplyFrame(reply *ampInputReply, frame ampReplyFrame, failed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.turn.reply == reply {
		reply.unproved = reply.unproved || failed
		reply.frames = append(reply.frames, frame)
	}
}

type ampReplyHistory struct {
	assistant []ampExportMessage
	steers    []ampSteerBoundary
	present   bool
	ended     bool
	conflict  bool
}

type ampSteerBoundary struct {
	inputID    string
	afterFrame int
}

// replyHistory begins at the exact submitted ID. Tool results and native
// steers can occur before the reply finishes. Another ordinary input ends
// the search even if the export has no completion for the earlier one.
func (exported *ampThreadExport) replyHistory(inputID string) ampReplyHistory {
	var history ampReplyHistory
	for _, message := range exported.Messages {
		if !history.present {
			if message.ID == inputID {
				if !isAmpExportPrompt(message) {
					history.conflict = true
					return history
				}
				history.present = true
			}
			continue
		}
		switch message.Role {
		case roleUser:
			if !isAmpExportPrompt(message) {
				continue
			}
			if !message.Meta.Steer {
				history.conflict = true
				return history
			}
			history.steers = append(history.steers, ampSteerBoundary{inputID: message.ID, afterFrame: len(history.assistant)})
		case contracts.AmpLineTypeAssistant:
			history.assistant = append(history.assistant, message)
			if message.State.Type == "complete" && message.State.StopReason == stopReasonEndTurn {
				history.ended = true
				return history
			}
		}
	}
	return history
}

func isAmpExportPrompt(message ampExportMessage) bool {
	return message.Role == roleUser && !isAmpExportToolResult(message)
}

func isAmpExportToolResult(message ampExportMessage) bool {
	if len(message.Content) == 0 {
		return false
	}
	for _, raw := range message.Content {
		if providerkit.ObjectString(providerkit.RawFields(raw), "type") != contracts.AmpBlockTypeToolResult {
			return false
		}
	}
	return true
}

func (history ampReplyHistory) blocksTool(toolUseID string) bool {
	_, ok := history.toolFrame(toolUseID)
	return ok
}

func (history ampReplyHistory) toolFrame(toolUseID string) (int, bool) {
	for i, assistant := range history.assistant {
		for _, raw := range assistant.Content {
			block := providerkit.RawFields(raw)
			if providerkit.ObjectString(block, "type") == contracts.AmpBlockTypeToolUse && providerkit.ObjectString(block, "id") == toolUseID {
				return i, true
			}
		}
	}
	return 0, false
}

// project compares complete visible frames before reusing their stored row
// keys. Amp's stdout omits the actor's message IDs.
func (reply *ampInputReply) project(history ampReplyHistory) (agent.NativeReplyTurn, bool) {
	if !history.ended || len(history.assistant) != len(reply.frames) || !reply.matchesHistoryPrefix(history) {
		return agent.NativeReplyTurn{}, false
	}
	result := agent.NativeReplyTurn{
		SessionID: reply.receipt.SessionID, TurnID: reply.receipt.InputID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: reply.receipt.InputID, AfterOrdinal: 1}},
	}
	positions := make([]int64, len(reply.frames)+1)
	positions[0] = 1
	for i, frame := range reply.frames {
		for _, key := range frame.textKeys {
			result.Items = append(result.Items, agent.NativeReplyItem{Ordinal: int64(len(result.Items)) + 2, MessageKey: key})
		}
		positions[i+1] = int64(len(result.Items)) + 1
	}
	owned := make(map[string]bool, len(reply.steers))
	for _, receipt := range reply.steers {
		owned[receipt.InputID] = true
	}
	for _, steer := range history.steers {
		if owned[steer.inputID] {
			result.Inputs = append(result.Inputs, agent.NativeReplyBoundary{InputID: steer.inputID, AfterOrdinal: positions[steer.afterFrame]})
		}
	}
	return result, len(result.Inputs) == len(reply.steers)+1
}

func (reply *ampInputReply) matchesHistoryPrefix(history ampReplyHistory) bool {
	if reply.unproved || !history.present || len(history.assistant) < len(reply.frames) {
		return false
	}
	for i, live := range reply.frames {
		message := history.assistant[i]
		if message.State.Type != "complete" {
			return false
		}
		native, err := visibleExportBlocks(message.Content)
		if err != nil {
			return false
		}
		digest, err := ampReplyFrameDigest(native)
		if err != nil || live.digest != digest {
			return false
		}
	}
	return true
}

func (a *Agent) readReplyHistory(ctx context.Context, receipt agent.NativeInputReceipt) (ampReplyHistory, error) {
	exported, err := a.readInputExport(ctx, receipt.SessionID)
	if err != nil {
		return ampReplyHistory{}, err
	}
	return exported.replyHistory(receipt.InputID), nil
}

func (a *Agent) readInputExport(ctx context.Context, threadID string) (*ampThreadExport, error) {
	raw, err := a.exportThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return parseThreadExport(raw, threadID)
}

func (a *Agent) completeInputReply(reply *ampInputReply, completion agent.MessageCompletion) {
	if reply == nil {
		return
	}
	if completion != agent.MessageCompletionComplete {
		if !reply.startsWithSteer {
			a.retireInputReply(reply.receipt)
		}
		return
	}
	if reply.unproved {
		a.retireReplyInputs(reply)
		return
	}
	// Keep history reads off the output reader. Later native turns can start
	// while this submission waits for its own export to become visible.
	go a.publishInputReply(reply)
}

func (a *Agent) publishInputReply(reply *ampInputReply) {
	ctx, cancel := a.exportContext()
	defer cancel()
	for attempt := 0; attempt < ampExportAttempts; attempt++ {
		var history ampReplyHistory
		var err error
		if reply.settledHistory == nil {
			history, err = a.readReplyHistory(ctx, reply.receipt)
		} else {
			history = *reply.settledHistory
		}
		if err != nil || history.conflict || a.discarding() {
			break
		}
		if history.ended {
			if a.retainReplyProjection(reply, history) {
				return
			}
			break
		}
		if attempt+1 == ampExportAttempts || !a.waitExportInterval(ctx) {
			break
		}
	}
	a.retireReplyInputs(reply)
}

func (a *Agent) retainReplyProjection(reply *ampInputReply, history ampReplyHistory) bool {
	turn, ok := reply.project(history)
	if !ok {
		return false
	}
	if err := a.sink.PublishNativeReplyTurn(turn); !agent.OutputRetained(err) {
		slog.Warn("retain the Amp exact reply", "agent_id", a.agentID, "error", err)
		return false
	}
	return true
}

func (a *Agent) retireReplyInputs(reply *ampInputReply) {
	a.retireInputReply(reply.receipt)
	for _, receipt := range reply.steers {
		a.retireInputReply(receipt)
	}
}

func (a *Agent) retireInputReply(receipt agent.NativeInputReceipt) {
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	if err := a.sink.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Warn("retain the Amp input outcome", "agent_id", a.agentID, "error", err)
	}
}

func (a *Agent) bindReplyPermission(ctx context.Context, control agent.ControlRequest, toolUseID string) {
	a.mu.Lock()
	reply := a.turn.reply
	if reply == nil || reply.unproved || reply.receipt.SessionID != control.AgentSessionID || toolUseID == "" {
		a.mu.Unlock()
		return
	}
	receipt := reply.receipt
	steers := make(map[string]bool, len(reply.steers))
	for _, steer := range reply.steers {
		steers[steer.InputID] = true
	}
	a.mu.Unlock()
	go func() {
		for attempt := 0; attempt < ampExportAttempts && ctx.Err() == nil; attempt++ {
			history, err := a.readReplyHistory(ctx, receipt)
			if err != nil || history.conflict || history.ended {
				return
			}
			if frame, matched := history.toolFrame(toolUseID); matched {
				inputIDs := []string{receipt.InputID}
				for _, boundary := range history.steers {
					if steers[boundary.inputID] && boundary.afterFrame <= frame {
						inputIDs = append(inputIDs, boundary.inputID)
					}
				}
				if err := a.sink.SetNativeControlInputs(agent.NativeControlInputs{
					SessionID: receipt.SessionID, RequestID: control.RequestID, InstanceID: control.InstanceID, InputIDs: inputIDs,
				}); !agent.OutputRetained(err) {
					slog.Warn("retain the Amp permission's input", "agent_id", a.agentID, "error", err)
				}
				return
			}
			if attempt+1 == ampExportAttempts || !a.waitExportInterval(ctx) {
				return
			}
		}
	}()
}

func replyTextMessageKey(receipt agent.NativeInputReceipt, frame, block int) string {
	return fmt.Sprintf("amp-live:%s:%d:%d", receipt.InputID, frame, block)
}
