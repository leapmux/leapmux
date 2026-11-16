package kimi

import (
	"encoding/json"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
)

// validateKimiTranscriptSnapshot checks the complete structure before any state changes.
// Native ordinals are signed integers; absent positions remain unavailable for attribution.
func validateKimiTranscriptSnapshot(snapshot *kimiTranscriptSnapshot) error {
	promptIDs := make(map[string]struct{})
	for _, raw := range snapshot.Prompts {
		prompt, valid := decodeKimiPromptRecord(raw)
		if !valid {
			return fmt.Errorf("the Kimi snapshot has an invalid prompt record")
		}
		if _, duplicate := promptIDs[prompt.PromptID]; duplicate {
			return fmt.Errorf("the Kimi snapshot repeats prompt %q", prompt.PromptID)
		}
		promptIDs[prompt.PromptID] = struct{}{}
	}
	turns := make(map[string]struct{})
	for _, raw := range snapshot.Items {
		var turn struct {
			Kind    string            `json:"kind"`
			State   string            `json:"state"`
			TurnID  string            `json:"turnId"`
			Ordinal *int64            `json:"ordinal"`
			Steps   []json.RawMessage `json:"steps"`
		}
		if err := json.Unmarshal(raw, &turn); err != nil {
			return fmt.Errorf("decode the Kimi snapshot item: %w", err)
		}
		if turn.Kind == "" {
			return fmt.Errorf("the Kimi snapshot item has no kind")
		}
		if turn.Kind != contracts.KimiTranscriptNodeTurn {
			continue
		}
		if !validKimiTranscriptTurnState(turn.State) {
			return fmt.Errorf("the Kimi snapshot turn has an invalid state %q", turn.State)
		}
		if turn.TurnID == "" {
			return fmt.Errorf("the Kimi snapshot turn has no identity")
		}
		if _, duplicate := turns[turn.TurnID]; duplicate {
			return fmt.Errorf("the Kimi snapshot repeats turn %q", turn.TurnID)
		}
		turns[turn.TurnID] = struct{}{}
		steps := make(map[string]struct{})
		for _, rawStep := range turn.Steps {
			var step struct {
				StepID  string            `json:"stepId"`
				TurnID  string            `json:"turnId"`
				Ordinal *int64            `json:"ordinal"`
				Frames  []json.RawMessage `json:"frames"`
			}
			if err := json.Unmarshal(rawStep, &step); err != nil {
				return fmt.Errorf("decode the Kimi snapshot step: %w", err)
			}
			if step.StepID == "" || step.TurnID != "" && step.TurnID != turn.TurnID {
				return fmt.Errorf("the Kimi snapshot step has no matching turn identity")
			}
			if _, duplicate := steps[step.StepID]; duplicate {
				return fmt.Errorf("the Kimi snapshot repeats step %q", step.StepID)
			}
			steps[step.StepID] = struct{}{}
			frames := make(map[string]struct{})
			for _, rawFrame := range step.Frames {
				var frame kimiTranscriptToolFrame
				if err := json.Unmarshal(rawFrame, &frame); err != nil {
					return fmt.Errorf("decode the Kimi snapshot frame: %w", err)
				}
				if frame.Kind == "" || frame.FrameID == "" {
					return fmt.Errorf("the Kimi snapshot frame has no identity")
				}
				if _, duplicate := frames[frame.FrameID]; duplicate {
					return fmt.Errorf("the Kimi snapshot repeats frame %q", frame.FrameID)
				}
				frames[frame.FrameID] = struct{}{}
				if frame.Kind != contracts.KimiTranscriptFrameTool || !kimiToolSpawns(frame.Name) {
					continue
				}
				refs := make(map[string]struct{}, len(frame.AgentRefs))
				for _, ref := range frame.AgentRefs {
					if kimiCheckID("child agent", ref.AgentID) != nil || ref.AgentID == kimiMainAgentID {
						return fmt.Errorf("the Kimi snapshot has an invalid child reference")
					}
					if _, duplicate := refs[ref.AgentID]; duplicate {
						return fmt.Errorf("the Kimi snapshot repeats child reference %q", ref.AgentID)
					}
					refs[ref.AgentID] = struct{}{}
				}
			}
		}
	}
	return nil
}

func validKimiTranscriptTurnState(state string) bool {
	switch state {
	case contracts.KimiTranscriptTurnQueued, contracts.KimiTranscriptTurnRunning, contracts.KimiTranscriptTurnCompleted, contracts.KimiTranscriptTurnFailed, contracts.KimiTranscriptTurnCancelled:
		return true
	default:
		return false
	}
}
