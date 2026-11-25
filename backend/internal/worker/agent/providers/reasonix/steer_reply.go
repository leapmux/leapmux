package reasonix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// reasonixMidTurnSteerPrefix matches Reasonix's internal/agent.MidTurnSteerPrefix
// written into committed mid-turn steer user messages.
const reasonixMidTurnSteerPrefix = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]"

const reasonixReplyMessageID = "reasonix:reply"
const reasonixSteerAnswerPrefix = "reasonix-steer-reply:"

type reasonixSteerReplies struct {
	mu        sync.Mutex
	active    *reasonixSteerTurn
	capturing *reasonixSteerTurn
	turns     map[string]*reasonixSteerTurn
}

type reasonixSteerTurn struct {
	id        string
	sessionID string
	cursor    int
	inputs    []reasonixSteerInput
	answers   []reasonixACPAnswer
}

type reasonixSteerInput struct {
	receipt     agent.NativeInputReceipt
	disposition string
	text        string
}

type reasonixACPAnswer struct {
	key, text string
}

// SteerInput sends `_reasonix.io/session/steer` and records the durable
// receipt plus the pre-steer journal cursor so prompt-end reconciliation can
// prove exact post-steer reply ownership from Reasonix's committed session
// transcript.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	var receipt agent.NativeInputReceipt
	a.steerDispatchMu.Lock()
	defer a.steerDispatchMu.Unlock()

	method, active, sessionID, _ := a.SteerTarget()
	if method == "" {
		return receipt, agent.ErrSteeringUnsupported
	}
	if !active || sessionID == "" {
		return receipt, agent.ErrNoActiveTurn
	}
	params, err := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"prompt":    acp.BuildPromptBlocks(request.Content, agent.ClassifyAttachments(request.Attachments)),
	})
	if err != nil {
		return receipt, fmt.Errorf("marshal Reasonix ACP steer params: %w", err)
	}

	var cursor int
	track := false
	if request.InputID != "" {
		msgs, readErr := readReasonixSessionMessages(context.Background(), a.storeQuery, sessionID, a.workingDir)
		switch {
		case readErr == nil:
			cursor, track = len(msgs), true
		case errors.Is(readErr, os.ErrNotExist):
			cursor, track = 0, true
		}
	}

	var dispErr error
	_, err = a.SendRequestObserved(method, params, a.APITimeout(), func(response json.RawMessage, replyErr error) {
		if replyErr != nil {
			return
		}
		if dErr := readReasonixSteerDisposition(response, ""); dErr != nil {
			dispErr = dErr
			return
		}
		if !track {
			return
		}
		var reply struct {
			ItemID      string `json:"itemId"`
			Disposition string `json:"disposition"`
		}
		_ = json.Unmarshal(response, &reply)
		nativeID := strings.TrimSpace(reply.ItemID)
		if nativeID == "" {
			nativeID = id.Generate()
		}
		receipt = agent.NativeInputReceipt{SessionID: sessionID, InputID: nativeID}
		a.steerReplies.admit(reasonixSteerInput{
			receipt:     receipt,
			disposition: reply.Disposition,
			text:        request.Content,
		}, cursor)
	})
	if err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
			return agent.NativeInputReceipt{}, agent.ErrNoActiveTurn
		}
		return agent.NativeInputReceipt{}, providerkit.ClassifyJSONRPCDeliveryError(method, err)
	}
	if dispErr != nil {
		return agent.NativeInputReceipt{}, dispErr
	}
	return receipt, nil
}

func (r *reasonixSteerReplies) admit(input reasonixSteerInput, cursor int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		turn := &reasonixSteerTurn{
			id:        id.Generate(),
			sessionID: input.receipt.SessionID,
			cursor:    cursor,
		}
		if r.turns == nil {
			r.turns = make(map[string]*reasonixSteerTurn)
		}
		r.turns[turn.id] = turn
		r.active = turn
	}
	if r.active.sessionID == input.receipt.SessionID {
		r.active.inputs = append(r.active.inputs, input)
	}
}

func (a *Agent) reasonixChunkMessageID(kind agent.AssembledMessageKind, _ map[string]json.RawMessage) string {
	if kind != agent.AssembledMessageKindText {
		return ""
	}
	return reasonixReplyMessageID
}

func (a *Agent) reasonixAssistantTextKey(messageID string) string {
	if messageID != reasonixReplyMessageID {
		return ""
	}
	a.steerReplies.mu.Lock()
	turn := a.steerReplies.active
	if turn == nil {
		turn = a.steerReplies.capturing
	}
	var turnID string
	if turn != nil {
		turnID = turn.id
	}
	a.steerReplies.mu.Unlock()
	if turnID == "" {
		return ""
	}
	return reasonixSteerAnswerPrefix + turnID + ":" + id.Generate()
}

