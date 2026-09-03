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
const (
	CopilotConfigReasoningEffort = "reasoning_effort"
	CopilotConfigAllowAll        = contracts.CopilotPermissionGroupAllowAll
)

// CopilotCLIAgent manages a single Copilot CLI ACP process.
type CopilotCLIAgent struct {
	acpBase
	assistedApproval            string
	assistedApprovalUnavailable bool
}

const copilotAssistedApprovalUnavailableText = "--assisted-approval is only supported in non-interactive prompt mode"

func copilotBaseArgs(opts Options) []string {
	args := []string{"--acp", "--stdio"}
	if opts.Get(contracts.CopilotPermissionGroupAssistedApproval) == contracts.CopilotPermissionValueOn {
		args = append(args, "--experimental", "--assisted-approval")
	}
	return args
}

// StartCopilotCLI starts a Copilot CLI ACP agent process and performs the handshake.
func StartCopilotCLI(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	started, err := startCopilotCLI(ctx, opts, sink, false)
	if err == nil || !opts.NewSessionDefaultOptionIDs[contracts.CopilotPermissionGroupAssistedApproval] ||
		!isCopilotAssistedApprovalUnavailable(err) {
		return started, err
	}

	slog.Warn("Copilot Assisted Approval is unavailable for ACP; using Off for the new session",
		"agent_id", opts.AgentID)
	fallback := opts
	fallback.Options = opts.Options.Clone()
	fallback.Options[contracts.CopilotPermissionGroupAssistedApproval] = contracts.CopilotPermissionValueOff
	return startCopilotCLI(ctx, fallback, sink, true)
}

func startCopilotCLI(ctx context.Context, opts Options, sink OutputSink, assistedApprovalUnavailable bool) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[CopilotCLIAgent]{
		provider:     leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		providerName: "copilot",
		binaryName:   "copilot",
		baseArgsFor:  copilotBaseArgs,
		newAgent: func() *CopilotCLIAgent {
			return &CopilotCLIAgent{
				assistedApprovalUnavailable: assistedApprovalUnavailable,
				assistedApproval: StringOrDefault(
					opts.Get(contracts.CopilotPermissionGroupAssistedApproval),
					contracts.CopilotPermissionValueOff,
				),
			}
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

func isCopilotAssistedApprovalUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), copilotAssistedApprovalUnavailableText)
}

func (a *CopilotCLIAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	groups := a.acpBase.OptionGroups()
	assistedID := contracts.CopilotPermissionGroupAssistedApproval
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
			template.Mutable = false
		}
		filtered = append(filtered, liveGroup(template, a.assistedApprovalValue()))
	}
	return filtered
}

func (a *CopilotCLIAgent) assistedApprovalValue() string {
	return StringOrDefault(a.assistedApproval, contracts.CopilotPermissionValueOff)
}

func (a *CopilotCLIAgent) addAssistedApproval(result SettingsApplyResult) SettingsApplyResult {
	if result.SurfacedOptions == nil {
		result.SurfacedOptions = optionmap.Map{}
	}
	if result.Settlements == nil {
		result.Settlements = OptionSettlements{}
	}
	value := a.assistedApprovalValue()
	result.SurfacedOptions[contracts.CopilotPermissionGroupAssistedApproval] = value
	result.Settlements[contracts.CopilotPermissionGroupAssistedApproval] = OptionSettlement{
		State: OptionSettlementConfirmed,
		Value: &value,
	}
	return result
}

func (a *CopilotCLIAgent) SettingsSnapshot() SettingsApplyResult {
	return a.addAssistedApproval(a.acpBase.SettingsSnapshot())
}

func (a *CopilotCLIAgent) UpdateSettings(options optionmap.Map) SettingsApplyResult {
	if requested := options[contracts.CopilotPermissionGroupAssistedApproval]; requested != "" && requested != a.assistedApprovalValue() {
		return restartRequiredSettings(options)
	}
	return a.addAssistedApproval(a.acpBase.UpdateSettings(options))
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
		CopilotConfigReasoningEffort, CopilotConfigAllowAll,
	)
	addStaticOptionGroups(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		selectGroup(
			contracts.CopilotPermissionGroupAssistedApproval,
			"Assisted Approval",
			OptionOrderProviderFourth,
			contracts.CopilotPermissionValueOff,
			[]optDef{
				{Id: contracts.CopilotPermissionValueOff, Name: "Off", Default: true},
				{Id: contracts.CopilotPermissionValueOn, Name: "On", Description: "Approve safe tool calls and ask about other calls. This also enables all experimental Copilot features."},
			},
		),
	)
	setNewAgentOptionDefaults(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, map[string]string{
		contracts.CopilotPermissionGroupAssistedApproval: contracts.CopilotPermissionValueOn,
		contracts.CopilotPermissionGroupAllowAll:         contracts.CopilotPermissionValueOff,
	})
}
