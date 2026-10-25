package qoder

import (
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// qoderLocator finds the Qoder CLI on the user's PATH.
var qoderLocator = launch.Binaries("qodercli")

// qoderPermissionModes are the modes the wire emits in camelCase. The flag
// vocabulary is snake_case and is normalized before emission.
var qoderPermissionModes = []agent.OptionDef{
	{Id: contracts.QoderModeDefault, Name: "Default", Default: true, Description: "Prompt for dangerous operations."},
	{Id: contracts.QoderModeAcceptEdits, Name: "Accept Edits", Description: "Auto-accept file edit operations."},
	{Id: contracts.QoderModeAuto, Name: "Auto", Description: "Approve what a safety check finds safe, ask about the rest."},
	{Id: contracts.QoderModeDontAsk, Name: "Don't Ask", Description: "Do not prompt; deny what is not pre-approved."},
	{Id: contracts.QoderModePlan, Name: "Plan", Description: "Plan the work without modifying files."},
	{Id: contracts.QoderModeBypassPermissions, Name: "Bypass Permissions", Description: "Run every tool without a permission prompt."},
}

func qoderPermissionModeGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDPermissionMode, "Permissions", agent.OptionOrderPermissionMode, current, qoderPermissionModes)
}

// qoderEffortLevels are the reasoning-effort values Qoder takes, strongest
// first, with LeapMux's Auto sentinel ahead of them.
//
// Auto is not a strength: it sends Qoder's own `--reasoning-effort auto`,
// which states no level, so the CLI keeps whatever default the model resolves
// (see qoderReasoningArgs for why the flag is never omitted). The five levels
// below are words that Qoder's own effort validator admits. Qoder also admits
// `none` (and its aliases `disabled` and `off`), which turns thinking off. The
// Extended Thinking setting owns that choice, so the effort menu does not offer
// it: an effort of `none` with thinking On would be a state that Qoder does not
// run.
var qoderEffortLevels = []agent.OptionDef{
	{Id: agent.EffortAuto, Name: providerkit.EffortLabel(agent.EffortAuto), Description: "Let Qoder pick the effort for the model", Default: true},
	{Id: contracts.QoderEffortLevelMax, Name: providerkit.EffortLabel(contracts.QoderEffortLevelMax)},
	{Id: contracts.QoderEffortLevelXhigh, Name: providerkit.EffortLabel(contracts.QoderEffortLevelXhigh)},
	{Id: contracts.QoderEffortLevelHigh, Name: providerkit.EffortLabel(contracts.QoderEffortLevelHigh)},
	{Id: contracts.QoderEffortLevelMedium, Name: providerkit.EffortLabel(contracts.QoderEffortLevelMedium)},
	{Id: contracts.QoderEffortLevelLow, Name: providerkit.EffortLabel(contracts.QoderEffortLevelLow)},
}

func qoderEffortGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(agent.OptionIDEffort, "Effort", agent.OptionOrderEffort, current, qoderEffortLevels)
}

// OptionThinking is the Extended Thinking axis. Its values are LeapMux's own
// words, as Cline's axis uses them; qoderReasoningArgs maps them onto Qoder's
// `--thinking` and `--reasoning-effort` flags.
const (
	OptionThinking    = "thinking"
	qoderThinkingAuto = "auto"
	qoderThinkingOff  = "false"
	qoderThinkingOn   = "true"
)

var qoderThinkingModes = []agent.OptionDef{
	{Id: qoderThinkingAuto, Name: "Auto", Description: "Use Qoder's own thinking default and the selected effort.", Default: true},
	{Id: qoderThinkingOff, Name: "Off", Description: "Do not think. The selected effort applies again when thinking returns."},
	{Id: qoderThinkingOn, Name: "On", Description: "Think on models that support it, at the selected effort."},
}

// qoderThinkingValue reads a stored value. An empty or unknown value is Auto,
// which sends no `--thinking` flag.
func qoderThinkingValue(value string) string {
	for _, mode := range qoderThinkingModes {
		if mode.Id == value {
			return value
		}
	}
	return qoderThinkingAuto
}

func qoderThinkingGroup(current string) *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(OptionThinking, "Extended Thinking", agent.OptionOrderProviderFirst, qoderThinkingValue(current), qoderThinkingModes)
}

// OptionOutputStyle is the Output Style axis. Its values are Qoder's own style
// names, which `--output-style` takes, and LeapMux's Auto, which sends no flag
// so that the style of Qoder's own settings applies.
const (
	OptionOutputStyle    = "outputStyle"
	qoderOutputStyleAuto = "auto"
)

// qoderBuiltinOutputStyles are the styles that every Qoder session offers. A
// running session reports its own list, which adds the user's styles.
var qoderBuiltinOutputStyles = []string{qoderOutputStyleDefault, qoderOutputStyleExplanatory, qoderOutputStyleLearning}

// qoderOutputStyleGroup lists Auto and the given native styles, each once. An
// empty current value is Auto.
func qoderOutputStyleGroup(current string, styles []string) *leapmuxv1.AvailableOptionGroup {
	defs := []agent.OptionDef{{Id: qoderOutputStyleAuto, Name: "Auto", Description: "Use the output style of Qoder's own settings.", Default: true}}
	seen := map[string]bool{qoderOutputStyleAuto: true}
	for _, style := range styles {
		if strings.TrimSpace(style) == "" || seen[style] {
			continue
		}
		seen[style] = true
		name := style
		if style == qoderOutputStyleDefault {
			name = "Default"
		}
		defs = append(defs, agent.OptionDef{Id: style, Name: name})
	}
	if current == "" {
		current = qoderOutputStyleAuto
	}
	return agent.SelectGroup(OptionOutputStyle, "Output Style", agent.OptionOrderProviderSecond, current, defs)
}

// Registration states everything the worker knows about Qoder CLI before any of
// its agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_QODER,
		Plugin:   qoderProvider{},
		Start:    Start,
		Locator:  qoderLocator,
		// The account decides which models exist, so the catalog is read from the
		// open session rather than declared here.
		DefaultModels: nil,
		OptionGroups: []*leapmuxv1.AvailableOptionGroup{
			qoderEffortGroup(""),
			qoderThinkingGroup(""),
			qoderOutputStyleGroup("", qoderBuiltinOutputStyles),
			qoderPermissionModeGroup(""),
		},
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.QoderModeDefault},
			Fallback:   contracts.QoderModeDefault,
		},
		FixedPermissionModes: true,
		EnvModelKey:          "LEAPMUX_QODER_DEFAULT_MODEL",
		EnvEffortKey:         "LEAPMUX_QODER_DEFAULT_EFFORT",
	}
}
