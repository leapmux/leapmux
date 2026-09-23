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
