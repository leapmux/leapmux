package bgtask

import (
	"errors"
	"fmt"
)

// ErrConflictingOutcomes preserves contradictory finished evidence for the caller.
var ErrConflictingOutcomes = errors.New("the aliases have conflicting finished outcomes")

// ResolveAlias combines rows the provider proved describe the same operation.
// It never infers that relation from their child link or arrival order.
func (source Item) ResolveAlias(target Item) (Item, error) {
	if err := source.ValidateRole(); err != nil {
		return Item{}, err
	}
	if err := target.ValidateRole(); err != nil {
		return Item{}, err
	}
	if source.Role.CountsAsTask() != target.Role.CountsAsTask() {
		return Item{}, fmt.Errorf("the rename would combine independent child activity and task outcomes")
	}
	if source.ChildAgentID != "" && target.ChildAgentID != "" && source.ChildAgentID != target.ChildAgentID {
		return Item{}, fmt.Errorf("the aliases link different child transcripts")
	}
	if source.ParentAgentID != "" && target.ParentAgentID != "" && source.ParentAgentID != target.ParentAgentID {
		return Item{}, fmt.Errorf("the aliases belong to different parent agents")
	}
	if source.Role != RoleTaskOutcome || target.Role != RoleTaskOutcome {
		if source.Role.IsCanonicalChildActivity() && !target.Role.IsCanonicalChildActivity() {
			target.Role, target.ChildAgentID, target.Kind = source.Role, source.ChildAgentID, source.Kind
			return target.PreservingBlanksFrom(source), nil
		}
		return target, nil
	}
	sourceEvidence, targetEvidence := outcomeEvidence(source.Status), outcomeEvidence(target.Status)
	if sourceEvidence == 3 && targetEvidence == 3 && source.Status != target.Status {
		return Item{}, ErrConflictingOutcomes
	}
	if sourceEvidence > targetEvidence {
		target.Status, target.EndedAt = source.Status, source.EndedAt
	}
	target = target.PreservingBlanksFrom(source)
	if target.Status.IsFinished() {
		target.ActiveForm = ""
	}
	return target, nil
}

// Native outcomes are stronger evidence than a process interruption or an
// unknown end. Active replay never replaces any finished observation.
func outcomeEvidence(status Status) uint8 {
	switch status {
	case StatusUnspecified, StatusPending, StatusRunning, StatusPaused:
		return 0
	case StatusEndedWithUnknownOutcome:
		return 1
	case StatusInterrupted:
		return 2
	case StatusSucceeded, StatusFailed, StatusStopped:
		return 3
	}
	return 0
}
