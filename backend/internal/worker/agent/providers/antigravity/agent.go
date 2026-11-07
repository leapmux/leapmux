package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const (
	antigravityStreamConnectTimerTag   = "antigravity-stream-connect"
	antigravityStreamReconnectTimerTag = "antigravity-stream-reconnect"
	antigravityChildAttachTimerTag     = "antigravity-child-attach"
	antigravityClearTimerTag           = "antigravity-clear"
	antigravityStopTimerTag            = "antigravity-stop"
	antigravityStreamRetryFirst        = 100 * time.Millisecond
	antigravityStreamRetryMax          = 5 * time.Second
)

// Agent owns one agy process and the language server inside it. ACP stdio
// manages sessions. Every live update arrives on one HTTP stream per cascade.
type Agent struct {
	providerkit.JSONRPCProcess
	sink       agent.ProviderServices
	rpc        antigravityRPC
	workingDir string
	workspace  string
	homeDir    string
	version    string
	clock      quartz.Clock
	agentDir   *agentdir.Dir
	logPath    string

	streamCtx    context.Context
	streamCancel context.CancelFunc
	streamWG     *sync.WaitGroup
	inputWG      sync.WaitGroup
	approvalWork providerkit.CoalescingWork
	quotaWork    providerkit.CoalescingWork
	settingsMu   sync.Mutex
	childSetupMu sync.Mutex

	// Mu protects streamCtx, streamCancel, streamWG, and every field below.
	// No caller holds Mu across a sink call or an HTTP request.
	sessionID        string
	generation       uint64
	sessionSwitching bool
	root             *cascadeState
	cascades         map[string]*cascadeState
	catalog          antigravityCatalog
	model            string
	effort           string
	mode             string
	slashCommands    []string
}

type cascadeState struct {
	id           string
	sink         agent.ProviderServices
	parent       *cascadeState
	childAgentID string
	generation   uint64
	seededCount  int
	stored       bool
	seenUpdate   bool
	trajectoryID string
	steps        map[int]*stepState
	turnSteps    map[int]struct{}
	childKeys    map[string]*childKey
	background   map[int]backgroundTask
	// Removed tasks stay owned until their close enters the Worker output queue.
	closingBackground map[string]backgroundTask
	controls          map[int]*antigravityControl
	planControl       *antigravityControl
	turnPhase         cascadeTurnPhase
	sendInFlight      bool
	heldErrorStep     json.RawMessage
	interrupted       bool
	retryPending      bool
	planPath          string
	contextWindow     int64
	contextTokens     int64
	rowOpen           bool
	closed            bool
	connectErr        error
	streamDone        chan struct{}
	connected         chan struct{}
	connectedOnce     sync.Once
}

type cascadeTurnPhase uint8

const (
	cascadeTurnIdle cascadeTurnPhase = iota
	cascadeTurnActive
	cascadeTurnFinalizing
)

type cascadeTurnStart struct {
	state   *cascadeState
	seq     uint64
	revival *agent.OutputTicket
}

type stepState struct {
	status             string
	consumed           bool
	liveOutputAccepted bool
	spanID             string
	text               string
	toolStep           bool
	modelUsage         map[string]string
	raw                json.RawMessage
}

type childKey struct {
	role        string
	spawnSpanID string
	stored      bool
	idle        bool
	refused     bool
	linked      bool
	state       *cascadeState
}

type backgroundTask struct {
	rowKey      string
	title       string
	description string
	stepIndex   int
	slot        int
}

var (
	_ agent.Agent            = (*Agent)(nil)
	_ agent.InputSteerer     = (*Agent)(nil)
	_ agent.ChildSteerer     = (*Agent)(nil)
	_ agent.ChildInterrupter = (*Agent)(nil)
	_ agent.ContextCompactor = (*Agent)(nil)
)

func newAgentState(sink agent.ProviderServices, rpc antigravityRPC, workingDir string, clock quartz.Clock) *Agent {
	if clock == nil {
		clock = quartz.NewReal()
	}
	if rpc.timeout <= 0 {
		rpc.timeout = 30 * time.Second
	}
	sink = agent.NewOrderedProviderServices(sink.OutputQueue(), sink)
	a := &Agent{
		sink: sink, rpc: rpc, workingDir: workingDir, workspace: workingDir,
		clock: clock, streamWG: &sync.WaitGroup{}, cascades: map[string]*cascadeState{}, mode: contracts.AntigravityModeDefault,
	}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		Ctx: context.Background(), ProcessDone: make(chan struct{}), Clock: clock,
	})
	a.streamCtx, a.streamCancel = context.WithCancel(context.Background())
	return a
}

