package goose

import (
	"strconv"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// UpdateSettings applies a change of Native Commands through a restart,
// because Goose reads the terminal capability only in the initialize
// handshake. Every other axis goes to the shared ACP writer.
func (a *Agent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if options.Get(OptionNativeCommands) != "" {
		requested, err := options.Bool(OptionNativeCommands)
		if err != nil {
			return a.SettingsSnapshot()
		}
		if requested != a.nativeCommands {
			return agent.RestartRequiredSettings(options)
		}
	}
	return a.Base.UpdateSettings(options)
}

// nativeCommandGroups serves the Native Commands group with the launch value.
func (a *Agent) nativeCommandGroups() []*leapmuxv1.AvailableOptionGroup {
	return []*leapmuxv1.AvailableOptionGroup{providerkit.LiveGroup(gooseNativeCommandsGroup, strconv.FormatBool(a.nativeCommands))}
}
