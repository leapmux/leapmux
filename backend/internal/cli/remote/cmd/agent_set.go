package cmd

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
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
				return remote.EmitErrorWith("invalid_request", err)
			}
			settings.Options = opts
			return nil
		},
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, _ string) error {
			resp := &leapmuxv1.UpdateAgentSettingsResponse{}
			if err := callInnerRPC(ctx, c, workerID, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
				AgentId:  agentID,
				Settings: settings,
			}, resp); err != nil {
				return err
			}
			applied, notApplied := appliedFromConfirmed(settings.GetOptions(), resp.GetConfirmedOptions())
			data := map[string]any{"agent_id": agentID, "applied": applied}
			// Only surface not_applied when something didn't take, so the common all-applied case
			// stays uncluttered.
			if len(notApplied) > 0 {
				data["not_applied"] = notApplied
			}
			return remote.EmitData(data)
		},
	})
}

// appliedFromConfirmed splits the axes the user requested into those the worker SETTLED to a
// concrete value (`applied`, id->settled value) and those that did NOT take (`notApplied`, sorted
// ids). confirmed is the worker's FULL option map (every inherited axis -- model, permission mode,
// provider defaults), so echoing it whole would read as if `agent set --model X` had changed them
// all; restricting to the requested keys reports only what the command touched, and the settled
// value reflects the worker's strips/clamps.
//
// A requested axis ABSENT from confirmed did not take -- the provider rejected it (e.g. an effort
// baked into the model id), OR accepted it then dropped it once it no longer applied (an ACP
// option a model switch removed). The worker's reply can't tell these two apart, and to the user
// both mean the same thing: the axis is not in effect. Reporting them under `notApplied` -- rather
// than as `applied` entries carrying a cryptic "" -- says that plainly instead of reading like the
// axis was set to an empty value.
func appliedFromConfirmed(requested, confirmed map[string]string) (applied map[string]string, notApplied []string) {
	applied = make(map[string]string, len(requested))
	for k := range requested {
		if v := confirmed[k]; v != "" {
			applied[k] = v
		} else {
			notApplied = append(notApplied, k)
		}
	}
	sort.Strings(notApplied)
	return applied, notApplied
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