func newCascadeState(id string, sink agent.ProviderServices, generation uint64, stored bool) *cascadeState {
	return &cascadeState{
		id: id, sink: sink, generation: generation, stored: stored,
		steps: map[int]*stepState{}, turnSteps: map[int]struct{}{}, childKeys: map[string]*childKey{},
		background: map[int]backgroundTask{}, closingBackground: map[string]backgroundTask{}, controls: map[int]*antigravityControl{},
		connected: make(chan struct{}),
	}
}

func (a *Agent) installRoot(id string, stored bool) *cascadeState {
	root := newCascadeState(id, a.sink, a.generation, stored)
	a.root = root
	a.sessionID = id
	a.cascades = map[string]*cascadeState{id: root}
	return root
}

func (a *Agent) cascadeInstalledLocked(state *cascadeState) bool {
	return state != nil && a.cascades[state.id] == state
}

func (a *Agent) cascadeInstalled(state *cascadeState) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.cascadeInstalledLocked(state)
}

func (a *Agent) cascadeCurrentLocked(state *cascadeState) bool {
	return a.cascadeInstalledLocked(state) && state.generation == a.generation && !a.StoppedLocked()
}

func (a *Agent) cascadeCurrent(state *cascadeState) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.cascadeCurrentLocked(state)
}

func (a *Agent) acceptsCascadeUpdate(state *cascadeState) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.cascadeCurrentLocked(state) && !state.closed && !a.IsDiscardingOutput()
}

func (a *Agent) startCascadeStream(state *cascadeState) {
	a.startCascadeWorker(state, a.runCascadeStream)
}

func (a *Agent) startCascadeWorker(state *cascadeState, work func(context.Context, *cascadeState)) {
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed || a.streamCtx.Err() != nil || a.IsDiscardingOutput() {
		a.Mu.Unlock()
		return
	}
	if state.streamDone != nil {
		select {
		case <-state.streamDone:
		default:
			a.Mu.Unlock()
			return
		}
	}
	done := make(chan struct{})
	state.streamDone = done
	ctx, group := a.streamCtx, a.streamWG
	group.Add(1)
	a.Mu.Unlock()
	go func() {
		defer group.Done()
		defer close(done)
		work(ctx, state)
	}()
}

