package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

const (
	CopilotCLIModeAgent     = "https://agentclientprotocol.com/protocol/session-modes#agent"
	CopilotCLIModePlan      = "https://agentclientprotocol.com/protocol/session-modes#plan"
	CopilotCLIModeAutopilot = "https://agentclientprotocol.com/protocol/session-modes#autopilot"
)

// Copilot's server-driven ACP config-option ids (surfaced as mutable option groups,
// not static templates). Declared in KnownOptionIDs so a not-running agent validates
// them, matching the ids the live `session/set_config_option` channel reports.
const CopilotConfigReasoningEffort = "reasoning_effort"

// CopilotCLIAgent manages a single Copilot CLI ACP process.
type CopilotCLIAgent struct {
	acpBase
	assistedApproval            string
	assistedApprovalUnavailable bool
}

const (
	copilotAssistedApprovalFlag = "--assisted-approval"
	// The Copilot CLI's own refusal text. Matching it is the only signal the CLI gives;
	// the match feeds a per-binary capability record, so a reworded message costs one
	// failed launch per worker process rather than a permanently broken tab.
	copilotAssistedApprovalUnavailableText = copilotAssistedApprovalFlag + " is only supported in non-interactive prompt mode"
)

func copilotBaseArgs(opts Options) []string {
	args := []string{"--acp", "--stdio"}
	if opts.Get(contracts.CopilotPermissionGroupAssistedApproval) == contracts.CopilotPermissionValueOn {
		args = append(args, "--experimental", copilotAssistedApprovalFlag)
	}
	return args
}

// copilotBinaryName is the CLI this provider launches, and the key the
// assisted-approval capability is remembered under.
const copilotBinaryName = "copilot"

// newCopilotCLIAgent builds the agent AND wires its catalog decorator. Every
// construction goes through here, in production and in tests, because the decorator is
// what puts Assisted Approval into the base catalog: an agent built without it reports a
// catalog with no such group and a snapshot that never settles one, and nothing else
// would say so. Wiring it in the one constructor rather than at each call site is what
// makes that mistake impossible rather than merely documented.
func newCopilotCLIAgent(assistedApproval string, assistedApprovalUnavailable bool) *CopilotCLIAgent {
	a := &CopilotCLIAgent{
		assistedApproval:            StringOrDefault(assistedApproval, contracts.CopilotPermissionValueOff),
		assistedApprovalUnavailable: assistedApprovalUnavailable,
	}
	a.decorateOptionGroups = a.copilotOptionGroups
	return a
}

// StartCopilotCLI starts a Copilot CLI ACP agent process and performs the handshake.
//
// Some Copilot builds reject --assisted-approval in Agent Client Protocol mode. That is a
// property of the INSTALLED CLI, so the first launch that hits it records the capability
// for the whole worker process: every later launch of that binary -- a second tab, a
// restart, a clear-context relaunch -- starts with the flag off on the FIRST attempt
// instead of paying a failed spawn and a full second handshake each time.
//
// The downgrade applies only to a value LeapMux chose. An explicitly requested Assisted
// Approval still surfaces the startup error, so a user who asks for it learns that this
// CLI cannot do it rather than silently running without it.
func StartCopilotCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	assisted := contracts.CopilotPermissionGroupAssistedApproval
	defaulted := opts.NewSessionDefaultOptionIDs[assisted]
	wantsAssisted := opts.Get(assisted) == contracts.CopilotPermissionValueOn
	unsupported := BinaryFlagUnsupported(opts.Shell, opts.LoginShell, copilotBinaryName, copilotAssistedApprovalFlag)

	if wantsAssisted && defaulted && unsupported {
		return startCopilotCLI(ctx, copilotWithoutAssistedApproval(opts), sink, true)
	}

	started, err := startCopilotCLI(ctx, opts, sink, wantsAssisted && unsupported)
	if err == nil || !isCopilotAssistedApprovalUnavailable(err) {
		return started, err
	}
	MarkBinaryFlagUnsupported(opts.Shell, opts.LoginShell, copilotBinaryName, copilotAssistedApprovalFlag)
	if !defaulted {
		return started, err
	}

	slog.Warn("Copilot Assisted Approval is unavailable for ACP; using Off for this session",
		"agent_id", opts.AgentID)
	return startCopilotCLI(ctx, copilotWithoutAssistedApproval(opts), sink, true)
}

