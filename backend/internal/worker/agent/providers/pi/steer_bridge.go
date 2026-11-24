package pi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/id"
)

//go:embed steer_identity.mjs
var piSteerIdentityProgram []byte

const piSteerMessageIDField = "leapmuxInputId"

type piSteerBridge struct {
	Capability   string `json:"capability"`
	Prefix       string `json:"prefix"`
	MessageField string `json:"messageField"`
	IDLength     int    `json:"idLength"`
}

func writePiSteerBridge(directory string) (string, *piSteerBridge, error) {
	nonce := id.Generate()
	bridge := &piSteerBridge{
		Capability: "leapmux-steer-proof-" + nonce, Prefix: "leapmux-steer:" + nonce + ":",
		MessageField: piSteerMessageIDField, IDLength: len(nonce),
	}
	raw, err := json.Marshal(bridge)
	if err != nil {
		return "", nil, fmt.Errorf("encode the Pi steer identity configuration: %w", err)
	}
	program := fmt.Appendf(nil, "const configuration = %s\n", raw)
	path := filepath.Join(directory, "leapmux-steer-identity.mjs")
	if err := os.WriteFile(path, append(program, piSteerIdentityProgram...), 0o600); err != nil {
		return "", nil, fmt.Errorf("write the Pi steer identity extension: %w", err)
	}
	return path, bridge, nil
}
