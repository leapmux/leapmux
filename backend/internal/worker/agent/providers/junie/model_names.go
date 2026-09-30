package junie

import (
	"strconv"
	"strings"
)

// model_names.go maps Junie's model ids between its CLI and ACP config option.
//
// Junie addresses a custom model profile by the profile id (`custom:mock-model`)
// on the command line and in the profile file, but by a decorated form on its
// model config option (`v1:6:custom:custom:mock-model`). The two must normalize
// to one id, or a startup compares the requested model against the decorated
// current, fails to apply it ("Unsupported or unavailable Junie model config
// value"), and the worker relaunches the agent to retry a write that cannot
// succeed.

const junieWireModelPrefix = "v1:6:custom:"

type junieWireModel struct {
	source string
	model  string
}

// parseJunieWireModel reads v1:<source byte count>:<source>:<model>. The
// source can contain colons, so splitting every colon loses provider identity.
func parseJunieWireModel(id string) (junieWireModel, bool) {
	rest, ok := strings.CutPrefix(id, "v1:")
	if !ok {
		return junieWireModel{}, false
	}
	sizeText, payload, ok := strings.Cut(rest, ":")
	if !ok {
		return junieWireModel{}, false
	}
	size, err := strconv.Atoi(sizeText)
	if err != nil || size <= 0 || size >= len(payload) || payload[size] != ':' {
		return junieWireModel{}, false
	}
	source, model := payload[:size], payload[size+1:]
	if model == "" {
		return junieWireModel{}, false
	}
	return junieWireModel{source: source, model: model}, true
}

// normalizeJunieModelID maps a decorated custom profile to its CLI handle.
// A proxy ID keeps its source because proxies can offer the same model name.
func normalizeJunieModelID(model string) string {
	if parsed, ok := parseJunieWireModel(model); ok && parsed.source == "custom" {
		return parsed.model
	}
	return model
}

// junieModelIDForWire decorates a custom profile and preserves all native IDs.
func junieModelIDForWire(model string) string {
	if _, ok := parseJunieWireModel(model); ok || !strings.HasPrefix(model, "custom:") {
		return model
	}
	return junieWireModelPrefix + model
}

// junieAlreadySelectedModel compares a bare launch model with Junie's current
// native wire selection. The source resolves duplicate names across proxies.
func junieAlreadySelectedModel(current, requested string) bool {
	if current == requested {
		return true
	}
	if strings.HasPrefix(requested, "v1:") {
		return false
	}
	selected, ok := parseJunieWireModel(current)
	return ok && selected.source != "custom" && selected.model == requested
}
