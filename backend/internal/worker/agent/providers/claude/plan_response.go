package claude

import (
	"encoding/json"
	"errors"

	"github.com/leapmux/leapmux/internal/util/jsonfield"
)

// applyClaudePlanPermission uses the native permission update that accompanies a tool approval.
// Claude applies this update before the approved tool's post-tool hook runs.
func applyClaudePlanPermission(content []byte, mode string) ([]byte, error) {
	path := []string{"response", "response", "updatedPermissions"}
	permissions, err := jsonfield.Get(content, path...)
	if errors.Is(err, jsonfield.ErrMissing) {
		permissions = []byte(`[]`)
	} else if err != nil {
		return nil, err
	}
	update, err := json.Marshal(map[string]string{"type": "setMode", "mode": mode, "destination": "session"})
	if err != nil {
		return nil, err
	}
	permissions, err = jsonfield.Append(permissions, update)
	if err != nil {
		return nil, err
	}
	return jsonfield.Set(content, permissions, path...)
}

// applyClaudeSessionPermission attaches the remembered grant a session-scoped
// allow carries: one allow rule for the approved tool, kept in memory for the
// session alone. The rule shape is Claude Code's own (a `toolName` with no
// `ruleContent` allows the tool), and a session destination writes no rule file.
func applyClaudeSessionPermission(content []byte, toolName string) ([]byte, error) {
	if toolName == "" {
		return nil, errors.New("the session grant names no tool")
	}
	path := []string{"response", "response", "updatedPermissions"}
	permissions, err := jsonfield.Get(content, path...)
	if errors.Is(err, jsonfield.ErrMissing) {
		permissions = []byte(`[]`)
	} else if err != nil {
		return nil, err
	}
	update, err := json.Marshal(map[string]any{
		"type":        "addRules",
		"rules":       []map[string]string{{"toolName": toolName}},
		"behavior":    "allow",
		"destination": "session",
	})
	if err != nil {
		return nil, err
	}
	permissions, err = jsonfield.Append(permissions, update)
	if err != nil {
		return nil, err
	}
	return jsonfield.Set(content, permissions, path...)
}
