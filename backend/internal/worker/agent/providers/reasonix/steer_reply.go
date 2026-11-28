package reasonix

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
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
	mu      sync.Mutex
	nextSeq uint64
	active  *reasonixSteerTurn
	turns   map[string]*reasonixSteerTurn
}

type reasonixSteerTurn struct {
	id           string
	seq          uint64
	sessionID    string
	cursor       int
	prefixDigest [sha256.Size]byte
	retired      bool
	inputs       []reasonixSteerInput
	answers      []reasonixACPAnswer
}

type reasonixSteerInput struct {
	receipt     agent.NativeInputReceipt
	disposition string
	text        string
}

type reasonixACPAnswer struct {
	key, text string
}

func (a *Agent) reasonixContext() context.Context {
	if ctx := a.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func (r *reasonixSteerReplies) hasActiveSession(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active != nil && r.active.sessionID == sessionID
}

// SteerInput sends `_reasonix.io/session/steer` and records the durable
// receipt plus the pre-steer journal cursor so prompt-end reconciliation can
// prove exact post-steer reply ownership from Reasonix's committed session
// transcript.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	var receipt agent.NativeInputReceipt
	a.steerDispatchMu.Lock()
	defer a.steerDispatchMu.Unlock()

	err := a.WithSessionID(func(sessionID string) error {
		method, active, _, _ := a.SteerTarget()
		if method == "" {
			return agent.ErrSteeringUnsupported
		}
		if !active || sessionID == "" {
			return agent.ErrNoActiveTurn
		}
		params, err := json.Marshal(map[string]any{
			"sessionId": sessionID,
			"prompt":    acp.BuildPromptBlocks(request.Content, agent.ClassifyAttachments(request.Attachments)),
		})
		if err != nil {
			return fmt.Errorf("marshal Reasonix ACP steer params: %w", err)
		}

		var cursor int
		var prefixDigest [sha256.Size]byte
		track := false
		if request.InputID != "" {
			if a.steerReplies.hasActiveSession(sessionID) {
				track = true
			} else {
				msgs, readErr := readReasonixSessionMessages(a.reasonixContext(), a.storeQuery, sessionID, a.workingDir)
				switch {
				case readErr == nil:
					cursor, prefixDigest, track = len(msgs), reasonixMessagesDigest(msgs), true
				case errors.Is(readErr, os.ErrNotExist):
					cursor, prefixDigest, track = 0, reasonixMessagesDigest(nil), true
				}
			}
		}

		var dispErr error
		var postTurnEnded bool
		_, err = a.SendRequestObserved(method, params, a.APITimeout(), func(response json.RawMessage, replyErr error) {
			if replyErr != nil {
				return
			}
			if dErr := readReasonixSteerDisposition(response, ""); dErr != nil {
				dispErr = dErr
				return
			}
			if !a.PromptActive() {
				postTurnEnded = true
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
			candidate := agent.NativeInputReceipt{SessionID: sessionID, InputID: nativeID}
			if a.steerReplies.admit(reasonixSteerInput{
				receipt:     candidate,
				disposition: reply.Disposition,
				text:        request.Content,
			}, cursor, prefixDigest) {
				receipt = candidate
			}
		})
		if err != nil {
			if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
				return agent.ErrNoActiveTurn
			}
			return providerkit.ClassifyJSONRPCDeliveryError(method, err)
		}
		if dispErr != nil {
			return dispErr
		}
		if postTurnEnded {
			return agent.ErrNoActiveTurn
		}
		return nil
	})
	if err != nil {
		return agent.NativeInputReceipt{}, err
	}
	return receipt, nil
}

func (r *reasonixSteerReplies) admit(input reasonixSteerInput, cursor int, prefixDigest [sha256.Size]byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.nextSeq++
		turn := &reasonixSteerTurn{
			id:           id.Generate(),
			seq:          r.nextSeq,
			sessionID:    input.receipt.SessionID,
			cursor:       cursor,
			prefixDigest: prefixDigest,
		}
		if r.turns == nil {
			r.turns = make(map[string]*reasonixSteerTurn)
		}
		r.turns[turn.id] = turn
		r.active = turn
	}
	if r.active.sessionID != input.receipt.SessionID {
		return false
	}
	r.active.inputs = append(r.active.inputs, input)
	return true
}

