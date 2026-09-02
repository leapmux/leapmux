package cmd

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// RunAgentSet updates --model / --effort / --permission-mode /
// --option key=value (repeatable).
func RunAgentSet(rawCtx any, args []string) error {
	var model, effort, permissionMode string
	optionSettings := stringSliceFlag{}
	settings := &leapmuxv1.AgentSettings{Options: map[string]string{}}
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&model, "model", "", "model id (empty = no change)")
			fs.StringVar(&effort, "effort", "", "effort id (empty = no change)")
			fs.StringVar(&permissionMode, "permission-mode", "", "permission mode (empty = no change)")
			fs.Var(&optionSettings, "option", "provider option in key=value form (repeatable)")
		},
		validate: func() error {
			opts, err := buildAgentSetOptions(model, effort, permissionMode, optionSettings.values)
			if err != nil {
				return control.EmitErrorWith("invalid_request", err)
			}
			settings.Options = opts
			return nil
		},
		body: func(ctx context.Context, c *control.Client, workerID, agentID string) error {
			resp := &leapmuxv1.UpdateAgentSettingsResponse{}
			if err := callInnerRPC(ctx, c, workerID, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
				AgentId:  agentID,
				Settings: settings,
			}, resp); err != nil {
				return err
			}
			applied, notApplied, unresolved := appliedFromSettlements(settings.GetOptions(), resp.GetOptionSettlements())
			data := map[string]any{"agent_id": agentID, "applied": applied}
			// Only surface not_applied when something didn't take, so the common all-applied case
			// stays uncluttered.
			if len(notApplied) > 0 {
				data["not_applied"] = notApplied
			}
			if len(unresolved) > 0 {
				data["unresolved"] = unresolved
			}
			return control.EmitData(data)
		},
	})
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

// stringSliceFlag implements flag.Value for repeatable string flags.
type stringSliceFlag struct {
	values []string
}

func (s *stringSliceFlag) String() string { return fmt.Sprintf("%v", s.values) }
func (s *stringSliceFlag) Set(v string) error {
	s.values = append(s.values, v)
	return nil
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