func (a *Agent) runCascadeStream(ctx context.Context, state *cascadeState) {
	delay := antigravityStreamRetryFirst
	emptyAttempts := 0
	undecodable := 0
	for {
		if ctx.Err() != nil {
			return
		}
		a.Mu.Lock()
		current := a.cascades[state.id] == state && state.generation == a.generation && !state.closed
		startIndex := a.firstOpenIndexLocked(state)
		a.Mu.Unlock()
		if !current {
			return
		}
		firstOnAttempt := true
		delivered, err := a.rpc.consumeStream(ctx, state.id, startIndex, func(update agentStateUpdate) error {
			first := firstOnAttempt
			firstOnAttempt = false
			if err := a.applyUpdate(state, update, first); err != nil {
				return err
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if isUndecodableFrame(err) {
			undecodable++
			if undecodable >= 10 {
				a.fatalCascade(state, err)
				return
			}
		} else {
			undecodable = 0
		}
		fatal := isStreamFatalOpen(err) || isStreamNotFound(err) || isFrameOversized(err)
		if !fatal {
			if delivered {
				emptyAttempts = 0
				delay = antigravityStreamRetryFirst
			} else {
				emptyAttempts++
				statusErr := a.rpc.status(ctx)
				if statusErr != nil && emptyAttempts >= 5 || statusErr == nil && emptyAttempts >= 30 {
					fatal = true
					if statusErr != nil {
						err = statusErr
					}
				}
			}
		}
		if fatal {
			a.fatalCascade(state, err)
			return
		}
		slog.Debug("antigravity stream ended; reconnecting", "agent_id", a.AgentID(), "cascade_id", state.id, "error", err, "delay", delay)
		timer := a.clock.NewTimer(delay, antigravityStreamReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(antigravityStreamReconnectTimerTag)
			return
		case <-a.ProcessDone():
			timer.Stop(antigravityStreamReconnectTimerTag)
			return
		case <-timer.C:
		}
		delay = min(delay*2, antigravityStreamRetryMax)
	}
}

func (a *Agent) applyUpdate(state *cascadeState, update agentStateUpdate, firstOnAttempt bool) error {
	queue := a.sink.OutputQueue()
	a.Mu.Lock()
	if a.IsDiscardingOutput() || a.cascades[state.id] != state || state.generation != a.generation || state.closed {
		a.Mu.Unlock()
		return nil
	}
	firstEver := !state.seenUpdate
	state.seenUpdate = true
	if update.TrajectoryID != "" {
		state.trajectoryID = update.TrajectoryID
	}
	for childID, child := range update.Subtrajectories {
		record := state.childKeys[childID]
		if record == nil {
			record = &childKey{}
			state.childKeys[childID] = record
		}
		if child.Metadata.SubagentSpec.Role != "" {
			record.role = child.Metadata.SubagentSpec.Role
		}
		if firstEver && state.stored {
			record.stored = true
			record.idle = child.Status == cascadeStatusIdle
		}
	}
	var start *cascadeTurnStart
	if update.Status == cascadeStatusRunning || update.Status == cascadeStatusBusy {
		start = a.beginTurnLocked(state, queue)
	}
	a.Mu.Unlock()
	a.publishTurnStart(start)

	if err := a.applyBackgroundTasks(state, update.BackgroundTasks, firstOnAttempt); err != nil {
		return err
	}

	patch := update.MainTrajectory.StepsUpdate
	for position, raw := range patch.Steps {
		if position >= len(patch.Indices) {
			break
		}
		if !a.acceptsCascadeUpdate(state) {
			return nil
		}
		index := patch.Indices[position]
		a.Mu.Lock()
		if index < state.seededCount {
			a.Mu.Unlock()
			continue
		}
		previous := state.steps[index]
		if previous != nil && previous.consumed {
			a.Mu.Unlock()
			if step, err := decodeStep(raw); err == nil {
				a.Mu.Lock()
				if state.steps[index] == previous && previous.consumed {
					previous.modelUsage = step.Metadata.ModelUsage
				}
				a.Mu.Unlock()
			}
			continue
		}
		isNew := previous == nil
		if previous == nil {
			previous = &stepState{}
			state.steps[index] = previous
		}
		a.Mu.Unlock()
		if isNew {
			a.beginTurn(state)
		}
		if !a.acceptsCascadeUpdate(state) {
			return nil
		}
		a.Mu.Lock()
		state.turnSteps[index] = struct{}{}
		a.Mu.Unlock()
		step, err := decodeStep(raw)
		if err != nil {
			if err := a.persistUnknownStep(state, index, raw); err != nil {
				return err
			}
			continue
		}
		if err := a.applyStep(state, index, step, raw); err != nil {
			return err
		}
	}

	if !a.acceptsCascadeUpdate(state) {
		return nil
	}
	if update.ExecutionStatus.ModelRequest != nil {
		a.noteModelRetry(state, update.ExecutionStatus.ModelRequest)
	}
	if !a.acceptsCascadeUpdate(state) {
		return nil
	}
	if update.ContextWindowUsage != nil {
		a.publishContextUsage(state, update.ContextWindowUsage)
	}
	if !a.acceptsCascadeUpdate(state) {
		return nil
	}
	if update.Status == cascadeStatusIdle {
		if err := a.endTurn(state, agent.MessageCompletionComplete); err != nil {
			return err
		}
	}
	a.ensureChildren(state)
	a.Mu.Lock()
	if a.cascadeCurrentLocked(state) && !state.closed && state.connectErr == nil {
		state.connectedOnce.Do(func() { close(state.connected) })
	}
	a.Mu.Unlock()
	return nil
}

func (a *Agent) firstOpenIndexLocked(state *cascadeState) int {
	for index := state.seededCount; ; index++ {
		step := state.steps[index]
		if step == nil {
			return index
		}
		if step.consumed {
			continue
		}
		if _, background := state.background[index]; background && isOpenStepStatus(step.status) && step.liveOutputAccepted {
			continue
		}
		return index
	}
}

func (a *Agent) beginTurn(state *cascadeState) {
	queue := a.sink.OutputQueue()
	a.Mu.Lock()
	start := a.beginTurnLocked(state, queue)
	a.Mu.Unlock()
	a.publishTurnStart(start)
}

func (a *Agent) beginTurnLocked(state *cascadeState, queue *agent.OutputQueue) *cascadeTurnStart {
	if !a.cascadeCurrentLocked(state) || state.turnPhase != cascadeTurnIdle || state.closed {
		return nil
	}
	state.turnPhase = cascadeTurnActive
	state.sendInFlight = false
	state.interrupted = false
	state.planPath = ""
	state.turnSteps = map[int]struct{}{}
	start := &cascadeTurnStart{state: state, seq: a.NextTurnSeq()}
	if state.parent != nil {
		if !state.rowOpen {
			sink, rowKey := a.sink, state.id
			start.revival = queue.Append(agent.OutputWrite{
				ScopeID: rowKey, RetainedBytes: len(rowKey), Label: "revive an Antigravity subagent",
				Run: func() error { return sink.ReviveBackgroundTask(rowKey) },
			})[0]
		}
		state.rowOpen = true
	}
	return start
}

func (a *Agent) publishTurnStart(start *cascadeTurnStart) {
	if start == nil {
		return
	}
	if start.revival != nil {
		if queue := start.revival.Queue(); queue != nil {
			a.flushBackgroundTaskWrites(queue)
		}
	}
	state := start.state
	a.Mu.Lock()
	if !a.cascadeCurrentLocked(state) || state.closed || a.IsDiscardingOutput() {
		a.Mu.Unlock()
		return
	}
	snapshot := a.turnStateLocked(state)
	a.Mu.Unlock()
	providerkit.PublishTurnStateTo(state.sink, snapshot, start.seq)
	if state.parent == nil && a.acceptsCascadeUpdate(state) {
		a.sink.ReportProgress(agent.ResetModelProgress())
	}
}

func (a *Agent) turnStateLocked(state *cascadeState) agent.TurnState {
	if !a.cascadeCurrentLocked(state) {
		return agent.TurnState{}
	}
	return agent.TurnState{
		Active:    state.turnPhase != cascadeTurnIdle || state.sendInFlight,
		Steerable: state.turnPhase == cascadeTurnActive && !state.closed && !a.sessionSwitching && a.streamCtx.Err() == nil && !a.IsDiscardingOutput(),
	}
}

func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state, seq := a.turnStateLocked(a.root), a.NextTurnSeq()
	a.Mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, state, seq)
}

func (a *Agent) HandleOutput(content []byte) {
	var update agentStateUpdate
	if err := json.Unmarshal(content, &update); err != nil {
		slog.Warn("antigravity test update is unreadable", "error", err)
		return
	}
	a.Mu.Lock()
	root := a.root
	a.Mu.Unlock()
	if root != nil {
		_ = a.applyUpdate(root, update, false)
	}
}

func (a *Agent) childState(childKey string) (*cascadeState, error) {
	a.Mu.Lock()
	state := a.cascades[childKey]
	if !a.cascadeCurrentLocked(state) {
		a.Mu.Unlock()
		return nil, agent.ErrChildRouteNotReady
	}
	connected, ctx := state.connected, a.streamCtx
	a.Mu.Unlock()
	timer := a.clock.NewTimer(a.APITimeout(), antigravityChildAttachTimerTag)
	defer timer.Stop(antigravityChildAttachTimerTag)
	select {
	case <-connected:
		a.Mu.Lock()
		defer a.Mu.Unlock()
		if !a.cascadeCurrentLocked(state) || state.connected != connected {
			return nil, agent.ErrChildRouteNotReady
		}
		if state.connectErr != nil {
			return nil, state.connectErr
		}
		return state, nil
	case <-timer.C:
		return nil, agent.ErrChildRouteNotReady
	case <-ctx.Done():
		return nil, fmt.Errorf("agent is stopped")
	}
}

func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.turnStateLocked(a.cascades[childKey])
}

func (a *Agent) SupportsSteering() bool     { return true }
func (a *Agent) SteerSurvivesTurnEnd() bool { return true }

func isInputNotRegistered(err error) bool {
	return providerkit.IsHTTPStatus(err, 500) && strings.Contains(strings.ToLower(streamErrorText(err)), "input not registered")
}

func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	return a.executeControlReply(data)
}
