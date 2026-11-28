package providerkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/id"
)

// SteerIdentityMessageField is the property added to tagged user messages by
// Pi-family steer identity extensions.
const SteerIdentityMessageField = "leapmuxInputId"

// SteerIdentityBridge describes one session-private steer identity extension
// configuration embedded at the top of leapmux-steer-identity.mjs.
type SteerIdentityBridge struct {
	Capability   string `json:"capability"`
	Prefix       string `json:"prefix"`
	ChildPrefix  string `json:"childPrefix,omitempty"`
	MessageField string `json:"messageField"`
	IDLength     int    `json:"idLength"`
}

// WriteSteerIdentityBridge generates a session-private nonce configuration and
// writes leapmux-steer-identity.mjs with 0600 permissions inside directory.
func WriteSteerIdentityBridge(directory, providerName string, program []byte, includeChildPrefix bool) (string, SteerIdentityBridge, error) {
	nonce := id.Generate()
	bridge := SteerIdentityBridge{
		Capability:   "leapmux-steer-proof-" + nonce,
		Prefix:       "leapmux-steer:" + nonce + ":",
		MessageField: SteerIdentityMessageField,
		IDLength:     len(nonce),
	}
	if includeChildPrefix {
		bridge.ChildPrefix = "leapmux-child-input:" + nonce + ":"
	}
	raw, err := json.Marshal(bridge)
	if err != nil {
		return "", SteerIdentityBridge{}, fmt.Errorf("encode the %s steer identity configuration: %w", providerName, err)
	}
	module := fmt.Appendf(nil, "const configuration = %s\n", raw)
	path := filepath.Join(directory, "leapmux-steer-identity.mjs")
	if err := os.WriteFile(path, append(module, program...), 0o600); err != nil {
		return "", SteerIdentityBridge{}, fmt.Errorf("write the %s steer identity extension: %w", providerName, err)
	}
	return path, bridge, nil
}
