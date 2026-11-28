package amp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	ampExportAttempts = 6
	ampExportInterval = 5 * time.Second
	ampExportBudget   = ampExportAttempts * ampExportInterval
	// AmpExportBudgetTag identifies the total wait for native delivery evidence.
	AmpExportBudgetTag = "amp-export-budget"
	// AmpExportIntervalTag identifies the interval between native history reads.
	AmpExportIntervalTag = "amp-export-interval"
)

// ampThreadExport reads native thread settings and the identities of delivered messages.
// The recorded shape comes from .tmp/verify-c29/amp/run2-export/export-before.json.
type ampThreadExport struct {
	ID        string `json:"id"`
	AgentMode string `json:"agentMode"`
	Meta      struct {
		Features *[]string `json:"features"`
	} `json:"meta"`
	Messages []ampExportMessage `json:"messages"`
}

type ampExportMessage struct {
	ID      string            `json:"protocolMessageID"`
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
	Meta    struct {
		Steer        bool `json:"steer"`
		FromExecutor bool `json:"fromExecutorClient"`
	} `json:"meta"`
	State struct {
		Type       string `json:"type"`
		StopReason string `json:"stopReason"`
	} `json:"state"`
}

type ampSteerState uint8

const (
	ampSteerPending ampSteerState = iota
	ampSteerEchoed
	ampSteerFinished
	ampSteerUncertain
)

// ampSteerSubmission tracks one identified steer from submission through echo,
// live frame capture, and recovery or retirement. The agent mutex protects
// these fields.
type ampSteerSubmission struct {
	threadID     string
	nativeID     string
	resolution   agent.SteerResolution
	state        ampSteerState
	liveFrames   []ampReplyFrame
	liveUnproved bool
}

func (s *ampSteerSubmission) markFinished() {
	s.state = ampSteerFinished
	s.liveFrames = nil
}

// ampInFlightExport coalesces concurrent export requests for one thread at the
// same or older export generation while letting newer-generation callers run
// independently.
type ampInFlightExport struct {
	gen      uint64
	done     chan struct{}
	exported *ampThreadExport
	err      error

	mu      sync.Mutex
	waiters int
	cancel  context.CancelFunc
}

func (s *ampInFlightExport) join() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiters <= 0 {
		return false
	}
	s.waiters++
	return true
}

func (s *ampInFlightExport) release() {
	s.mu.Lock()
	s.waiters--
	last := s.waiters == 0
	s.mu.Unlock()
	if last {
		s.cancel()
	}
}

func (a *Agent) bumpExportGen() {
	a.exportGen.Add(1)
}

func (a *Agent) readInputExport(ctx context.Context, threadID string) (*ampThreadExport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	minGen := a.exportGen.Load()
	a.exportMu.Lock()
	var slot *ampInFlightExport
	if existing := a.activeExports[threadID]; existing != nil && existing.gen >= minGen && existing.join() {
		slot = existing
	} else {
		runCtx, runCancel := context.WithCancel(a.ctx)
		slot = &ampInFlightExport{
			gen:     minGen,
			done:    make(chan struct{}),
			waiters: 1,
			cancel:  runCancel,
		}
		if a.activeExports == nil {
			a.activeExports = make(map[string]*ampInFlightExport)
		}
		a.activeExports[threadID] = slot
		a.exportWG.Add(1)
		go func(s *ampInFlightExport) {
			defer a.exportWG.Done()
			defer func() {
				a.exportMu.Lock()
				if a.activeExports[threadID] == s {
					delete(a.activeExports, threadID)
				}
				a.exportMu.Unlock()
				close(s.done)
			}()
			raw, err := a.exportThread(runCtx, threadID)
			if err == nil && runCtx.Err() != nil {
				err = runCtx.Err()
			}
			if err == nil {
				s.exported, err = parseThreadExport(raw, threadID)
			}
			s.err = err
		}(slot)
	}
	a.exportMu.Unlock()

	select {
	case <-slot.done:
		slot.release()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return slot.exported, slot.err
	case <-ctx.Done():
		slot.release()
		return nil, ctx.Err()
	}
}

