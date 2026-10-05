package deepseekharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (a *Agent) createSession(ctx context.Context, resume string) (string, error) {
	request := map[string]any{"cwd": a.workingDir}
	if resume != "" {
		request["sessionId"] = resume
	}
	var created struct {
		ID string `json:"sessionId"`
	}
	if err := a.rpc.request(ctx, "session/create", request, &created); err != nil {
		return "", err
	}
	if created.ID == "" || resume != "" && created.ID != resume {
		return "", fmt.Errorf("DeepSeek Harness returned an invalid Session identity")
	}
	return created.ID, nil
}

type nativeCommandResult struct {
	Result struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"result"`
}

func (a *Agent) executeCommand(sessionID, line string) error {
	var result nativeCommandResult
	if err := a.rpc.call(a.Context(), "commands/execute", map[string]any{"agentId": sessionID, "line": line, "submittedAttachments": []any{}}, &result); err != nil {
		return err
	}
	if result.Result.Kind != "success" {
		return fmt.Errorf("DeepSeek Harness refused the command: %s", result.Result.Text)
	}
	return nil
}

func (a *Agent) userCommand(sessionID, content string) (bool, error) {
	words := strings.Fields(content)
	if len(words) == 0 || !strings.HasPrefix(words[0], "/") {
		return false, nil
	}
	switch words[0] {
	case "/compact":
		return true, a.compact(sessionID, content)
	case "/plan", "/permission", "/goal":
		err := a.executeCommand(sessionID, content)
		// These commands start no turn of their own, so no native event ends the turn that the
		// input queue opened for the input. Publish the real state to release it. A turn that
		// the command did start (a goal round) keeps the state active.
		a.PublishTurnActive()
		return true, err
	default:
		return false, nil
	}
}

// compact runs a native compaction command. The native process finishes the compaction inside the
// command call and emits `compaction/start` and `compaction/end`, but no turn event. The Worker
// holds the turn for the call. The reader sees the agent busy, and the input queue keeps later
// input until the compaction ends, whether the command succeeds or fails.
// Source: `@deepseek-ai/dsh` 0.2.0-rc.2, probed on a live Session.
func (a *Agent) compact(sessionID, line string) error {
	a.Mu.Lock()
	idle := !a.active
	a.Mu.Unlock()
	if !idle {
		return a.executeCommand(sessionID, line)
	}
	a.setTurnState(true)
	defer a.setTurnState(false)
	return a.executeCommand(sessionID, line)
}

func (a *Agent) applyProjections(stream *sessionStream, values map[string]json.RawMessage, snapshot bool) error {
	if stream.childAgentID != "" {
		return nil
	}
	if raw, present := values["subagentCatalog"]; present {
		var entries []struct {
			ID        string  `json:"id"`
			CreatedAt *int64  `json:"createdAt"`
			Mode      string  `json:"mode"`
			Label     *string `json:"label"`
		}
		if json.Unmarshal(raw, &entries) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("DeepSeek Harness returned an invalid native child catalog projection")
		}
		for _, entry := range entries {
			if entry.ID == "" || entry.CreatedAt == nil || *entry.CreatedAt < 0 || entry.Mode == "continuable" && entry.Label == nil {
				return fmt.Errorf("DeepSeek Harness child catalog projection has an invalid native identity")
			}
			descriptor := nativeChildDescriptor{ID: entry.ID, Mode: entry.Mode}
			if entry.Label != nil {
				descriptor.Label = *entry.Label
			}
			if err := a.beginOwnedChild(stream, descriptor); err != nil {
				return err
			}
		}
	}
	var plan struct {
		Active bool `json:"active"`
	}
	if raw := values["plan"]; len(raw) > 0 && json.Unmarshal(raw, &plan) == nil {
		mode := "act"
		if plan.Active {
			mode = "plan"
		}
		a.Mu.Lock()
		a.mode = mode
		a.Mu.Unlock()
	}
	var permissions struct {
		Value string `json:"currentValue"`
	}
	if raw := values["permissions"]; len(raw) > 0 && json.Unmarshal(raw, &permissions) == nil && permissions.Value != "" {
		a.Mu.Lock()
		a.permissions = permissions.Value
		a.Mu.Unlock()
	}
	var model struct {
		Next *modelSelection `json:"next"`
	}
	if raw := values["modelSelection"]; len(raw) > 0 && json.Unmarshal(raw, &model) == nil && model.Next != nil {
		a.Mu.Lock()
		a.selection = *model.Next
		a.Mu.Unlock()
	}
	if raw, ok := values["goal"]; ok {
		if err := a.applyGoalProjection(raw, snapshot); err != nil {
			return err
		}
	}
	return nil
}

// Native history verifies that this descriptor belongs to the child's own log suffix.
func childOwnSequence(stream *sessionStream, values map[string]json.RawMessage, cursor int64) (int64, error) {
	var identity struct {
		Mode string `json:"mode"`
		Seq  *int64 `json:"seq"`
	}
	raw := values["subagent"]
	if json.Unmarshal(raw, &identity) != nil || identity.Seq == nil || *identity.Seq < 0 || *identity.Seq > cursor ||
		identity.Mode != stream.address.Mode {
		return 0, fmt.Errorf("DeepSeek Harness child snapshot has no exact native descriptor")
	}
	return *identity.Seq, nil
}
