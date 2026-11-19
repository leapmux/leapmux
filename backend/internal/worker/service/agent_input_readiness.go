package service

import (
	"errors"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type inputReadinessAdmission struct{ source nativeSessionSource }
type inputReadinessLease struct {
	once    sync.Once
	release func()
}

func (lease *inputReadinessLease) Release() { lease.once.Do(lease.release) }

func (admission inputReadinessAdmission) Acquire() agent.InputReadinessLease {
	release := admission.source.lock()
	if !admission.source.currentLocked() {
		release()
		return nil
	}
	return &inputReadinessLease{release: release}
}

func (svc *Service) childInputReady(childID string) func(agent.CapturedTranscript) error {
	return func(captured agent.CapturedTranscript) error {
		content := captured.FrozenContent()
		if content.Publication == nil {
			return errors.New("the child input readiness has no captured source")
		}
		owner, ok := content.Publication.Owner().(*transcriptOwner)
		if !ok || owner == nil || owner.sink == nil || owner.sink.h != svc.Output || owner.sink.agentID != childID {
			return errors.New("the child input readiness belongs to another source")
		}
		source, err := owner.sink.capturedNativeSessionSource(captured)
		if err != nil {
			return err
		}
		if svc.InputQueue != nil {
			svc.InputQueue.NotifyInputReady(childID, inputReadinessAdmission{source: source})
		}
		return nil
	}
}
