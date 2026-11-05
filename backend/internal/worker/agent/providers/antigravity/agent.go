package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	streamWG     sync.WaitGroup
	inputWG      sync.WaitGroup
	approvalWork providerkit.CoalescingWork
	settingsMu   sync.Mutex

	// Mu protects every field below. A stream applies its whole frame in order,
	// and no caller holds Mu across a sink call or an HTTP request.
	sessionID           string
	generation          uint64
	sessionSwitching    bool
	root                *cascadeState
	cascades            map[string]*cascadeState
	catalog             antigravityCatalog
	model               string
	effort              string
	mode                string
	slashCommands       []string
	planPromptRequestID string
}

type cascadeState struct {
	id            string
	sink          agent.ProviderServices
	parent        *cascadeState
	childAgentID  string
	generation    uint64
	seededCount   int
	stored        bool
	seenUpdate    bool
	trajectoryID  string
	steps         map[int]*stepState
	turnSteps     map[int]struct{}
	childKeys     map[string]*childKey
	background    map[int]backgroundTask
	controls      map[int]*antigravityControl
	turnActive    bool
	sendInFlight  bool
	heldError     string
	interrupted   bool
	retryPending  bool
	planPath      string
	contextWindow int64
	contextTokens int64
	rowOpen       bool
	stopped       bool
	closed        bool
	connectErr    error
	connected     chan struct{}
	connectedOnce sync.Once
	done          chan struct{}
}

type stepState struct {
	status        string
	persisted     bool
	livePersisted bool
	spanID        string
	text          string
	thinking      string
	modelUsage    map[string]string
	raw           json.RawMessage
	background    bool
}

type childKey struct {
	role            string
	spawnSpanID     string
	prompt          string
	promptPersisted bool
	stored          bool
	idle            bool
	refused         bool
	linked          bool
	state           *cascadeState
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
	a := &Agent{
		sink: sink, rpc: rpc, workingDir: workingDir, workspace: workingDir,
		clock: clock, cascades: map[string]*cascadeState{}, mode: "default",
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
		background: map[int]backgroundTask{}, controls: map[int]*antigravityControl{},
		connected: make(chan struct{}), done: make(chan struct{}),
	}
}

func (a *Agent) installRoot(id string, stored bool) *cascadeState {
	root := newCascadeState(id, a.sink, a.generation, stored)
	a.root = root
	a.sessionID = id
	a.cascades = map[string]*cascadeState{id: root}
	return root
}

func (a *Agent) startCascadeStream(state *cascadeState) {
	a.streamWG.Add(1)
	go func() {
		defer a.streamWG.Done()
		defer close(state.done)
		a.runCascadeStream(state)
	}()
}