func parseThreadExport(raw []byte, threadID string) (*ampThreadExport, error) {
	var exported ampThreadExport
	if err := json.Unmarshal(raw, &exported); err != nil {
		return nil, fmt.Errorf("read the Amp thread export: %w", err)
	}
	if exported.ID != threadID || exported.Messages == nil {
		return nil, errors.New("the Amp export does not identify the requested thread history")
	}
	if exported.Meta.Features != nil && slices.Contains(*exported.Meta.Features, "") {
		return nil, errors.New("the Amp export has an empty or null thread feature")
	}
	seen := make(map[string]bool, len(exported.Messages))
	for _, message := range exported.Messages {
		if message.ID == "" || seen[message.ID] {
			return nil, errors.New("the Amp export has an absent or repeated message identity")
		}
		seen[message.ID] = true
	}
	return &exported, nil
}

// refreshThreadSettings reads immutable native choices before a resumed agent publishes its catalog.
// An absent feature list is unknown; an explicit empty list confirms standard speed.
func (a *Agent) refreshThreadSettings(ctx context.Context, threadID string) error {
	exported, err := a.readInputExport(ctx, threadID)
	if err != nil {
		return err
	}
	fast := ""
	if exported.Meta.Features != nil {
		features := *exported.Meta.Features
		fast = strconv.FormatBool(slices.Contains(features, "fast") || slices.Contains(features, "plaid"))
	}
	a.mu.Lock()
	a.agentMode = exported.AgentMode
	a.fastMode = fast
	a.mu.Unlock()
	return nil
}

// associateSteers assigns each tracked submission its proved native message ID
// when that ID appears in exported thread history as a user prompt.
func associateSteers(submissions []*ampSteerSubmission, exported *ampThreadExport) {
	claimed := make(map[string]bool)
	positions := make(map[string]int, len(exported.Messages))
	for i, message := range exported.Messages {
		positions[message.ID] = i
	}
	for _, submission := range submissions {
		if submission.threadID != exported.ID {
			continue
		}
		if submission.nativeID != "" {
			claimed[submission.nativeID] = true
			continue
		}
		if id := submission.resolution.Receipt.InputID; id != "" {
			if position, ok := positions[id]; ok && !claimed[id] && isAmpExportPrompt(exported.Messages[position]) {
				submission.nativeID = id
				claimed[id] = true
			}
		}
	}
}

func (a *Agent) exportThread(ctx context.Context, threadID string) ([]byte, error) {
	if a.exportFn != nil {
		return a.exportFn(ctx, threadID)
	}
	return a.runCLI(ctx, []string{"threads", "export", threadID}, ampExportBudget, 64<<20)
}

// exportContext includes command duration in the injected timeout.
func (a *Agent) exportContext() (context.Context, func()) {
	return a.exportContextForProc(nil)
}

func (a *Agent) exportContextForProc(proc *ampProcess) (context.Context, func()) {
	ctx, cancel := context.WithCancel(a.ctx)
	timer := a.clock.NewTimer(ampExportBudget, AmpExportBudgetTag)
	var procEnding <-chan struct{}
	if proc != nil {
		procEnding = proc.endingSignal()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-timer.C:
			cancel()
		case <-procEnding:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(); timer.Stop(); <-done }
}

func (a *Agent) rememberSteer(proc *ampProcess, threadID string, resolution agent.SteerResolution) (*ampSteerSubmission, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped || a.proc != proc || a.threadID != threadID || !a.turn.active || proc.ending() {
		return nil, agent.ErrNoActiveTurn
	}
	select {
	case <-proc.ProcessDone():
		return nil, agent.ErrNoActiveTurn
	default:
	}
	if resolution.Receipt.InputID == "" {
		return nil, nil
	}
	submission := &ampSteerSubmission{threadID: threadID, resolution: resolution}
	a.nativeSteers = append(a.nativeSteers, submission)
	return submission, nil
}

// forgetUnconsumedSteer removes a failed write unless native history or
// uncertainty already claimed ownership of the submission.
func (a *Agent) forgetUnconsumedSteer(target *ampSteerSubmission) bool {
	a.mu.Lock()
	if target.state != ampSteerPending || target.nativeID != "" {
		a.mu.Unlock()
		return false
	}
	a.nativeSteers = slices.DeleteFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool { return submission == target })
	a.mu.Unlock()
	return true
}

func (a *Agent) hasUncertainSteer(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.state == ampSteerUncertain && submission.threadID == threadID
	})
}

func (a *Agent) clearSettledUncertainSteers(threadID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nativeSteers = slices.DeleteFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.state == ampSteerUncertain && submission.threadID == threadID
	})
}

