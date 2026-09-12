package agent

import (
	"bytes"
	"encoding/json"
)

// JSONRPCControlRequestID keeps native string IDs separate from numeric IDs.
// The original payload retains the native ID bytes. This key belongs to LeapMux only.
func JSONRPCControlRequestID(nativeID json.RawMessage) (string, bool) {
	nativeID = bytes.TrimSpace(nativeID)
	if len(nativeID) == 0 || bytes.Equal(nativeID, []byte("null")) {
		return "", false
	}
	if nativeID[0] == '"' {
		var value string
		if json.Unmarshal(nativeID, &value) != nil {
			return "", false
		}
		canonical, _ := json.Marshal(value)
		return "jsonrpc:" + string(canonical), true
	}
	var number json.Number
	if json.Unmarshal(nativeID, &number) != nil {
		return "", false
	}
	return "jsonrpc:" + string(nativeID), true
}

// storedControlRequestID uses the persisted identity when the service supplies it.
// Direct provider calls can use the native identity without a stored request.
func storedControlRequestID(ctx ControlResponseContext, nativeID string) string {
	if ctx.RequestID != "" {
		return ctx.RequestID
	}
	return nativeID
}
