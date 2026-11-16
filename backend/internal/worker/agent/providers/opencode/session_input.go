package opencode

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// openCodeInputIDPrefix identifies user records whose rows the Worker owns.
const openCodeInputIDPrefix = "msg_leapmux_"

type openCodeSessionServices struct {
	sink agent.ProviderServices
	// A zero sequence refuses publication from a retired session.
	claimTurnSequence func() uint64
	// requestRecovery schedules stream restoration without waiting for it.
	requestRecovery func()
}

// openCodeSessionRuntime owns admission and activity for one native session.
// operations orders input against settings changes. Native events take mu only.
type openCodeSessionRuntime struct {
	operations sync.Mutex
	mu         sync.Mutex
	ctx        context.Context
	rpc        openCodeRPC
	openCodeSessionServices
	id              string
	parentID        string
	location        openCodeLocation
	selectedAgentID string
	selectedModel   *openCodeModelRef
	catalog         openCodeCatalog
	active          bool
	compacting      bool
	activityVersion uint64
	settingsVersion uint64
	admissions      map[string]*openCodeInputAdmission
	steers          providerkit.SteerLedger
}

func newOpenCodeSessionRuntime(ctx context.Context, rpc openCodeRPC, info openCodeSessionInfo, catalog openCodeCatalog, services openCodeSessionServices) (*openCodeSessionRuntime, error) {
	if ctx == nil || rpc.endpoint == nil {
		return nil, errors.New("an OpenCode session requires context and transport")
	}
	if services.sink == nil || services.claimTurnSequence == nil || services.requestRecovery == nil {
		return nil, errors.New("OpenCode session services require output, turn sequencing, and recovery notification")
	}
	if err := openCodeRouteID(info.ID); err != nil {
		return nil, err
	}
	services.sink = agent.NewOrderedProviderServices(agent.OutputQueueFor(services.sink), services.sink)
	return &openCodeSessionRuntime{
		ctx: ctx, rpc: rpc, openCodeSessionServices: services,
		id: info.ID, parentID: info.ParentID, location: info.Location,
		selectedAgentID: info.Agent, selectedModel: copyOpenCodeModelRef(info.Model), catalog: catalog,
	}, nil
}

func (s *openCodeSessionRuntime) SupportsSteering() bool { return true }
func (s *openCodeSessionRuntime) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return s.sendInput(nil, content, attachments, false)
}
func (s *openCodeSessionRuntime) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return s.sendInput(&sessionID, content, attachments, false)
}
func (s *openCodeSessionRuntime) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return s.sendInput(nil, content, attachments, true)
}

func (s *openCodeSessionRuntime) turnStateLocked() agent.TurnState {
	return agent.TurnState{Active: s.active || s.compacting, Steerable: s.active && !s.compacting}
}
func (s *openCodeSessionRuntime) PublishTurnActive() agent.TurnState {
	s.mu.Lock()
	state, seq := s.turnStateLocked(), s.claimTurnSequence()
	s.mu.Unlock()
	return s.publishState(state, seq)
}

func (s *openCodeSessionRuntime) publishState(state agent.TurnState, seq uint64) agent.TurnState {
	if seq == 0 {
		return agent.TurnState{}
	}
	return providerkit.PublishTurnStateTo(s.sink, state, seq)
}

// setNativeActivity records every native observation, including one equal to
// optimistic input activity. Its version prevents an older HTTP refusal rollback.
func (s *openCodeSessionRuntime) setNativeActivity(active, compacting bool) {
	s.mu.Lock()
	previous := s.turnStateLocked()
	s.active, s.compacting = active, compacting
	s.activityVersion++
	state, seq := s.turnStateLocked(), s.claimTurnSequence()
	s.mu.Unlock()
	if state != previous {
		s.publishState(state, seq)
	}
}

// checkInputActivityLocked requires s.mu.
func (s *openCodeSessionRuntime) checkInputActivityLocked(steer bool) error {
	state := s.turnStateLocked()
	if steer && !state.Steerable {
		return agent.ErrNoActiveTurn
	}
	if !steer && state.Active {
		return agent.ErrAgentBusy
	}
	return nil
}

