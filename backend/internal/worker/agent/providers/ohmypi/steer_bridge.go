package ohmypi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/id"
)

//go:embed steer_identity.mjs
var ompSteerIdentityProgram []byte

const ompSteerIdentityField = "leapmuxInputId"

type ompSteerBridge struct {
	Capability   string `json:"capability"`
	Prefix       string `json:"prefix"`
	ChildPrefix  string `json:"childPrefix"`
	MessageField string `json:"messageField"`
	IDLength     int    `json:"idLength"`
}

func writeOMPSteerBridge(directory string) (string, ompSteerBridge, error) {
	nonce := id.Generate()
	bridge := ompSteerBridge{
		Capability: "leapmux-steer-proof-" + nonce, Prefix: "leapmux-steer:" + nonce + ":",
		ChildPrefix:  "leapmux-child-input:" + nonce + ":",
		MessageField: ompSteerIdentityField, IDLength: len(nonce),
	}
	config, err := json.Marshal(bridge)
	if err != nil {
		return "", ompSteerBridge{}, fmt.Errorf("encode the Oh My Pi steer identity configuration: %w", err)
	}
	module := fmt.Appendf(nil, "const configuration = %s\n", config)
	path := filepath.Join(directory, "leapmux-steer-identity.mjs")
	if err := os.WriteFile(path, append(module, ompSteerIdentityProgram...), 0o600); err != nil {
		return "", ompSteerBridge{}, fmt.Errorf("write the Oh My Pi steer identity extension: %w", err)
	}
	return path, bridge, nil
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
