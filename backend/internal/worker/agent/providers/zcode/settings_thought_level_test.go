package zcode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

func TestResolveZCodeThoughtLevel(t *testing.T) {
	t.Parallel()

	tiers := func(ids ...string) []*agent.EffortInfo {
		out := make([]*agent.EffortInfo, 0, len(ids))
		for _, id := range ids {
			out = append(out, &agent.EffortInfo{Id: id})
		}
		return out
	}
	for name, tc := range map[string]struct {
		level, defaultLevel string
		offered             []*agent.EffortInfo
		want                string
	}{
		"an offered level wins":                             {level: "low", defaultLevel: "high", offered: tiers(agent.EffortAuto, "high", "low"), want: "low"},
		"a level that the model does not offer":             {level: "max", defaultLevel: "high", offered: tiers(agent.EffortAuto, "high", "low"), want: "high"},
		"a level that the model does not offer, no default": {level: "max", defaultLevel: "", offered: tiers("high"), want: ""},
		"auto takes the default":                            {level: agent.EffortAuto, defaultLevel: "high", offered: tiers("high"), want: "high"},
		"an empty level takes the default":                  {level: "", defaultLevel: "high", offered: tiers("high"), want: "high"},
		"the sentinel is no default":                        {level: agent.EffortAuto, defaultLevel: agent.EffortAuto, offered: tiers(agent.EffortAuto, "high"), want: ""},
		"an unknown level list refuses nothing":             {level: "max", defaultLevel: "high", offered: nil, want: "max"},
		"a list of the sentinel alone refuses nothing":      {level: "max", defaultLevel: "high", offered: tiers(agent.EffortAuto), want: "max"},
		"a nil entry is skipped":                            {level: "low", defaultLevel: "high", offered: []*agent.EffortInfo{nil, {Id: "low"}}, want: "low"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, resolveZCodeThoughtLevel(tc.level, tc.defaultLevel, tc.offered))
		})
	}
}
