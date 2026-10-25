package deepseekharness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// createSession opens a new native Session, or the stored Session resume.
//
// dsh 0.2.1-rc.2 resumes a stored Session inside each Session method, and its
// `session/create` reuses a named Session only while that Session is blank: for
// a Session that started a turn, it mints a new identity. A stored Session
// therefore opens without `session/create`, and the first Session method of the
// startup resumes it. `session/create` still creates a Session that no store
// holds, under the requested identity.
func (a *Agent) createSession(ctx context.Context, resume string) (string, error) {
	if resume != "" {
		stored, err := a.storedSession(ctx, resume)
		if err != nil {
			return "", err
		}
		if stored {
			return resume, nil
		}
	}
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

// storedSession reports whether the native store holds sessionID. It refuses a
// stored Session of another working directory, as `session/create` does.
func (a *Agent) storedSession(ctx context.Context, sessionID string) (bool, error) {
	var projections *struct {
		Values struct {
			WorkingDirectory *string `json:"workingDirectory"`
		} `json:"values"`
	}
	if err := a.rpc.request(ctx, "session/projections", map[string]string{"sessionId": sessionID}, &projections); err != nil {
		return false, err
	}
	if projections == nil {
		return false, nil
	}
	if directory := projections.Values.WorkingDirectory; directory != nil && *directory != a.workingDir {
		return false, fmt.Errorf("DeepSeek Harness stored the Session in another working directory")
	}
	return true, nil
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
	defer a.queuePermissionApprovals()
	if raw, present := values["subagentCatalog"]; present {
		var entries []struct {
			ID        string  `json:"id"`
			CreatedAt *int64  `json:"createdAt"`
			Mode      string  `json:"mode"`
			Label     *string `json:"label"`
		}
		if json.Unmarshal(raw, &entries) != nil || providerkit.RawKindOf(raw) == providerkit.RawNull {
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