func (s *openCodeSessionRuntime) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) (sendErr error) {
	s.operations.Lock()
	defer s.operations.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if err := providerkit.CheckInputSession(expected, s.id); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.checkInputActivityLocked(steer); err != nil {
		s.mu.Unlock()
		return err
	}
	model, err := s.catalog.effectiveModel(s.selectedModel, s.selectedAgentID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	nativeModel := s.catalog.findModel(openCodeModelKey(model))
	sessionID := s.id
	s.mu.Unlock()
	request, err := buildOpenCodePrompt(content, attachments, nativeModel)
	if err != nil {
		return err
	}
	request.ID = openCodeInputIDPrefix + id.Generate()
	// Native queued input waits behind pending steer reminders and can miss the next model step.
	// The Worker decides when ordinary input dispatches; both native submissions use steer.
	request.Delivery = "steer"
	s.mu.Lock()
	// Activity can change while the attachment builder validates local data.
	if err := s.checkInputActivityLocked(steer); err != nil {
		s.mu.Unlock()
		return err
	}
	previous := s.turnStateLocked()
	seq := s.claimTurnSequence()
	if seq == 0 {
		s.mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	s.active = true
	s.activityVersion++
	version := s.activityVersion
	admission := &openCodeInputAdmission{}
	if s.admissions == nil {
		s.admissions = make(map[string]*openCodeInputAdmission)
	}
	s.admissions[request.ID] = admission
	var ticket providerkit.SteerTicket
	if steer && s.parentID == "" {
		ticket = s.steers.Add(request.ID)
		s.steers.SetID(ticket, request.ID)
	}
	state := s.turnStateLocked()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.admissions, request.ID)
		admitted := admission.admitted
		s.mu.Unlock()
		if sendErr != nil && ticket != 0 && !admitted {
			s.steers.Forget(ticket)
			s.steers.Settle(s.sink)
		}
	}()
	s.publishState(state, seq)
	_, err = s.rpc.prompt(s.ctx, sessionID, request)
	if openCodeDeliveryUncertain(err) {
		defer s.requestRecovery()
	}
	s.mu.Lock()
	admitted := admission.admitted
	s.mu.Unlock()
	if err == nil || admitted {
		return nil
	}
	if !openCodeDeliveryUncertain(err) {
		s.mu.Lock()
		if s.activityVersion == version {
			s.active, s.compacting = previous.Active, !previous.Steerable && previous.Active
			s.activityVersion++
		}
		state, seq := s.turnStateLocked(), s.claimTurnSequence()
		s.mu.Unlock()
		s.publishState(state, seq)
		return err
	}
	confirmed, reconcileErr := s.reconcileInput(request.ID, version)
	s.mu.Lock()
	admission.admitted = admission.admitted || confirmed
	confirmed = admission.admitted
	s.mu.Unlock()
	if confirmed {
		return nil
	}
	return fmt.Errorf("%w: OpenCode did not confirm input delivery: %w", agent.ErrDeliveryUncertain, errors.Join(err, reconcileErr))
}

// reconcileInput separates native activity from an uncertain HTTP outcome.
// An absent input does not prove rejection: an interrupted handler can still
// finish admission. Only its native ID can confirm delivery; this method never
// resubmits it. Native events can announce a later admission normally.
func (s *openCodeSessionRuntime) reconcileInput(inputID string, version uint64) (bool, error) {
	var confirmed bool
	var active map[string]bool
	var admissionErr, activityErr error
	var reads sync.WaitGroup
	reads.Go(func() { confirmed, admissionErr = s.inputAdmitted(inputID, contracts.OpenCodeNativeMessageTypeUser) })
	reads.Go(func() { active, activityErr = s.rpc.activeSessions(s.ctx) })
	reads.Wait()
	s.mu.Lock()
	if admission := s.admissions[inputID]; admission != nil {
		admission.admitted = admission.admitted || confirmed
		confirmed = admission.admitted
	}
	if activityErr != nil {
		s.mu.Unlock()
		return confirmed, errors.Join(admissionErr, activityErr)
	}
	if s.activityVersion == version {
		s.active, s.compacting = active[s.id], false
		s.activityVersion++
	}
	state, seq := s.turnStateLocked(), s.claimTurnSequence()
	s.mu.Unlock()
	s.publishState(state, seq)
	return confirmed, admissionErr
}

// inputAdmitted checks both queued and delivered native records. An error from
// one reader cannot invalidate an input ID confirmed by the other reader.
func (s *openCodeSessionRuntime) inputAdmitted(inputID, kind string) (bool, error) {
	pending, pendingErr := s.rpc.pendingInbox(s.ctx, s.id)
	for _, item := range pending {
		if item.ID == inputID && item.Type == kind {
			return true, pendingErr
		}
	}
	confirmed := false
	historyErr := s.rpc.visitMessages(s.ctx, s.id, func(message openCodeStoredMessage) error {
		if message.ID == inputID && message.Type == kind {
			confirmed = true
		}
		return nil
	})
	return confirmed, errors.Join(pendingErr, historyErr)
}

type openCodeInputAdmission struct{ admitted bool }

func (s *openCodeSessionRuntime) finishPendingSteers() {
	for _, ticket := range s.steers.Pending() {
		s.steers.Uncertain(ticket)
	}
	s.steers.Settle(s.sink)
}

func copyOpenCodeModelRef(model *openCodeModelRef) *openCodeModelRef {
	if model == nil {
		return nil
	}
	copied := *model
	return &copied
}

func (s *openCodeSessionRuntime) observeSessionInfo(info openCodeSessionInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info.ID != s.id {
		return fmt.Errorf("the OpenCode session update belongs to %q instead of %q", info.ID, s.id)
	}
	if info.ParentID != s.parentID {
		return fmt.Errorf("the OpenCode session update changes parent ownership from %q to %q", s.parentID, info.ParentID)
	}
	s.selectedAgentID = info.Agent
	s.selectedModel = copyOpenCodeModelRef(info.Model)
	s.location = info.Location
	s.settingsVersion++
	return nil
}