func (a *Agent) reasonixUpdateMessageID(info acp.UpdateMessageInfo) string {
	if info.RowKey != "" || info.UpdateType != contracts.ACPUpdateAgentMessageChunk {
		return ""
	}
	return reasonixReplyMessageID
}

func (a *Agent) reasonixAssistantTextKey(messageID string) string {
	if messageID != reasonixReplyMessageID {
		return ""
	}
	a.steerReplies.mu.Lock()
	var turnID string
	if a.steerReplies.active != nil {
		turnID = a.steerReplies.active.id
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

func (a *Agent) captureReasonixSteerCompletion(sink agent.ProviderServices) func(agent.MessageCompletion, bool) error {
	a.steerReplies.mu.Lock()
	turn := a.steerReplies.active
	a.steerReplies.active = nil
	a.steerReplies.mu.Unlock()
	if turn == nil {
		return nil
	}
	return func(completion agent.MessageCompletion, discard bool) error {
		a.steerReplies.mu.Lock()
		if turn.retired {
			a.steerReplies.mu.Unlock()
			return nil
		}
		if a.steerReplies.turns != nil {
			delete(a.steerReplies.turns, turn.id)
		}
		answers := append([]reasonixACPAnswer(nil), turn.answers...)
		a.steerReplies.mu.Unlock()
		return a.reconcileReasonixSteerReplies(sink, turn, answers, completion, discard)
	}
}

func (a *Agent) retireReasonixSteers(sink agent.ProviderServices) {
	a.steerReplies.mu.Lock()
	pending := make([]*reasonixSteerTurn, 0, len(a.steerReplies.turns))
	for _, turn := range a.steerReplies.turns {
		turn.retired = true
		pending = append(pending, turn)
	}
	a.steerReplies.active = nil
	a.steerReplies.turns = nil
	a.steerReplies.mu.Unlock()
	slices.SortFunc(pending, func(left, right *reasonixSteerTurn) int {
		return cmp.Compare(left.seq, right.seq)
	})
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

func reasonixMessagesDigest(messages []reasonixJournalMessage) [sha256.Size]byte {
	hasher := sha256.New()
	var lenBuf [8]byte
	writeString := func(s string) {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
		_, _ = hasher.Write(lenBuf[:])
		_, _ = hasher.Write([]byte(s))
	}
	for _, msg := range messages {
		writeString(msg.ID)
		writeString(msg.Role)
		writeString(msg.Origin)
		writeString(msg.Content)
		writeString(msg.RawContent)
		if msg.LocalOnly {
			_, _ = hasher.Write([]byte{1})
		} else {
			_, _ = hasher.Write([]byte{0})
		}
	}
	var sum [sha256.Size]byte
	hasher.Sum(sum[:0])
	return sum
}

type reasonixJournalAssistant struct {
	index int
	id    string
	text  string
}

func (a *Agent) reconcileReasonixSteerReplies(sink agent.ProviderServices, turn *reasonixSteerTurn, answers []reasonixACPAnswer, completion agent.MessageCompletion, discard bool) error {
	if discard {
		return retireReasonixInputsWithOutcome(sink, turn.inputs, nil, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	switch completion {
	case agent.MessageCompletionInterrupted:
		return retireReasonixInputsWithOutcome(sink, turn.inputs, nil, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED)
	case agent.MessageCompletionError:
		return retireReasonixInputsWithOutcome(sink, turn.inputs, nil, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED)
	case agent.MessageCompletionComplete, agent.MessageCompletionFinished:
	}
	messages, err := readReasonixSessionMessages(a.reasonixContext(), a.storeQuery, turn.sessionID, a.workingDir)
	if err != nil || len(messages) <= turn.cursor || reasonixMessagesDigest(messages[:turn.cursor]) != turn.prefixDigest {
		return retireReasonixInputsWithOutcome(sink, turn.inputs, nil, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
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
	var ordinaryUserIndices []int

	for idx, msg := range delta {
		if msg.LocalOnly {
			continue
		}
		switch msg.Role {
		case "user":
			steerText, isSteer := extractReasonixSteerText(msg.Content, msg.RawContent)
			if !isSteer {
				ordinaryUserIndices = append(ordinaryUserIndices, idx)
			}
			for i, candidate := range remaining {
				followUp := candidate.disposition == "queued_followup"
				var matches bool
				if isSteer && !followUp {
					matches = strings.TrimSpace(steerText) == strings.TrimSpace(candidate.text)
				} else if !isSteer && followUp {
					matches = reasonixUserTextMatches(msg, candidate.text)
				}
				if !matches {
					continue
				}
				turnID := msg.ID
				if turnID == "" {
					prefix := "reasonix-steer-"
					if followUp {
						prefix = "reasonix-followup-"
					}
					turnID = prefix + candidate.receipt.InputID
				}
				matched = append(matched, matchedSteer{
					input:      candidate,
					followUp:   followUp,
					afterIndex: idx,
					turnID:     turnID,
				})
				remaining = append(remaining[:i], remaining[i+1:]...)
				break
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
	validKeys := make(map[string]bool)

	for _, steer := range matched {
		nextFollowUpIndex := len(delta)
		for _, userIdx := range ordinaryUserIndices {
			if userIdx > steer.afterIndex {
				nextFollowUpIndex = userIdx
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
				valid, seen := validKeys[key]
				if !seen {
					stored, readErr := sink.ReadMessageByIdempotencyKey(turn.sessionID, key)
					if readErr != nil {
						return readErr
					}
					valid = stored != nil && stored.Content.Completion != agent.MessageCompletionInterrupted && stored.Content.Completion != agent.MessageCompletionError
					validKeys[key] = valid
				}
				if !valid {
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

	return retireReasonixInputsWithOutcome(sink, turn.inputs, proved, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
}

func retireReasonixInputsWithOutcome(sink agent.ProviderServices, inputs []reasonixSteerInput, proved map[string]bool, outcome leapmuxv1.AgentInputTurnOutcome) error {
	for _, input := range inputs {
		if proved[input.receipt.InputID] {
			continue
		}
		if err := sink.RetireNativeInputTurn(input.receipt, outcome); err != nil {
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
			trimmed := strings.TrimSpace(combined)
			if trimmed == target {
				result[i] = keys
				ansIdx = k - 1
				matchedSplit = true
				break
			}
			if len(trimmed) > len(target)+64 {
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

func openReasonixRegularFile(path, label string) (*os.File, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("the %s is not a regular file", label)
	}
	return file, info, nil
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
	file, info, err := openReasonixRegularFile(path, "Reasonix transcript")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
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
	file, info, err := openReasonixRegularFile(eventPath, "Reasonix event log")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	graph := newReasonixToolGraph()
	var (
		schema int
		count  int
		linear []reasonixJournalMessage
	)
	_, err = readReasonixJSONL(ctx, file, 0, info.Size(), func(raw json.RawMessage, offset int64, length int) error {
		return applyReasonixEventLine(
			raw,
			offset,
			length,
			&schema,
			graph,
			&count,
			func() {
				linear = linear[:0]
			},
			func(_ int, rawMsg json.RawMessage, _ reasonixMessageLocation) {
				var msg reasonixJournalMessage
				if json.Unmarshal(rawMsg, &msg) == nil && msg.Role != "" {
					linear = append(linear, msg)
				}
			},
		)
	})
	if err != nil {
		return nil, err
	}
	if schema == 1 {
		return linear, nil
	}
	if schema != 2 {
		return nil, nil
	}
	var chain []reasonixJournalMessage
	err = graph.walkSelectedBranch(func(node reasonixToolNode) error {
		raw, _, readErr := readReasonixRawMessageAt(file, node.at)
		if readErr != nil {
			return readErr
		}
		var msg reasonixJournalMessage
		if json.Unmarshal(raw, &msg) == nil && msg.Role != "" {
			chain = append(chain, msg)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Reverse(chain)
	return chain, nil
}
