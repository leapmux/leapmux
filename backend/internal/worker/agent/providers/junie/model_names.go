package junie

import "strings"

// model_names.go maps Junie's decorated wire model ids to the plain ids the
// worker keeps and back.
//
// Junie addresses a custom model profile by the profile id (`custom:mock-model`)
// on the command line and in the profile file, but by a decorated form on its
// model config option (`v1:6:custom:custom:mock-model`). The two must normalize
// to one id, or a startup compares the requested model against the decorated
// current, fails to apply it ("Unsupported or unavailable Junie model config
// value"), and the worker relaunches the agent to retry a write that cannot
// succeed.

// junieWireModelPrefix is the decoration Junie puts in front of a custom
// profile id on the wire.
const junieWireModelPrefix = "v1:6:custom:"

// normalizeJunieModelID maps the decorated wire id back to the profile id the
// worker keeps, and leaves any other id unchanged.
func normalizeJunieModelID(model string) string {
	return strings.TrimPrefix(model, junieWireModelPrefix)
}

// junieModelIDForWire maps the profile id the worker keeps to the decorated
// form Junie's model config option takes. A profile id is the only form Junie
// decorates, so any other id is sent unchanged.
func junieModelIDForWire(model string) string {
	if strings.HasPrefix(model, junieWireModelPrefix) || !strings.HasPrefix(model, "custom:") {
		return model
	}
	return junieWireModelPrefix + model
}
