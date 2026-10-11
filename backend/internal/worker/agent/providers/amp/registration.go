package amp

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// ampLocator finds the Amp CLI on the user's PATH.
var ampLocator = launch.Binaries("amp")

// AgentModeLabel is the label of Amp's agent-mode axis. Amp calls the setting a
// mode (`--mode`), and each mode chooses the model and the reasoning effort.
const AgentModeLabel = "Mode"

// PermissionModeLabel is the label of the permission-mode axis.
const PermissionModeLabel = "Permissions"

// agentModeGroup is the static option group of Amp's agent mode, and the one
// home of that template: Registration and Agent.OptionGroups both read it.
//
// The four modes are Amp's built-in Dial. Amp can also take a mode that a
// plugin defines, but only `amp plugins show-agent-options` lists them, which
// asks Amp's server and loads every plugin, so the group offers the four modes
// Amp always has. A thread keeps the mode of its first message, which is why the
// agent turns this group read-only once the thread received one.
var agentModeGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           contracts.AmpOptionAgentMode,
	Label:        AgentModeLabel,
	DefaultValue: agentModeMedium,
	Mutable:      true,
	Order:        agent.OptionOrderProviderFirst,
	Options: []*leapmuxv1.AvailableOption{
		{Id: agentModeLow, Name: "Low", Description: "Small, well-defined tasks, such as a focused fix or test"},
		{Id: agentModeMedium, Name: "Medium", Description: "Most work, including tasks where Amp fills in some steps"},
		{Id: agentModeHigh, Name: "High", Description: "Hard tasks where a subtle miss is expensive"},
		{Id: agentModeUltra, Name: "Ultra", Description: "Hard, open-ended work across many files or systems"},
	},
}

// permissionModeGroup is the static option group of the permission mode.
//
// Amp states no permission mode of its own for a stream-JSON session: its
// `delegate` rule asks the worker about each call, and the worker answers from
// this axis at the moment of the call. So a change applies to the next call at
// once, with no restart.
var permissionModeGroup = &leapmuxv1.AvailableOptionGroup{
	Id:           agent.OptionIDPermissionMode,
	Label:        PermissionModeLabel,
	DefaultValue: contracts.AmpPermissionModeAsk,
	Mutable:      true,
	Order:        agent.OptionOrderPermissionMode,
	Options: []*leapmuxv1.AvailableOption{
		{Id: contracts.AmpPermissionModeAsk, Name: "Ask", Description: "Ask before each tool that Amp runs on this machine, unless a rule in your own Amp settings decides it"},
		{Id: contracts.AmpPermissionModeAllowAll, Name: "Allow All", Description: "Run each tool on this machine without asking"},
	},
}

// staticOptionGroups holds the option groups that do not depend on a running
// agent. Amp reports no model catalog, so these two are its whole settings.
var staticOptionGroups = []*leapmuxv1.AvailableOptionGroup{agentModeGroup, permissionModeGroup}

// agentDirSpec states the private directory of each Amp agent. It holds three
// files:
//
//   - the bridge's socket;
//   - the helper spec, which holds the bridge secret;
//   - the generated settings file, which holds a copy of the user's settings.
//
// agentdir puts the directory under a base where the socket's path fits the
// platform's limit. The usual $TMPDIR of macOS leaves too little room for it,
// and the directory then goes to /tmp. When no base fits, the agent does not
// start, and the error states the limit.
//
// The spec states no hook: the directory records no process for the sweep to
// end, as a Cline directory does. The sweep removes the secret and the copy of
// the settings that an ended worker left.
func agentDirSpec() agentdir.Spec {
	return agentdir.Spec{Prefix: "amp", SocketName: bridgeSocketName}
}

// Registration states everything the worker knows about Amp before any of its
// agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP,
		Plugin:   ampProvider{},
		Start:    Start,
		Locator:  ampLocator,
		// No catalog. Amp picks the model from the mode, on its server, and its
		// stream states no model. The `--model` flag is hidden and needs an account
		// feature, so LeapMux offers none.
		DefaultModels: nil,
		OptionGroups:  staticOptionGroups,
		// A new session asks before each call. A session that stored no mode takes
		// the same answer, so no resume ever opens with the checks turned off.
		PermissionDefaults: agent.PermissionDefaults{
			NewSession: map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAsk},
			Fallback:   contracts.AmpPermissionModeAsk,
		},
		// LeapMux states the two modes completely, so the worker refuses an
		// unknown value at launch and does not read it as one of them.
		FixedPermissionModes: true,
		// Amp has neither a model axis nor an effort axis, so it honors no
		// LEAPMUX_AMP_DEFAULT_MODEL or LEAPMUX_AMP_DEFAULT_EFFORT.
		EnvModelKey:  "",
		EnvEffortKey: "",
		Helpers: map[string]agent.HelperFunc{
			helperPermission: runPermissionHelper,
		},
		AgentDir: ptrconv.Ptr(agentDirSpec()),
	}
}
