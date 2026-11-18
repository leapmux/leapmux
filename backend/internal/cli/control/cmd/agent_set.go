package cmd

import (
	"fmt"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// agentSetResult is the result of `agent set`.
type agentSetResult struct {
	ID string `json:"id"`
	// Applied maps each axis that took effect to the value the agent
	// confirmed.
	Applied map[string]string `json:"applied"`
	// NotApplied lists the axes that the provider removed instead of
	// setting.
	NotApplied []string `json:"not_applied,omitempty"`
	// Unresolved lists the axes that the agent did not confirm.
	Unresolved []string `json:"unresolved,omitempty"`
}

// runAgentSet updates --model / --effort / --permission-mode /
// --option key=value (repeatable).
func runAgentSet(inv *cli.Invocation) (any, error) {
	options, err := inv.Command.Flags().GetStringArray("option")
	if err != nil {
		return nil, err
	}
	opts, err := buildAgentSetOptions(stringFlag(inv, "model"), stringFlag(inv, "effort"), stringFlag(inv, "permission-mode"), options)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	if len(opts) == 0 {
		return nil, cli.Usagef("nothing to set").
			WithHint("pass --model, --effort, --permission-mode or --option")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	settings := &leapmuxv1.AgentSettings{Options: opts}
	resp := &leapmuxv1.UpdateAgentSettingsResponse{}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceUpdateAgentSettingsProcedure, &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  got.ID,
		Settings: settings,
	}, resp); err != nil {
		return nil, err
	}
	applied, notApplied, unresolved := appliedFromSettlements(settings.GetOptions(), resp.GetOptionSettlements())
	return agentSetResult{ID: got.ID, Applied: applied, NotApplied: notApplied, Unresolved: unresolved}, nil
}

// appliedFromSettlements classifies each requested axis. A confirmed
// settlement without a value means that the provider removed the axis.
func appliedFromSettlements(
	requested map[string]string,
	settlements map[string]*leapmuxv1.AgentOptionSettlement,
) (applied map[string]string, notApplied, unresolved []string) {
	applied = make(map[string]string, len(requested))
	for k := range requested {
		settlement := settlements[k]
		if settlement == nil || settlement.GetState() != leapmuxv1.AgentOptionSettlementState_AGENT_OPTION_SETTLEMENT_STATE_CONFIRMED {
			unresolved = append(unresolved, k)
			continue
		}
		if settlement.Value == nil || settlement.GetValue() == "" {
			notApplied = append(notApplied, k)
			continue
		}
		applied[k] = settlement.GetValue()
	}
	sort.Strings(notApplied)
	sort.Strings(unresolved)
	return applied, notApplied, unresolved
}

// buildAgentSetOptions merges the dedicated --model / --effort / --permission-mode flags and
// the repeatable --option key=value into one option map keyed by option-group id. It rejects any
// key set more than once -- via a dedicated flag and an --option, or two --options -- so a
// contradictory pair like `--effort high --option effort=low` (or a repeated `--option k=`)
// can't silently resolve to whichever assignment the loop happens to apply last.
func buildAgentSetOptions(model, effort, permissionMode string, options []string) (map[string]string, error) {
	// Seed the three dedicated flags through the same builder the spawn path uses (spawnOptions),
	// so the "build an option map from --model/--effort/--permission-mode, omitting empties" rule
	// lives in one place. The dup check below then also catches an --option that collides with a
	// dedicated flag, since both land in this one map.
	out := spawnOptions(model, effort, permissionMode)
	for _, kv := range options {
		k, v, err := splitKV(kv)
		if err != nil {
			return nil, err
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("option %q set more than once (via a dedicated flag and/or a repeated --option); set it exactly once", k)
		}
		out[k] = v
	}
	return out, nil
}

func splitKV(s string) (string, string, error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	// Reject an empty key (`--option =value`) and an empty value (`--option key=`) at parse
	// time. Without this the empty key persists no axis (the worker drops an unknown id) and the
	// empty value is a silent no-op (the worker skips an empty value rather than clearing) -- both
	// would report "applied" with the assignment silently vanished. Failing fast is clearer, and
	// clearing an option is not supported via the CLI.
	if k == "" {
		return "", "", fmt.Errorf("empty option key in %q", s)
	}
	if v == "" {
		return "", "", fmt.Errorf("empty option value for key %q (a value is required; clearing is not supported)", k)
	}
	return k, v, nil
}