// copilotWithoutAssistedApproval returns opts with Assisted Approval turned off, without
// touching the caller's map.
func copilotWithoutAssistedApproval(opts Options) Options {
	out := opts
	out.Options = opts.Options.Clone()
	out.Options[contracts.CopilotPermissionGroupAssistedApproval] = contracts.CopilotPermissionValueOff
	return out
}

func startCopilotCLI(ctx context.Context, opts Options, sink OutputSink, assistedApprovalUnavailable bool) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[CopilotCLIAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		providerName: "copilot",
		binaryName:   copilotBinaryName,
		baseArgs:     copilotBaseArgs(opts),
		newAgent: func() *CopilotCLIAgent {
			return newCopilotCLIAgent(
				opts.Get(contracts.CopilotPermissionGroupAssistedApproval), assistedApprovalUnavailable)
		},
		base: func(a *CopilotCLIAgent) *acpBase { return &a.acpBase },
		configure: func(a *CopilotCLIAgent) {
			a.modeChannel = modeChannelPermissionMode
			// Copilot's reasoning-effort axis is the convention id "reasoning_effort", not the
			// well-known "effort" -- declare it so the env-effort override maps onto it.
			a.effortConfigID = CopilotConfigReasoningEffort
		},
		afterHandshake: func(a *CopilotCLIAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPermissionModeStartup(handshake, opts, CopilotCLIModeAgent, opts.Model())
		},
	})
}

// resolveCopilotOptionConflicts settles Copilot's two permission axes. It is wired at
// registration in provider.go.
//
// The rule runs in ONE direction: turning Assisted Approval on turns Allow All off.
// Assisted Approval narrows what runs without asking, so leaving the broader Allow All
// beside it would make the narrowing meaningless.
//
// The reverse does NOT hold. Allow All is a runtime config option the ACP server owns,
// while Assisted Approval rides the launch flags -- so clearing it takes a process
// restart, and a restart in the middle of a turn is a far worse answer than letting the
// broader permission simply win. Allow All already supersedes Assisted Approval while
// both are on, so the combination is redundant rather than contradictory.
//
// It also settles only a conflict the REQUEST creates. When neither axis is in the
// request, a stored combination came from somewhere this resolver never saw -- a settings
// refresh folds the server's own Allow All in without passing through here -- and
// rewriting it would turn a permission axis off during an unrelated edit, such as a model
// change, which the user never asked for.
func resolveCopilotOptionConflicts(current, requested optionmap.Map) optionmap.Map {
	resolved := current.Merge(requested)
	assisted := contracts.CopilotPermissionGroupAssistedApproval
	allowAll := contracts.CopilotPermissionGroupAllowAll
	on := contracts.CopilotPermissionValueOn
	if resolved[assisted] != on || resolved[allowAll] != on {
		return resolved
	}
	if requested[assisted] == on {
		resolved[allowAll] = contracts.CopilotPermissionValueOff
	}
	return resolved
}

func isCopilotAssistedApprovalUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), copilotAssistedApprovalUnavailableText)
}

// copilotOptionGroups is the acpBase.decorateOptionGroups hook: it appends the Assisted
// Approval group, which rides the launch flags and so reaches the base through no ACP
// channel. Wired in configure, NOT written as an OptionGroups override -- an override
// would leave acpBase.SettingsSnapshot building its settlements from the undecorated
// catalog, so the UI would show a group nothing ever settled.
func (a *CopilotCLIAgent) copilotOptionGroups(groups []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
	assistedID := contracts.CopilotPermissionGroupAssistedApproval
	// Assisted Approval is a LeapMux-owned static group backed by launch flags, so the
	// base never emits it today. The drop is a standing guard, not dead code: the base
	// surfaces any non-reserved config option the server reports under its own id, so a
	// Copilot build that ever reported this id would yield TWO groups under one key, and
	// the frontend keys its option submenus by group id.
	filtered := make([]*leapmuxv1.AvailableOptionGroup, 0, len(groups)+1)
	for _, group := range groups {
		if group.GetId() != assistedID {
			filtered = append(filtered, group)
		}
	}
	template := optionids.GroupByID(
		AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT),
		assistedID,
	)
	if template != nil {
		if a.assistedApprovalUnavailable {
			template = filterGroupOptions(template, func(option *leapmuxv1.AvailableOption) bool {
				return option.GetId() == contracts.CopilotPermissionValueOff
			})
			template.DefaultValue = contracts.CopilotPermissionValueOff
			template.Mutable = false
		}
		filtered = append(filtered, liveGroup(template, a.assistedApprovalValue()))
	}
	return filtered
}

