package kilo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The daemon and ACP output have separate readers. A daemon response can
// finish before the matching ACP text reaches the transcript.
const nativeReplyOutputTimeout = 3 * time.Second

// nativePermissionLookupTimeout caps the synchronous daemon read performed on
// the ACP reader goroutine when attributing a permission request to a native turn.
const nativePermissionLookupTimeout = 250 * time.Millisecond

// One native assistant message can produce several assembled transcript rows.
// The prefix identifies the native message and the suffix distinguishes its rows.
func kiloAssistantTextKey(messageID string) string {
	return "kilo-reply:" + strconv.Quote(messageID) + ":" + id.Generate()
}

type kiloNativeMessage struct {
	Info struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		ParentID  string `json:"parentID"`
		Role      string `json:"role"`
		Finish    string `json:"finish"`
		Error     *struct {
			Name string `json:"name"`
		} `json:"error"`
	} `json:"info"`
	Parts []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Ignored bool   `json:"ignored"`
		CallID  string `json:"callID"`
	} `json:"parts"`
}

func (m kiloNativeMessage) isAssistantReply(sessionID, inputID string) bool {
	return m.Info.ID != "" && m.Info.SessionID == sessionID && m.Info.Role == "assistant" && m.Info.ParentID == inputID
}

type kiloReplySegment struct {
	key  string
	text string
}

// kiloReplyTarget retains the transcript that admitted an input. A parent may
// finish its child and close the live route before the daemon answers.
type kiloReplyTarget struct {
	receipt       agent.NativeInputReceipt
	rootSessionID string
	transcript    agent.ProviderServices
	flush         func(map[string]bool)
}

type kiloToolOwnerKey struct {
	sessionID  string
	toolCallID string
}

type kiloExpectedReply struct {
	messageID string
	text      string
}

type kiloNativeReplies struct {
	mu          sync.Mutex
	inputs      map[string]agent.NativeInputReceipt
	text        map[string][]kiloReplySegment
	toolOwners  map[kiloToolOwnerKey]string
	textChanged chan struct{}
}

func (r *kiloNativeReplies) add(messageID string, receipt agent.NativeInputReceipt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inputs == nil {
		r.inputs = make(map[string]agent.NativeInputReceipt)
		r.text = make(map[string][]kiloReplySegment)
	}
	r.inputs[messageID] = receipt
}

func (r *kiloNativeReplies) remove(messageID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inputs, messageID)
	if len(r.inputs) == 0 {
		r.inputs, r.text, r.toolOwners = nil, nil, nil
	}
}

func (r *kiloNativeReplies) pruneText(messageIDs map[string]bool) {
	if len(messageIDs) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range messageIDs {
		delete(r.text, id)
	}
}

func (r *kiloNativeReplies) textSignal() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.textChanged == nil {
		r.textChanged = make(chan struct{})
	}
	return r.textChanged
}

func (r *kiloNativeReplies) recordText(messageID, key, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.inputs) == 0 {
		return
	}
	for _, recorded := range r.text[messageID] {
		if recorded.key == key {
			return
		}
	}
	r.text[messageID] = append(r.text[messageID], kiloReplySegment{key: key, text: text})
	if r.textChanged != nil {
		close(r.textChanged)
		r.textChanged = nil
	}
}

func kiloExpectedReplies(messages []kiloNativeMessage) []kiloExpectedReply {
	expected := make([]kiloExpectedReply, len(messages))
	for i, message := range messages {
		var native strings.Builder
		for _, part := range message.Parts {
			if part.Type == "text" && !part.Ignored {
				native.WriteString(part.Text)
			}
		}
		expected[i] = kiloExpectedReply{messageID: message.Info.ID, text: native.String()}
	}
	return expected
}

func (r *kiloNativeReplies) replyItems(messages []kiloNativeMessage) ([]agent.NativeReplyItem, bool) {
	return r.replyItemsExpected(kiloExpectedReplies(messages))
}

func (r *kiloNativeReplies) replyItemsExpected(expected []kiloExpectedReply) ([]agent.NativeReplyItem, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var items []agent.NativeReplyItem
	for _, entry := range expected {
		var captured strings.Builder
		for _, segment := range r.text[entry.messageID] {
			captured.WriteString(segment.text)
			items = append(items, agent.NativeReplyItem{Ordinal: int64(len(items)) + 2, MessageKey: segment.key})
		}
		if entry.text != captured.String() {
			return nil, false
		}
	}
	return items, true
}

