package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one native Web process and one root Session.
type Agent struct {
	providerkit.Process
	sink            agent.ProviderServices
	rpc             remoteRPC
	workingDir      string
	opMu            sync.Mutex
	writeMu         sync.Mutex
	dispatchMu      sync.Mutex
	conn            *websocket.Conn
	streamCancel    context.CancelFunc
	streamDone      chan struct{}
	ready           chan struct{}
	readyOnce       sync.Once
	discard         atomic.Bool
	sessionID       string
	clientID        string
	catalog         modelCatalog
	selection       modelSelection
	mode            string
	permissions     string
	active          bool
	streams         map[string]*sessionStream
	controls        map[string]nativeControl
	children        map[string]*nativeChild
	childCatalog    map[string]nativeChildDescriptor
	workflows       map[string]nativeWorkflow
	directory       *agentdir.Dir
	imageReceipts   imageHookFiles
	streamFailure   error
	failureStopDone chan struct{}
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.ContextCompactor = (*Agent)(nil)
var _ agent.GoalWriter = (*Agent)(nil)
var _ agent.ChildSteerer = (*Agent)(nil)
var _ agent.ChildInterrupter = (*Agent)(nil)

func newAgent(sink agent.ProviderServices, workingDir string) *Agent {
	return &Agent{sink: sink, workingDir: workingDir, streamDone: make(chan struct{}), ready: make(chan struct{}), streams: map[string]*sessionStream{}, controls: map[string]nativeControl{}, children: map[string]*nativeChild{}, childCatalog: map[string]nativeChildDescriptor{}, workflows: map[string]nativeWorkflow{}, mode: "act", permissions: "workspace-write"}
}

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput("", content, attachments, "queue")
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	if sessionID == "" {
		return agent.ErrInputSessionChanged
	}
	return a.sendInput(sessionID, content, attachments, "queue")
}

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput("", content, attachments, "steer")
}

func (a *Agent) SupportsSteering() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return !a.StoppedLocked() && a.streamFailure == nil && a.sessionID != ""
}

func (a *Agent) sendInput(expected, content string, attachments []*leapmuxv1.Attachment, delivery string) error {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	a.Mu.Lock()
	sessionID := a.sessionID
	if a.streamFailure != nil {
		failure := a.streamFailure
		a.Mu.Unlock()
		return fmt.Errorf("DeepSeek Harness lost its native Remote stream: %w", failure)
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("DeepSeek Harness is stopped")
	}
	if sessionID == "" || expected != "" && expected != sessionID {
		a.Mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	if delivery == "queue" && a.active {
		a.Mu.Unlock()
		return agent.ErrAgentBusy
	}
	if delivery == "steer" && !a.active {
		a.Mu.Unlock()
		return agent.ErrNoActiveTurn
	}
	a.Mu.Unlock()
	if len(attachments) == 0 {
		handled, err := a.userCommand(sessionID, content)
		if handled || err != nil {
			return err
		}
	}
	parts, err := a.promptParts(sessionID, content, attachments)
	if err != nil {
		return err
	}
	if delivery == "queue" {
		a.Mu.Lock()
		a.active = true
		seq := a.NextTurnSeq()
		a.Mu.Unlock()
		a.sink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, seq)
	}
	var receipt struct {
		Accepted bool `json:"accepted"`
	}
	err = a.rpc.request(a.Context(), "session/prompt", map[string]any{"requestId": uuid.NewString(), "sessionId": sessionID, "mode": delivery, "content": parts}, &receipt)
	if err == nil && !receipt.Accepted {
		err = fmt.Errorf("DeepSeek Harness did not accept the prompt")
	}
	if err == nil {
		return nil
	}
	var native *remoteFailure
	if errors.As(err, &native) {
		if delivery == "queue" {
			a.setTurnState(false)
		}
		return err
	}
	return fmt.Errorf("%w: DeepSeek Harness did not confirm the prompt: %w", agent.ErrDeliveryUncertain, err)
}

func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.active, Steerable: a.active}
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(state, seq)
	return state
}

func (a *Agent) setTurnState(active bool) {
	a.Mu.Lock()
	a.active = active
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: active, Steerable: active}, seq)
}

func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	sessionID, active := a.sessionID, a.active
	a.Mu.Unlock()
	if !active {
		return nil
	}
	return a.rpc.request(a.Context(), "session/cancel", map[string]string{"sessionId": sessionID}, nil)
}

// The Web API has no synchronous Session disposal operation. The Worker
// replaces the process so reset stops its descendants and retains no old Agent.
func (a *Agent) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }

func (a *Agent) CompactContext() error {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	a.Mu.Lock()
	sessionID := a.sessionID
	active := a.active
	a.Mu.Unlock()
	if active {
		return agent.ErrAgentBusy
	}
	return a.executeCommand(sessionID, "/compact")
}

func (a *Agent) DiscardOutput() { a.discard.Store(true); a.Process.DiscardOutput() }

func (a *Agent) SendRawInput(raw []byte) error { return a.answerControl(raw) }

// HandleOutput accepts one native mux frame for provider tests.
func (a *Agent) HandleOutput(raw []byte) {
	if err := a.handleFrame(raw); err != nil {
		a.reportStreamFailure(err)
	}
}

func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.Process.Stop()
	a.stopConnection()
}

func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stopConnection()
	a.Mu.Lock()
	failure, stopDone := a.streamFailure, a.failureStopDone
	a.Mu.Unlock()
	if stopDone != nil {
		<-stopDone
	}
	return errors.Join(err, failure, a.directory.Close())
}

func (a *Agent) ProcessExitCompletion() agent.MessageCompletion {
	a.Mu.Lock()
	failed := a.streamFailure != nil
	a.Mu.Unlock()
	if failed {
		return agent.MessageCompletionError
	}
	return a.Process.ProcessExitCompletion()
}

func (a *Agent) stopConnection() {
	if a.streamCancel != nil {
		a.streamCancel()
	}
	if a.conn != nil {
		_ = a.conn.CloseNow()
	}
	if a.rpc.endpoint != nil {
		a.rpc.endpoint.Close()
	}
	if a.streamDone != nil && a.conn != nil {
		<-a.streamDone
	}
}

func (a *Agent) session() string { a.Mu.Lock(); defer a.Mu.Unlock(); return a.sessionID }

func encodeJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode the DeepSeek Harness payload: %w", err)
	}
	return raw, nil
}
