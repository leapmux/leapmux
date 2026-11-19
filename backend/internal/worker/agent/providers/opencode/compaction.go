package opencode

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (s *openCodeSessionRuntime) compactNativeContext() error {
	s.operations.Lock()
	defer s.operations.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.compacting {
		s.mu.Unlock()
		return agent.ErrAgentBusy
	}
	previous := s.turnStateLocked()
	seq := s.claimTurnSequence()
	if seq == 0 {
		s.mu.Unlock()
		return agent.ErrInputSessionChanged
	}
	s.compacting = true
	s.activityVersion++
	version := s.activityVersion
	state := s.turnStateLocked()
	inputID := "msg_leapmux_compact_" + id.Generate()
	admission := &openCodeInputAdmission{}
	if s.admissions == nil {
		s.admissions = make(map[string]*openCodeInputAdmission)
	}
	s.admissions[inputID] = admission
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.admissions, inputID); s.mu.Unlock() }()
	s.publishState(state, seq)
	_, err := s.rpc.compact(s.ctx, s.id, openCodeCompactionRequest{ID: inputID, Delivery: "queue"})
	s.mu.Lock()
	confirmed := admission.admitted
	s.mu.Unlock()
	if openCodeDeliveryUncertain(err) {
		defer s.requestRecovery()
	}
	if err == nil || confirmed {
		return nil
	}
	if !openCodeDeliveryUncertain(err) {
		s.mu.Lock()
		if s.activityVersion == version {
			s.active, s.compacting = previous.Active, false
			s.activityVersion++
		}
		state, seq = s.turnStateLocked(), s.claimTurnSequence()
		s.mu.Unlock()
		s.publishState(state, seq)
		return err
	}
	confirmed, readErr := s.inputAdmitted(inputID, "compaction")
	if confirmed {
		return nil
	}
	return fmt.Errorf("%w: OpenCode did not confirm compaction admission: %w", agent.ErrDeliveryUncertain, errors.Join(err, readErr))
}