// checkUncertainThread refuses a replacement executor while native history still shows work in progress.
func (a *Agent) checkUncertainThread(threadID string) error {
	if !a.hasUncertainSteer(threadID) {
		return nil
	}
	ctx, cancel := a.exportContext()
	defer cancel()
	exported, err := a.readInputExport(ctx, threadID)
	if err != nil {
		return err
	}
	for i := len(exported.Messages) - 1; i >= 0; i-- {
		message := exported.Messages[i]
		if message.Role == "user" && message.Meta.FromExecutor {
			return agent.ErrAgentBusy
		}
		if message.Role == "assistant" {
			if message.State.Type == "complete" && message.State.StopReason == stopReasonEndTurn {
				a.clearSettledUncertainSteers(threadID)
				return nil
			}
			return agent.ErrAgentBusy
		}
	}
	a.clearSettledUncertainSteers(threadID)
	return nil
}

func visibleExportBlocks(content []json.RawMessage) ([]ampBlock, error) {
	var visible []ampBlock
	for _, raw := range content {
		var block ampBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		if assistantBlockKept(block) {
			visible = append(visible, block)
		}
	}
	return visible, nil
}

func (a *Agent) noteLiveAssistant(threadID string, reply *ampInputReply, frame ampReplyFrame, ended bool) {
	a.mu.Lock()
	var latestTracked string
	if reply != nil {
		if reply.startsWithSteer {
			latestTracked = reply.receipt.InputID
		}
		if count := len(reply.steers); count > 0 {
			latestTracked = reply.steers[count-1].InputID
		}
	}
	for i := len(a.nativeSteers) - 1; i >= 0; i-- {
		submission := a.nativeSteers[i]
		if threadID != "" && submission.threadID != threadID {
			continue
		}
		if submission.state == ampSteerEchoed {
			if latestTracked != "" && latestTracked != submission.nativeID {
				continue
			}
			if reply != nil && !reply.unproved {
				submission.liveFrames = append(submission.liveFrames, frame)
			} else {
				submission.liveUnproved = true
			}
			break
		}
	}
	if ended {
		a.finishEchoedSteersLocked(threadID)
	}
	a.mu.Unlock()
}

func (a *Agent) finishEchoedSteersLocked(threadID string) {
	for _, submission := range a.nativeSteers {
		if submission.state == ampSteerEchoed && (threadID == "" || submission.threadID == threadID) {
			submission.markFinished()
		}
	}
	a.pruneTrackedSteersLocked()
}

func (a *Agent) hasRecoverableSteerLocked() bool {
	return slices.ContainsFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.state == ampSteerPending || submission.state == ampSteerEchoed
	})
}

// prepareSteerRecovery prevents a process-end row from releasing known server work.
func (a *Agent) prepareSteerRecovery() bool {
	if a.discarding() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return false
	}
	if a.hasRecoverableSteerLocked() {
		a.reconciling = true
		return true
	}
	return false
}