func (a *Agent) readNativeMessage(ctx context.Context, sessionID, messageID string) (kiloNativeMessage, error) {
	var message kiloNativeMessage
	raw, err := a.Daemon().Get(ctx, a.nativeMessageRoute(sessionID, messageID))
	if err != nil {
		return message, err
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return message, err
	}
	if message.Info.SessionID != sessionID || message.Info.ID != messageID {
		return message, errors.New("the Kilo daemon returned a different message")
	}
	return message, nil
}

func (a *Agent) nativeQuestionTurnID(ctx context.Context, sessionID, assistantID string) string {
	ctx, cancel := context.WithTimeout(ctx, nativeReplyOutputTimeout)
	defer cancel()
	message, err := a.readNativeMessage(ctx, sessionID, assistantID)
	if err != nil || message.Info.Role != "assistant" {
		return ""
	}
	return a.nativeReplies.turnID(sessionID, message.Info.ParentID)
}

func (r *kiloNativeReplies) turnID(sessionID, parentID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	receipt := r.inputs[parentID]
	if receipt.SessionID != sessionID {
		return ""
	}
	return receipt.TurnID
}

func (a *Agent) nativePermissionTurnID(sessionID, toolCallID string) string {
	a.nativeReplies.mu.Lock()
	var active bool
	for _, receipt := range a.nativeReplies.inputs {
		if receipt.SessionID == sessionID {
			active = true
			break
		}
	}
	if !active {
		a.nativeReplies.mu.Unlock()
		return ""
	}
	if cached, found := a.nativeReplies.toolOwners[kiloToolOwnerKey{sessionID: sessionID, toolCallID: toolCallID}]; found {
		receipt := a.nativeReplies.inputs[cached]
		a.nativeReplies.mu.Unlock()
		if cached == "" || receipt.SessionID != sessionID {
			return ""
		}
		return receipt.TurnID
	}
	a.nativeReplies.mu.Unlock()

	ctx, cancel := context.WithTimeout(a.Context(), nativePermissionLookupTimeout)
	defer cancel()
	messages, _, err := a.readNativeMessagePage(ctx, sessionID, "")
	if err != nil {
		return ""
	}
	return a.nativeReplies.permissionTurnID(sessionID, toolCallID, messages)
}

func (r *kiloNativeReplies) permissionTurnID(sessionID, toolCallID string, messages []kiloNativeMessage) string {
	var owner string
	ambiguous := false
	for _, message := range messages {
		if message.Info.SessionID != sessionID || message.Info.Role != "assistant" || message.Info.ID == "" {
			continue
		}
		for _, part := range message.Parts {
			if part.Type == "tool" && part.CallID == toolCallID {
				if owner != "" || message.Info.ParentID == "" {
					ambiguous = true
					break
				}
				owner = message.Info.ParentID
			}
		}
		if ambiguous {
			break
		}
	}
	if ambiguous {
		owner = ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.inputs) > 0 && (owner != "" || ambiguous) {
		if r.toolOwners == nil {
			r.toolOwners = make(map[kiloToolOwnerKey]string)
		}
		r.toolOwners[kiloToolOwnerKey{sessionID: sessionID, toolCallID: toolCallID}] = owner
	}
	if owner == "" {
		return ""
	}
	receipt := r.inputs[owner]
	if receipt.SessionID != sessionID {
		return ""
	}
	return receipt.TurnID
}

func (a *Agent) readNativeMessagePage(ctx context.Context, sessionID, before string) ([]kiloNativeMessage, string, error) {
	query := url.Values{"limit": {"64"}}
	if before != "" {
		query.Set("before", before)
	}
	raw, next, err := a.Daemon().GetPage(ctx, a.nativeMessageRoute(sessionID, "")+"&"+query.Encode())
	if err != nil {
		return nil, "", err
	}
	var messages []kiloNativeMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, "", err
	}
	return messages, next, nil
}