func (a *Agent) runCascadeStream(state *cascadeState) {
	delay := antigravityStreamRetryFirst
	emptyAttempts := 0
	undecodable := 0
	for {
		if a.streamCtx.Err() != nil {
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
		delivered, err := a.rpc.consumeStream(a.streamCtx, state.id, startIndex, func(update agentStateUpdate) error {
			first := firstOnAttempt
			firstOnAttempt = false
			if err := a.applyUpdate(state, update, first); err != nil {
				return err
			}
			return nil
		})
		if a.streamCtx.Err() != nil {
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
				statusErr := a.rpc.status(a.streamCtx)
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
		case <-a.streamCtx.Done():
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
	a.Mu.Lock()
	if a.cascades[state.id] != state || state.generation != a.generation || state.closed {
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
	a.Mu.Unlock()

	a.applyBackgroundTasks(state, update.BackgroundTasks, firstOnAttempt)

	patch := update.MainTrajectory.StepsUpdate
	for position, raw := range patch.Steps {
		if position >= len(patch.Indices) {
			break
		}
		index := patch.Indices[position]
		a.Mu.Lock()
		if index < state.seededCount {
			a.Mu.Unlock()
			continue
		}
		previous := state.steps[index]
		isNew := previous == nil
		if previous == nil {
			previous = &stepState{}
			state.steps[index] = previous
		}
		a.Mu.Unlock()
		if isNew {
			a.beginTurn(state)
		}
		a.Mu.Lock()
		state.turnSteps[index] = struct{}{}
		a.Mu.Unlock()
		step, err := decodeStep(raw)
		if err != nil {
			a.persistUnknownStep(state, index, raw)
			continue
		}
		a.applyStep(state, index, step, raw)
	}

	if update.ExecutionStatus.ModelRequest != nil {
		a.noteModelRetry(state, update.ExecutionStatus.ModelRequest)
	}
	if update.ContextWindowUsage != nil {
		a.publishContextUsage(state, update.ContextWindowUsage)
	}
	if update.Status == cascadeStatusRunning || update.Status == cascadeStatusBusy {
		a.beginTurn(state)
	}
	if update.Status == cascadeStatusIdle {
		a.endTurn(state, agent.MessageCompletionComplete)
	}
	a.ensureChildren(state)
	a.Mu.Lock()
	connectErr := state.connectErr
	a.Mu.Unlock()
	if connectErr == nil {
		state.connectedOnce.Do(func() { close(state.connected) })
	}
	return nil
}

func (a *Agent) firstOpenIndexLocked(state *cascadeState) int {
	first := -1
	last := state.seededCount
	for index, step := range state.steps {
		last = max(last, index+1)
		if index >= state.seededCount && isOpenStepStatus(step.status) {
			if _, background := state.background[index]; !background && (first < 0 || index < first) {
				first = index
			}
		}
	}
	if first >= 0 {
		return first
	}
	return last
}

func (a *Agent) beginTurn(state *cascadeState) {
	a.Mu.Lock()
	if state.turnActive || state.closed {
		a.Mu.Unlock()
		return
	}
	state.turnActive = true
	state.sendInFlight = false
	state.interrupted = false
	state.planPath = ""
	state.turnSteps = map[int]struct{}{}
	seq := a.NextTurnSeq()
	isChild := state.parent != nil
	rowKey, revive := state.id, state.rowOpen
	if isChild && !revive {
		state.rowOpen = true
	}
	a.Mu.Unlock()
	if isChild {
		if revive {
			providerkit.LogRegistryRefusal("antigravity", "revive", a.sink.ReviveBackgroundTask(rowKey))
		}
		providerkit.PublishSteerableTurnActiveTo(state.sink, true, seq)
		return
	}
	providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: true, Steerable: true}, seq)
	a.sink.ReportProgress(agent.ResetModelProgress())
}

func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{}
	if a.root != nil {
		state = agent.TurnState{Active: a.root.turnActive || a.root.sendInFlight, Steerable: a.root.turnActive}
	}
	seq := a.NextTurnSeq()
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
	a.Mu.Unlock()
	if state == nil {
		return nil, agent.ErrChildRouteNotReady
	}
	timer := a.clock.NewTimer(a.APITimeout(), antigravityChildAttachTimerTag)
	defer timer.Stop(antigravityChildAttachTimerTag)
	select {
	case <-state.connected:
		a.Mu.Lock()
		err := state.connectErr
		a.Mu.Unlock()
		if err != nil {
			return nil, err
		}
		return state, nil
	case <-timer.C:
		return nil, agent.ErrChildRouteNotReady
	case <-a.streamCtx.Done():
		return nil, fmt.Errorf("agent is stopped")
	}
}

func (a *Agent) ActiveChildTurnState(childKey string) agent.TurnState {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	state := a.cascades[childKey]
	if state == nil {
		return agent.TurnState{}
	}
	return agent.TurnState{Active: state.turnActive || state.sendInFlight, Steerable: state.turnActive}
}

func (a *Agent) SupportsSteering() bool     { return true }
func (a *Agent) SteerSurvivesTurnEnd() bool { return true }

func isInputNotRegistered(err error) bool {
	return providerkit.IsHTTPStatus(err, 500) && errors.Is(err, err) && containsInputNotRegistered(streamErrorText(err))
}

func containsInputNotRegistered(text string) bool {
	return len(text) >= len("input not registered") && stringContainsFold(text, "input not registered")
}

func stringContainsFold(text, substring string) bool {
	for index := 0; index+len(substring) <= len(text); index++ {
		match := true
		for offset := range len(substring) {
			left, right := text[index+offset], substring[offset]
			if left >= 'A' && left <= 'Z' {
				left += 'a' - 'A'
			}
			if left != right {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (a *Agent) SendRawInput(data []byte, stop agent.StopContext) error {
	return a.executeControlReply(data)
}

var _ = leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