func (a *Agent) reasonixAssistantTextPrepared(messageID, key, text string) {
	if messageID != reasonixReplyMessageID {
		return
	}
	rest, ok := strings.CutPrefix(key, reasonixSteerAnswerPrefix)
	if !ok {
		return
	}
	turnID, _, ok := strings.Cut(rest, ":")
	if !ok || turnID == "" {
		return
	}
	a.steerReplies.mu.Lock()
	if turn := a.steerReplies.turns[turnID]; turn != nil {
		turn.answers = append(turn.answers, reasonixACPAnswer{key: key, text: text})
	}
	a.steerReplies.mu.Unlock()
}

func (a *Agent) captureReasonixSteerCompletion(sink agent.ProviderServices) func() error {
	a.steerReplies.mu.Lock()
	turn := a.steerReplies.active
	a.steerReplies.active = nil
	if turn != nil {
		a.steerReplies.capturing = turn
	}
	a.steerReplies.mu.Unlock()
	if turn == nil {
		return nil
	}
	return func() error {
		a.steerReplies.mu.Lock()
		if a.steerReplies.turns != nil {
			delete(a.steerReplies.turns, turn.id)
		}
		if a.steerReplies.capturing == turn {
			a.steerReplies.capturing = nil
		}
		answers := append([]reasonixACPAnswer(nil), turn.answers...)
		a.steerReplies.mu.Unlock()
		return a.reconcileReasonixSteerReplies(sink, turn, answers)
	}
}

func (a *Agent) retireReasonixSteers(sink agent.ProviderServices) {
	a.steerReplies.mu.Lock()
	var pending []*reasonixSteerTurn
	for _, turn := range a.steerReplies.turns {
		pending = append(pending, turn)
	}
	a.steerReplies.active = nil
	a.steerReplies.capturing = nil
	a.steerReplies.turns = nil
	a.steerReplies.mu.Unlock()
	for _, turn := range pending {
		for _, input := range turn.inputs {
			if err := sink.RetireNativeInputTurn(input.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil && !agent.OutputRetained(err) {
				slog.Warn("retire Reasonix steer reply", "agent_id", a.AgentID(), "input_id", input.receipt.InputID, "error", err)
			}
		}
	}
}

type reasonixJournalMessage struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Origin     string `json:"origin"`
	Content    string `json:"content"`
	RawContent string `json:"raw_content"`
	LocalOnly  bool   `json:"local_only"`
}

type reasonixJournalAssistant struct {
	index int
	id    string
	text  string
}