func kiloReplyHistory(sessionID, inputID, finalID string, page func(string) ([]kiloNativeMessage, string, error)) ([]kiloNativeMessage, error) {
	var answer []kiloNativeMessage
	foundFinal := false
	seen := make(map[string]bool)
	seenMessages := make(map[string]bool)
	for cursor := ""; !seen[cursor]; {
		seen[cursor] = true
		messages, next, err := page(cursor)
		if err != nil {
			return nil, err
		}
		// The daemon returns ascending order within each page. Pages move
		// backwards, so collect in reverse until the native input is found.
		for index := len(messages) - 1; index >= 0; index-- {
			message := messages[index]
			if message.Info.SessionID != sessionID {
				return nil, errors.New("the Kilo history contains a message from another session")
			}
			if message.Info.ID == "" || seenMessages[message.Info.ID] {
				return nil, errors.New("the Kilo history repeats or omits a message ID")
			}
			seenMessages[message.Info.ID] = true
			if message.Info.ID == inputID && message.Info.Role == "user" {
				if !foundFinal {
					return nil, errors.New("the Kilo history did not contain the final reply")
				}
				slices.Reverse(answer)
				return answer, nil
			}
			if message.isAssistantReply(sessionID, inputID) {
				foundFinal = foundFinal || message.Info.ID == finalID
				if foundFinal {
					answer = append(answer, message)
				}
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return nil, errors.New("the Kilo history did not contain the native input")
}

func (a *Agent) publishNativeReply(target kiloReplyTarget, inputID, finalID string) error {
	receipt := target.receipt
	ctx, cancel := context.WithTimeout(a.Context(), nativeReplyOutputTimeout)
	defer cancel()
	messages, err := kiloReplyHistory(receipt.SessionID, inputID, finalID, func(before string) ([]kiloNativeMessage, string, error) {
		return a.readNativeMessagePage(ctx, receipt.SessionID, before)
	})
	if err != nil {
		return err
	}
	messageIDs := make(map[string]bool, len(messages))
	for _, message := range messages {
		messageIDs[message.Info.ID] = true
	}
	expected := kiloExpectedReplies(messages)
	ticker := a.Clock().NewTicker(nativeAdmissionInterval)
	defer ticker.Stop()
	for {
		if a.CurrentSessionID() != target.rootSessionID || a.IsDiscardingOutput() {
			return agent.ErrInputSessionChanged
		}
		changed := a.nativeReplies.textSignal()
		target.flush(messageIDs)
		if items, ready := a.nativeReplies.replyItemsExpected(expected); ready {
			err := target.transcript.PublishNativeReplyTurn(agent.NativeReplyTurn{
				SessionID: receipt.SessionID, TurnID: receipt.TurnID,
				Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
				Items:   items,
			})
			if agent.OutputRetained(err) {
				a.nativeReplies.pruneText(messageIDs)
				return nil
			}
			return err
		}
		select {
		case <-changed:
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("the Kilo transcript did not confirm all native reply text: %w", ctx.Err())
		}
	}
}

func (a *Agent) finishNativePrompt(target kiloReplyTarget, messageID string, result kiloPromptResult, finish func(json.RawMessage, error)) {
	receipt := target.receipt
	defer a.nativeReplies.remove(messageID)
	outcome := leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
	response := json.RawMessage(`{"stopReason":"end_turn"}`)
	switch {
	case result.err != nil:
	case !result.message.isAssistantReply(receipt.SessionID, messageID):
		result.err = errors.New("the Kilo daemon did not identify the input that owns the reply")
	case result.message.Info.Error != nil:
		outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
		if result.message.Info.Error.Name == "MessageAbortedError" {
			outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
			response = json.RawMessage(`{"stopReason":"cancelled"}`)
		} else {
			result.err = fmt.Errorf("the Kilo prompt failed: %s", result.message.Info.Error.Name)
		}
	case result.message.Info.Finish != "":
		if err := a.publishNativeReply(target, messageID, result.message.Info.ID); err == nil {
			finish(response, nil)
			return
		} else {
			slog.Debug("verify Kilo reply text", "agent_id", a.AgentID(), "error", err)
		}
	}
	if err := target.transcript.RetireNativeInputTurn(receipt, outcome); !agent.OutputRetained(err) {
		slog.Error("record the Kilo reply outcome", "agent_id", a.AgentID(), "error", err)
	}
	if result.err != nil {
		response = nil
	}
	finish(response, result.err)
}
