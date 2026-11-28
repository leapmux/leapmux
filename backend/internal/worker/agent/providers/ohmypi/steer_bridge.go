package ohmypi

import (
	_ "embed"
	"encoding/json"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

//go:embed steer_identity.mjs
var ompSteerIdentityProgram []byte

type ompSteerBridge = providerkit.SteerIdentityBridge

func writeOMPSteerBridge(directory string) (string, ompSteerBridge, error) {
	return providerkit.WriteSteerIdentityBridge(directory, "Oh My Pi", ompSteerIdentityProgram, true)
}

func (a *Agent) observeOMPSteerCapability(raw []byte) {
	var frame struct {
		Commands []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"commands"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return
	}
	ready := false
	for _, command := range frame.Commands {
		if command.Source == "extension" && command.Name == a.steerBridge.Capability {
			ready = true
			break
		}
	}
	a.Mu.Lock()
	a.steerBridgeReady = ready
	a.Mu.Unlock()
}