func (a *Agent) reconcileReasonixSteerReplies(sink agent.ProviderServices, turn *reasonixSteerTurn, answers []reasonixACPAnswer) error {
	messages, err := readReasonixSessionMessages(context.Background(), a.storeQuery, turn.sessionID, a.workingDir)
	if err != nil || len(messages) <= turn.cursor {
		return retireUnprovedReasonixInputs(sink, turn.inputs, nil)
	}
	delta := messages[turn.cursor:]
	proved := make(map[string]bool)

	// Walk committed messages after the pre-steer cursor and associate each
	// committed steer/follow-up input with the assistant replies that follow it
	// within its native turn.
	type matchedSteer struct {
		input      reasonixSteerInput
		followUp   bool
		afterIndex int
		turnID     string
	}
	var matched []matchedSteer
	remaining := append([]reasonixSteerInput(nil), turn.inputs...)

	var assistants []reasonixJournalAssistant

	for idx, msg := range delta {
		if msg.LocalOnly {
			continue
		}
		switch msg.Role {
		case "user":
			steerText, isSteer := extractReasonixSteerText(msg.Content, msg.RawContent)
			for i, candidate := range remaining {
				if isSteer && candidate.disposition != "queued_followup" && strings.TrimSpace(steerText) == strings.TrimSpace(candidate.text) {
					turnID := msg.ID
					if turnID == "" {
						turnID = "reasonix-steer-" + candidate.receipt.InputID
					}
					matched = append(matched, matchedSteer{
						input:      candidate,
						followUp:   false,
						afterIndex: idx,
						turnID:     turnID,
					})
					remaining = append(remaining[:i], remaining[i+1:]...)
					break
				}
				if !isSteer && candidate.disposition == "queued_followup" && reasonixUserTextMatches(msg, candidate.text) {
					turnID := msg.ID
					if turnID == "" {
						turnID = "reasonix-followup-" + candidate.receipt.InputID
					}
					matched = append(matched, matchedSteer{
						input:      candidate,
						followUp:   true,
						afterIndex: idx,
						turnID:     turnID,
					})
					remaining = append(remaining[:i], remaining[i+1:]...)
					break
				}
			}
		case "assistant":
			text := strings.TrimSpace(msg.Content)
			if text != "" {
				assistants = append(assistants, reasonixJournalAssistant{index: idx, id: msg.ID, text: text})
			}
		}
	}

	// Match committed assistant messages to stored ACP answer rows from the back
	// (because PrepareSteer may have flushed pre-steer text before SteerInput).
	matchedKeys := matchReasonixAnswersToJournal(assistants, answers)

	for i, steer := range matched {
		nextFollowUpIndex := len(delta)
		for _, later := range matched[i+1:] {
			if later.followUp {
				nextFollowUpIndex = later.afterIndex
				break
			}
		}
		var items []agent.NativeReplyItem
		var ordinal int64 = 2
		for j, assistant := range assistants {
			if assistant.index <= steer.afterIndex || assistant.index >= nextFollowUpIndex {
				continue
			}
			keys := matchedKeys[j]
			for _, key := range keys {
				stored, readErr := sink.ReadMessageByIdempotencyKey(turn.sessionID, key)
				if readErr != nil {
					return readErr
				}
				if stored == nil || stored.Content.Completion == agent.MessageCompletionInterrupted || stored.Content.Completion == agent.MessageCompletionError {
					continue
				}
				items = append(items, agent.NativeReplyItem{Ordinal: ordinal, MessageKey: key})
				ordinal++
			}
		}
		if len(items) == 0 {
			continue
		}
		if err := sink.PublishNativeReplyTurn(agent.NativeReplyTurn{
			SessionID:      turn.sessionID,
			TurnID:         steer.turnID,
			Outcome:        leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Items:          items,
			Inputs:         []agent.NativeReplyBoundary{{InputID: steer.input.receipt.InputID, AfterOrdinal: 1}},
			ExclusiveItems: steer.followUp,
		}); err != nil {
			return fmt.Errorf("store the Reasonix steer reply %q: %w", steer.turnID, err)
		}
		proved[steer.input.receipt.InputID] = true
	}

	return retireUnprovedReasonixInputs(sink, turn.inputs, proved)
}

func retireUnprovedReasonixInputs(sink agent.ProviderServices, inputs []reasonixSteerInput, proved map[string]bool) error {
	for _, input := range inputs {
		if proved[input.receipt.InputID] {
			continue
		}
		if err := sink.RetireNativeInputTurn(input.receipt, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED); err != nil {
			return fmt.Errorf("retire the Reasonix steer reply %q: %w", input.receipt.InputID, err)
		}
	}
	return nil
}

func matchReasonixAnswersToJournal(assistants []reasonixJournalAssistant, answers []reasonixACPAnswer) [][]string {
	result := make([][]string, len(assistants))
	ansIdx := len(answers) - 1
	for i := len(assistants) - 1; i >= 0 && ansIdx >= 0; i-- {
		target := strings.TrimSpace(assistants[i].text)
		// Exact single-row match first.
		if strings.TrimSpace(answers[ansIdx].text) == target {
			result[i] = []string{answers[ansIdx].key}
			ansIdx--
			continue
		}
		// Allow adjacent PrepareSteer-split rows whose combined text equals target.
		var combined string
		var keys []string
		matchedSplit := false
		for k := ansIdx; k >= 0; k-- {
			if combined == "" {
				combined = answers[k].text
			} else {
				combined = answers[k].text + combined
			}
			keys = append([]string{answers[k].key}, keys...)
			if strings.TrimSpace(combined) == target {
				result[i] = keys
				ansIdx = k - 1
				matchedSplit = true
				break
			}
			if len(combined) > len(target)+64 {
				break
			}
		}
		if !matchedSplit {
			// Skip an unmatched assistant entry (for example pre-steer text flushed
			// before SteerInput ran).
			continue
		}
	}
	return result
}

func extractReasonixSteerText(content, rawContent string) (string, bool) {
	s := content
	for {
		trimmed := strings.TrimLeft(s, " \t\r\n")
		if after, found := strings.CutPrefix(trimmed, reasonixMidTurnSteerPrefix); found {
			if strings.TrimSpace(rawContent) != "" {
				return rawContent, true
			}
			return strings.TrimPrefix(after, "\n"), true
		}
		advanced := false
		for _, tag := range []string{
			"workspace",
			"current-date",
			"session-scratch",
			"available-skills",
			"project-instructions",
			"response-language",
			"reasoning-language",
		} {
			openPrefix := "<" + tag
			if !strings.HasPrefix(trimmed, openPrefix) || len(trimmed) <= len(openPrefix) {
				continue
			}
			next := trimmed[len(openPrefix)]
			if next != '>' && next != ' ' && next != '\t' && next != '\n' {
				continue
			}
			closeTag := "</" + tag + ">"
			if end := strings.Index(trimmed, closeTag); end >= 0 {
				s = trimmed[end+len(closeTag):]
				advanced = true
				break
			}
		}
		if !advanced {
			return "", false
		}
	}
}

