package qwen

import (
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// qwenStaticOptionGroups holds Qwen's static approval-mode and output-style
// groups. The registration and the start both read the mode list, so the
// static fallback and a running agent's fallback modes cannot drift.
var qwenStaticOptionGroups = append(acp.StaticSecondaryGroup(acp.ModeChannelPermissionMode, qwenModes()),
	qwenOutputStyleGroup("", qwenBuiltinOutputStyles))

// OptionOutputStyle is the Output Style axis. Its values are Qwen's own style
// names, which `--output-style` takes at launch, and LeapMux's Auto, which
// sends no flag so that the style of Qwen's own settings applies.
const (
	OptionOutputStyle   = "outputStyle"
	qwenOutputStyleAuto = "auto"
)

// qwenBuiltinOutputStyles are the styles that Qwen Code 0.26.0 offers in every
// session. A user can add styles of their own in Qwen's settings; a name that
// Qwen does not know falls back to its default style with a warning, so a
// stored value from a newer CLI still launches.
var qwenBuiltinOutputStyles = []string{"Concise", "Proactive", "Explanatory", "Learning"}

// qwenOutputStyleGroup lists Auto and the given native styles, each once. An
// empty current value is Auto.
func qwenOutputStyleGroup(current string, styles []string) *leapmuxv1.AvailableOptionGroup {
	defs := []agent.OptionDef{{Id: qwenOutputStyleAuto, Name: "Auto", Description: "Use the output style of Qwen's own settings.", Default: true}}
	seen := map[string]bool{qwenOutputStyleAuto: true}
	for _, style := range styles {
		if strings.TrimSpace(style) == "" || seen[style] {
			continue
		}
		seen[style] = true
		defs = append(defs, agent.OptionDef{Id: style, Name: style})
	}
	if current == "" {
		current = qwenOutputStyleAuto
	}
	return agent.SelectGroup(OptionOutputStyle, "Output Style", agent.OptionOrderProviderSecond, current, defs)
}

// qwenLocator finds the Qwen Code CLI on the user's PATH.
var qwenLocator = launch.Binaries("qwen")

// Registration states everything the worker knows about Qwen Code before any
// of its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE,
		Plugin:   qwenProvider{},
		Start:    Start,
		Locator:  qwenLocator,
		// Qwen lists its models from the user's settings and environment, so the
		// session is the only source of the catalog.
		DefaultModels: nil,
		OptionGroups:  qwenStaticOptionGroups,
		// The reasoning effort is a server-driven config option whose menu belongs
		// to the model, so it is known before the session reports it. The output
		// style group is static, but its launch-only axis joins the known ids so
		// a stored value survives a settings write.
		AdditionalOptionIDs: []string{contracts.QwenConfigReasoningEffort, OptionOutputStyle},
		PermissionDefaults: agent.PermissionDefaults{
			// Both halves are Default. Qwen's own default is `auto`, whose classifier
			// approves actions with no prompt, so neither a new nor a resumed
			// session may inherit it.
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.QwenModeDefault},
			Fallback:   contracts.QwenModeDefault,
		},
		EnvModelKey:  "LEAPMUX_QWEN_DEFAULT_MODEL",
		EnvEffortKey: "LEAPMUX_QWEN_DEFAULT_EFFORT",
	}
}