func (a *CopilotCLIAgent) assistedApprovalValue() string {
	return StringOrDefault(a.assistedApproval, contracts.CopilotPermissionValueOff)
}

// UpdateSettings refuses an Assisted Approval change in place: the axis is a launch flag,
// so only a relaunch can move it. Every other axis, and the settlement for this one, come
// from the base, which reads the decorated catalog.
func (a *CopilotCLIAgent) UpdateSettings(options optionmap.Map) SettingsApplyResult {
	if requested := options[contracts.CopilotPermissionGroupAssistedApproval]; requested != "" && requested != a.assistedApprovalValue() {
		return restartRequiredSettings(options)
	}
	return a.acpBase.UpdateSettings(options)
}

func fallbackCopilotCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent"},
		{Id: CopilotCLIModePlan, Name: "Plan"},
		{Id: CopilotCLIModeAutopilot, Name: "Autopilot"},
	}
}

func init() {
	// model + permissionMode (static group) + Copilot's server-driven config options. Copilot
	// has no well-known "effort" axis -- its reasoning axis is the config option
	// "reasoning_effort" -- so `--effort` against Copilot is correctly treated as foreign.
	registerPermissionModeConfigProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		StartCopilotCLI,
		fallbackCopilotCLIModes(),
		"LEAPMUX_COPILOT_DEFAULT_MODEL", "copilot",
		CopilotConfigReasoningEffort, contracts.CopilotPermissionGroupAllowAll,
	)
	addStaticOptionGroups(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		selectGroup(
			contracts.CopilotPermissionGroupAssistedApproval,
			"Assisted Approval",
			OptionOrderProviderFourth,
			contracts.CopilotPermissionValueOff,
			// Off is the DEFAULT: the value a session runs at when nothing sets this axis,
			// which is what an empty current resolves to and what a first settlement is
			// measured against. That a NEW session asks for On is a different fact, and
			// setNewAgentOptionDefaults below is its one home -- a resumed session never
			// receives it, and stamping it here would announce a spurious
			// "Assisted Approval (Off)" the moment such a session settled the axis.
			[]optDef{
				{Id: contracts.CopilotPermissionValueOff, Name: "Off", Default: true},
				{
					Id: contracts.CopilotPermissionValueOn, Name: "On",
					Description: "Approve safe tool calls and ask about other calls. This also enables all experimental Copilot features.",
					// Assisted Approval narrows what runs without a prompt, so the broader
					// Allow All cannot stand beside it. Declaring the consequence lets the
					// picker say so before the click; resolveCopilotOptionConflicts is what
					// actually applies it, and the two must state the same rule.
					Clears: []*leapmuxv1.OptionSideEffect{{
						GroupId: contracts.CopilotPermissionGroupAllowAll,
						Value:   contracts.CopilotPermissionValueOff,
					}},
				},
			},
		),
	)
	setPermissionDefaults(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, PermissionDefaults{
		NewSession: map[string]string{
			contracts.CopilotPermissionGroupAssistedApproval: contracts.CopilotPermissionValueOn,
			contracts.CopilotPermissionGroupAllowAll:         contracts.CopilotPermissionValueOff,
		},
		// Copilot's permission-mode axis is separate from the two axes above, and Agent
		// is the mode its session starts in.
		Fallback: CopilotCLIModeAgent,
	})
}