func reasonixUserTextMatches(msg reasonixJournalMessage, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	if strings.TrimSpace(msg.RawContent) == want {
		return true
	}
	return strings.TrimSpace(msg.Content) == want
}

func readReasonixSessionMessages(ctx context.Context, q agent.StoredSessionQuery, sessionID, workingDir string) ([]reasonixJournalMessage, error) {
	if v4Dir := reasonixV4SessionDir(q, sessionID, workingDir); v4Dir != "" {
		return readReasonixV4SessionMessages(ctx, v4Dir, sessionID)
	}
	path := reasonixToolStorePath(q, sessionID, workingDir)
	if path == "" {
		return nil, os.ErrNotExist
	}
	eventPath := strings.TrimSuffix(path, ".jsonl") + ".events.jsonl"
	if msgs, err := readReasonixEventsBranchMessages(ctx, eventPath); err == nil {
		return msgs, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the Reasonix transcript is not a regular file")
	}
	var out []reasonixJournalMessage
	_, err = readReasonixJSONL(ctx, file, 0, info.Size(), func(raw json.RawMessage, _ int64, _ int) error {
		var msg reasonixJournalMessage
		if json.Unmarshal(raw, &msg) == nil && msg.Role != "" {
			out = append(out, msg)
		}
		return nil
	})
	return out, err
}

func readReasonixEventsBranchMessages(ctx context.Context, eventPath string) ([]reasonixJournalMessage, error) {
	file, err := os.Open(eventPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the Reasonix event log is not a regular file")
	}
	graph := newReasonixToolGraph()
	bodies := make(map[string]json.RawMessage)
	var (
		linear    []reasonixJournalMessage
		sawLinear bool
		sawDAG    bool
	)
	_, err = readReasonixJSONL(ctx, file, 0, info.Size(), func(raw json.RawMessage, offset int64, length int) error {
		var ev reasonixToolEvent
		if json.Unmarshal(raw, &ev) != nil {
			return nil
		}
		if len(ev.Messages) == 0 && len(ev.LegacyMessages) > 0 {
			ev.Messages = ev.LegacyMessages
		}
		switch ev.Type {
		case "replace":
			sawLinear = true
			linear = linear[:0]
			for _, rawMsg := range ev.Messages {
				var msg reasonixJournalMessage
				if json.Unmarshal(rawMsg, &msg) == nil && msg.Role != "" {
					linear = append(linear, msg)
				}
			}
			return nil
		case "append":
			sawLinear = true
			if ev.MessageIndex != len(linear) {
				return fmt.Errorf("the Reasonix append position %d does not match its history length %d", ev.MessageIndex, len(linear))
			}
			for _, rawMsg := range ev.Messages {
				var msg reasonixJournalMessage
				if json.Unmarshal(rawMsg, &msg) == nil && msg.Role != "" {
					linear = append(linear, msg)
				}
			}
			return nil
		}
		if err := graph.apply(ev, reasonixMessageLocation{offset: offset, length: length}); err != nil {
			return nil
		}
		switch ev.Type {
		case "message":
			sawDAG = true
			if ev.ID != "" && len(ev.Messages) == 1 {
				bodies[ev.ID] = ev.Messages[0]
			}
		case "patch":
			sawDAG = true
			if ev.Target != "" && len(ev.Messages) == 1 {
				bodies[ev.Target] = ev.Messages[0]
			}
		case "redact":
			sawDAG = true
			for id, msgs := range ev.Targets {
				if len(msgs) == 1 {
					bodies[id] = msgs[0]
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if sawLinear && !sawDAG {
		return linear, nil
	}
	head := graph.selectedHead()
	if head == nil {
		return nil, nil
	}
	leaf := head.leaf
	var chain []reasonixJournalMessage
	visited := make(map[string]bool)
	for cur := leaf; cur != "" && !visited[cur]; {
		visited[cur] = true
		node, ok := graph.nodes[cur]
		if !ok {
			break
		}
		if raw := bodies[cur]; len(raw) > 0 {
			var msg reasonixJournalMessage
			if json.Unmarshal(raw, &msg) == nil && msg.Role != "" {
				chain = append(chain, msg)
			}
		}
		cur = node.parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}
