package pi

import (
	_ "embed"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

//go:embed steer_identity.mjs
var piSteerIdentityProgram []byte

const piSteerMessageIDField = providerkit.SteerIdentityMessageField

type piSteerBridge = providerkit.SteerIdentityBridge

func writePiSteerBridge(directory string) (string, *piSteerBridge, error) {
	path, bridge, err := providerkit.WriteSteerIdentityBridge(directory, "Pi", piSteerIdentityProgram, false)
	if err != nil {
		return "", nil, err
	}
	return path, &bridge, nil
}
