package amp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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

// ampSteerSubmission retains ownership after a live echo removes its ledger entry.
// The agent mutex protects these fields. Each baseline is immutable.
type ampSteerSubmission struct {
	ticket       providerkit.SteerTicket
	key          string
	threadID     string
	baseline     map[string]bool
	nativeID     string
	echoed       bool
	finished     bool
	uncertain    bool
	liveMessages [][]ampBlock
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
	raw, err := a.exportThread(ctx, threadID)
	if err != nil {
		return err
	}
	exported, err := parseThreadExport(raw, threadID)
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

func (exported *ampThreadExport) identities() map[string]bool {
	ids := make(map[string]bool, len(exported.Messages))
	for _, message := range exported.Messages {
		ids[message.ID] = true
	}
	return ids
}

func (message ampExportMessage) steerKey() (string, bool) {
	if message.Role != "user" || !message.Meta.Steer || !message.Meta.FromExecutor {
		return "", false
	}
	var texts []string
	hasPrompt := false
	for _, raw := range message.Content {
		var block struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if json.Unmarshal(raw, &block) != nil {
			return "", false
		}
		switch block.Type {
		case contracts.AmpBlockTypeText:
			text, ok := providerkit.RawString(block.Text)
			if !ok {
				return "", false
			}
			texts = append(texts, text)
			hasPrompt = true
		case blockTypeImage:
			hasPrompt = true
		default:
			return "", false
		}
	}
	return ampEchoKey(texts), hasPrompt
}

// associateSteers assigns each new native identity to at most one local submission.
// A key with missing or extra candidates remains unresolved. Text alone cannot identify that delivery.
// Earlier submissions stay in the search even after their live echoes settle.
func associateSteers(submissions []*ampSteerSubmission, exported *ampThreadExport) {
	claimed := make(map[string]bool)
	proposed := make(map[*ampSteerSubmission]string)
	positions := make(map[string]int)
	for i, message := range exported.Messages {
		positions[message.ID] = i
	}
	groups := make(map[string][]*ampSteerSubmission)
	for _, submission := range submissions {
		if submission.threadID != exported.ID {
			continue
		}
		if submission.nativeID != "" {
			claimed[submission.nativeID] = true
			continue
		}
		groups[submission.key] = append(groups[submission.key], submission)
	}
	for key, group := range groups {
		var candidates []ampExportMessage
		for _, message := range exported.Messages {
			candidateKey, ok := message.steerKey()
			if !ok || candidateKey != key || claimed[message.ID] {
				continue
			}
			if slices.ContainsFunc(group, func(submission *ampSteerSubmission) bool { return !submission.baseline[message.ID] }) {
				candidates = append(candidates, message)
			}
		}
		if len(candidates) != len(group) {
			continue
		}
		valid := true
		for i, submission := range group {
			if submission.baseline[candidates[i].ID] {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		for i, submission := range group {
			proposed[submission] = candidates[i].ID
			claimed[candidates[i].ID] = true
		}
	}
	last := -1
	for _, submission := range submissions {
		if submission.threadID != exported.ID {
			continue
		}
		id := submission.nativeID
		if id == "" {
			id = proposed[submission]
		}
		if id == "" {
			continue
		}
		position, ok := positions[id]
		if !ok || position <= last {
			return
		}
		last = position
	}
	for submission, id := range proposed {
		submission.nativeID = id
	}

}

func (a *Agent) exportThread(ctx context.Context, threadID string) ([]byte, error) {
	if a.exportFn != nil {
		return a.exportFn(ctx, threadID)
	}
	return providerkit.RunCLI(ctx, providerkit.CLIRun{
		Locator: ampLocator, Label: "Amp", Shell: a.launch.opts.Shell, LoginShell: a.launch.opts.LoginShell,
		Args: []string{"threads", "export", threadID}, WorkingDir: a.launch.opts.WorkingDir,
		StripEnvKeys: ampIdentityEnvKeys, SetEnv: []string{envSkipUpdateCheck + "=1"},
		Env: a.launch.processEnv, Timeout: ampExportBudget, MaxOutput: 64 << 20,
	})
}

// exportContext includes command duration in the injected timeout.
func (a *Agent) exportContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(a.ctx)
	timer := a.clock.NewTimer(ampExportBudget, AmpExportBudgetTag)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-timer.C:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(); timer.Stop(); <-done }
}

func (a *Agent) steerBaseline() (*ampThreadExport, error) {
	a.mu.Lock()
	threadID := a.threadID
	a.mu.Unlock()
	if threadID == "" {
		// Amp emits its first init after a complete message. Waiting would hold the user's steer request for that whole response.
		return nil, agent.ErrSteeringUnsupported
	}
	ctx, cancel := a.exportContext()
	defer cancel()
	raw, err := a.exportThread(ctx, threadID)
	if err != nil {
		return nil, fmt.Errorf("read the history before steering Amp: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exported, err := parseThreadExport(raw, threadID)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	associateSteers(a.nativeSteers, exported)
	a.mu.Unlock()
	return exported, nil
}

func (a *Agent) rememberSteer(proc *ampProcess, key string, baseline *ampThreadExport) (providerkit.SteerTicket, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped || a.proc != proc || a.threadID != baseline.ID || !a.turn.active || proc.ending() {
		return 0, agent.ErrNoActiveTurn
	}
	select {
	case <-proc.ProcessDone():
		return 0, agent.ErrNoActiveTurn
	default:
	}
	ticket := a.steers.Add(key)
	a.nativeSteers = append(a.nativeSteers, &ampSteerSubmission{ticket: ticket, key: key, threadID: baseline.ID, baseline: baseline.identities()})
	return ticket, nil
}

func (a *Agent) forgetSteer(ticket providerkit.SteerTicket) {
	a.mu.Lock()
	a.nativeSteers = slices.DeleteFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool { return submission.ticket == ticket })
	a.mu.Unlock()
}

func (a *Agent) hasUncertainSteer(threadID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.nativeSteers, func(submission *ampSteerSubmission) bool {
		return submission.uncertain && submission.threadID == threadID
	})
}

// checkUncertainThread refuses a replacement executor while native history still shows work in progress.
func (a *Agent) checkUncertainThread(threadID string) error {
	if !a.hasUncertainSteer(threadID) {
		return nil
	}
	ctx, cancel := a.exportContext()
	defer cancel()
	raw, err := a.exportThread(ctx, threadID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	exported, err := parseThreadExport(raw, threadID)
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
				return nil
			}
			return agent.ErrAgentBusy
		}
	}
	return nil
}