func (a *Agent) waitExportInterval(ctx context.Context) bool {
	timer := a.clock.NewTimer(ampExportInterval, AmpExportIntervalTag)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

// reconcileStoppedSteers resolves unproved deliveries within one total budget.
// A confirmed unfinished reply remains active. Its separate monitor continues until the native end or Stop.
// The exit handler calls this without sendMu. A sender can hold sendMu while it waits for that handler.
func (a *Agent) reconcileStoppedSteers(threadID string) {
	if !a.prepareSteerRecovery() {
		return
	}
	defer func() {
		a.mu.Lock()
		a.reconciling = false
		a.mu.Unlock()
		a.PublishTurnActive()
	}()
	ctx, cancel := a.exportContext()
	for attempt := 0; attempt < ampExportAttempts && ctx.Err() == nil && !a.discarding(); attempt++ {
		if a.readRecoveredSteers(ctx, threadID) {
			cancel()
			return
		}
		if attempt+1 < ampExportAttempts && !a.waitExportInterval(ctx) {
			break
		}
	}
	cancel()
	if a.ctx.Err() != nil || a.discarding() {
		return
	}
	// An absent record is not proof of a native drop.
	a.mu.Lock()
	var uncertain []*ampSteerSubmission
	knownUnfinished := false
	for _, submission := range a.nativeSteers {
		if submission.state == ampSteerFinished || submission.state == ampSteerUncertain {
			continue
		}
		if submission.state == ampSteerPending && submission.nativeID == "" {
			submission.state = ampSteerUncertain
			uncertain = append(uncertain, submission)
		} else {
			knownUnfinished = true
		}
	}
	a.mu.Unlock()
	for _, submission := range uncertain {
		resolution := submission.resolution
		resolution.Outcome, resolution.Receipt = agent.SteerOutcomeUncertain, agent.NativeInputReceipt{}
		if err := agent.RetainSteerResolution(a.sink, resolution); !agent.OutputRetained(err) {
			slog.Warn("retain the Amp steer uncertainty", "agent_id", a.agentID, "error", err)
		}
	}
	// Uncertainty pauses the queue. A confirmed reply still owns an active turn.
	if knownUnfinished {
		a.ensureTurn()
		a.PublishTurnActive()
	}
	for knownUnfinished && a.ctx.Err() == nil && !a.discarding() {
		if !a.waitExportInterval(a.ctx) {
			return
		}
		ctx, cancel := a.exportContext()
		complete := a.readRecoveredSteers(ctx, threadID)
		cancel()
		if complete {
			return
		}
	}
}

func (a *Agent) readRecoveredSteers(ctx context.Context, threadID string) bool {
	exported, err := a.readInputExport(ctx, threadID)
	if err != nil {
		if a.ctx.Err() == nil {
			slog.Warn("read the Amp recovery history", "agent_id", a.agentID, "error", err)
		}
		return false
	}
	return a.applyRecoveredSteers(exported)
}

// applyRecoveredSteers runs after the native reader ends. The sender cannot start a replacement process yet.
func (a *Agent) applyRecoveredSteers(exported *ampThreadExport) bool {
	if a.discarding() || a.IsStopped() {
		return false
	}
	a.mu.Lock()
	associateSteers(a.nativeSteers, exported)
	submissions := slices.Clone(a.nativeSteers)
	a.mu.Unlock()
	if !a.recoverTrackedSteers(exported, submissions) {
		return false
	}
	for _, submission := range submissions {
		a.mu.Lock()
		state := submission.state
		a.mu.Unlock()
		if state == ampSteerPending || state == ampSteerEchoed {
			return false
		}
	}
	a.finishTurn(agent.MessageCompletionComplete, nil)
	return true
}

// persistExportMessage keeps native identities through failed writes and repeated history reads.
func (a *Agent) persistExportMessage(threadID string, message ampExportMessage, blocks []ampBlock) bool {
	if a.discarding() || a.IsStopped() {
		return false
	}
	a.mu.Lock()
	importedKey := threadID + "\x00" + message.ID
	if a.importedMessages[importedKey] {
		a.mu.Unlock()
		return true
	}
	a.mu.Unlock()
	var rows []agent.TranscriptMessage
	for index, block := range blocks {
		kind, text := agent.AssembledMessageKindText, block.Text
		switch block.Type {
		case contracts.AmpBlockTypeThinking:
			kind, text = agent.AssembledMessageKindReasoning, block.Thinking
		case contracts.AmpBlockTypeText:
		default:
			continue
		}
		if agent.TrimJavaScriptSpace(text) == "" {
			continue
		}
		raw, err := agent.MarshalAssembledMessage(kind, text, agent.MessageCompletionComplete)
		if err != nil {
			slog.Error("encode the recovered Amp reply", "agent_id", a.agentID, "error", err)
			return false
		}
		rows = append(rows, agent.TranscriptMessage{
			Source:  leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			Content: agent.MessageContent{Original: raw, AgentSessionID: threadID, IdempotencyKey: exportMessageKey(threadID, message.ID, index)},
		})
	}
	err := agent.PersistTranscriptBatch(a.sink, rows)
	if err != nil && !agent.OutputRetained(err) {
		slog.Error("persist the recovered Amp reply", "agent_id", a.agentID, "error", err)
		return false
	}
	a.mu.Lock()
	if a.importedMessages == nil {
		a.importedMessages = make(map[string]bool)
	}
	a.importedMessages[importedKey] = true
	a.mu.Unlock()
	return true
}

func exportMessageKey(threadID, messageID string, index int) string {
	return fmt.Sprintf("amp-export:%s:%s:%d", threadID, messageID, index)
}