// noteSteerEcho retains the live receipt after the input queue removes the input.
func (a *Agent) noteSteerEcho(ticket providerkit.SteerTicket) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, submission := range a.nativeSteers {
		if submission.ticket == ticket {
			submission.echoed = true
			return
		}
	}
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

func (a *Agent) noteLiveAssistant(message *ampMessage) {
	blocks, err := visibleExportBlocks(message.Content)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.nativeSteers) - 1; i >= 0; i-- {
		submission := a.nativeSteers[i]
		if submission.echoed && !submission.finished {
			submission.liveMessages = append(submission.liveMessages, blocks)
			break
		}
	}
	if message.StopReason != nil && *message.StopReason == stopReasonEndTurn {
		for _, submission := range a.nativeSteers {
			if submission.echoed {
				submission.finished = true
			}
		}
	}
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
	for _, submission := range a.nativeSteers {
		if !submission.finished && !submission.uncertain {
			a.reconciling = true
			return true
		}
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
	var uncertain []providerkit.SteerTicket
	knownUnfinished := false
	for _, submission := range a.nativeSteers {
		if submission.finished || submission.uncertain {
			continue
		}
		if !submission.echoed && submission.nativeID == "" {
			submission.uncertain = true
			uncertain = append(uncertain, submission.ticket)
		} else {
			knownUnfinished = true
		}
	}
	a.mu.Unlock()
	for _, ticket := range uncertain {
		a.steers.Uncertain(ticket)
	}
	a.steers.Settle(a.sink)
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
	raw, err := a.exportThread(ctx, threadID)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if a.ctx.Err() == nil {
			slog.Warn("read the Amp recovery history", "agent_id", a.agentID, "error", err)
		}
		return false
	}
	exported, err := parseThreadExport(raw, threadID)
	if err != nil {
		slog.Warn("read the Amp recovery history", "agent_id", a.agentID, "error", err)
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
	positions := make(map[string]int, len(exported.Messages))
	for i, message := range exported.Messages {
		positions[message.ID] = i
	}
	for _, submission := range submissions {
		if a.discarding() || a.IsStopped() {
			return false
		}
		a.mu.Lock()
		finished, uncertain, nativeID := submission.finished, submission.uncertain, submission.nativeID
		live := slices.Clone(submission.liveMessages)
		a.mu.Unlock()
		if finished || uncertain {
			continue
		}
		position, found := positions[nativeID]
		if !found {
			return false
		}
		a.ensureTurn()
		a.mu.Lock()
		submission.echoed = true
		a.mu.Unlock()
		a.steers.Echo(submission.ticket)
		a.steers.Settle(a.sink)
		liveIndex := 0
		complete := false
		for _, message := range exported.Messages[position+1:] {
			if message.Role == "user" && message.Meta.FromExecutor {
				// Consecutive owned steers can share one reply. The later steer owns that reply.
				a.mu.Lock()
				complete = slices.ContainsFunc(submissions, func(next *ampSteerSubmission) bool {
					return next.nativeID == message.ID && next.threadID == exported.ID
				})
				a.mu.Unlock()
				break
			}
			if message.Role != "assistant" {
				continue
			}
			if message.State.Type != "complete" {
				return false
			}
			blocks, err := visibleExportBlocks(message.Content)
			if err != nil {
				return false
			}
			if liveIndex < len(live) {
				// Native stdout has no message identity. Match its complete visible prefix before importing any later message.
				before, err := json.Marshal(live[liveIndex])
				if err != nil {
					return false
				}
				after, err := json.Marshal(blocks)
				if err != nil || !agent.JSONCanonicalEqual(before, after) {
					return false
				}
				liveIndex++
			} else if !a.persistExportMessage(exported.ID, message, blocks) {
				return false
			}
			if message.State.StopReason == stopReasonEndTurn {
				complete = true
				break
			}
		}
		if liveIndex < len(live) || !complete {
			return false
		}
		a.mu.Lock()
		submission.finished = true
		a.mu.Unlock()
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
	if a.importedMessages[message.ID] {
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
		if strings.TrimSpace(text) == "" {
			continue
		}
		raw, err := agent.MarshalAssembledMessage(kind, text, agent.MessageCompletionComplete)
		if err != nil {
			slog.Error("encode the recovered Amp reply", "agent_id", a.agentID, "error", err)
			return false
		}
		rows = append(rows, agent.TranscriptMessage{
			Source:  leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			Content: agent.MessageContent{Original: raw, AgentSessionID: threadID, IdempotencyKey: fmt.Sprintf("amp-export:%s:%s:%d", threadID, message.ID, index)},
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
	a.importedMessages[message.ID] = true
	a.mu.Unlock()
	return true
}
