package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
)

// TestModelInfoEqual_HiddenFlagRegisters guards that a Hidden-only difference is
// treated as a genuine catalog change, so the "idempotent re-report vs real
// change" check can't silently miss a picker-visibility flip.
func TestModelInfoEqual_HiddenFlagRegisters(t *testing.T) {
	t.Parallel()

	base := &ModelInfo{Id: "m", DisplayName: "M", DefaultEffort: "high"}
	hidden := &ModelInfo{Id: "m", DisplayName: "M", DefaultEffort: "high", Hidden: true}

	assert.True(t, base.equal(&ModelInfo{Id: "m", DisplayName: "M", DefaultEffort: "high"}),
		"identical entries are equal")
	assert.False(t, base.equal(hidden), "a Hidden-flag difference must register as a change")
	assert.False(t, hidden.equal(base), "equality is symmetric for the Hidden flag")
}

// OptionGroupSetEqualExact compares two catalogs as sets of groups, and the
// options of each group as a set. Every other field of a group counts.
func TestOptionGroupSetEqualExact(t *testing.T) {
	t.Parallel()

	group := func(id, current string, options ...string) *leapmuxv1.AvailableOptionGroup {
		g := &leapmuxv1.AvailableOptionGroup{Id: id, CurrentValue: current}
		for _, o := range options {
			g.Options = append(g.Options, &leapmuxv1.AvailableOption{Id: o})
		}
		return g
	}
	catalog := func() []*leapmuxv1.AvailableOptionGroup {
		return []*leapmuxv1.AvailableOptionGroup{group("mode", "ask", "ask", "auto"), group("tools", "on", "on", "off")}
	}
	changed := func(mutate func([]*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
		return mutate(catalog())
	}

	assert.True(t, OptionGroupSetEqualExact(catalog(), catalog()))
	assert.True(t, OptionGroupSetEqualExact(catalog(), []*leapmuxv1.AvailableOptionGroup{
		group("tools", "on", "off", "on"), group("mode", "ask", "auto", "ask"),
	}), "a new order of the groups or of the options is not a change")
	assert.True(t, OptionGroupSetEqualExact(nil, []*leapmuxv1.AvailableOptionGroup{}), "nil and empty are one empty catalog")

	for name, other := range map[string][]*leapmuxv1.AvailableOptionGroup{
		"one group fewer":                      catalog()[:1],
		"another group id with the same count": {group("mode", "ask", "ask", "auto"), group("other", "on", "on", "off")},
		"another current value": changed(func(c []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
			c[0].CurrentValue = "auto"
			return c
		}),
		"another option set": changed(func(c []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
			c[0].Options = c[0].Options[:1]
			return c
		}),
		"another label": changed(func(c []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
			c[1].Label = "Tools"
			return c
		}),
		"another mutability": changed(func(c []*leapmuxv1.AvailableOptionGroup) []*leapmuxv1.AvailableOptionGroup {
			c[1].Mutable = true
			return c
		}),
	} {
		assert.Falsef(t, OptionGroupSetEqualExact(catalog(), other), "%s is a change", name)
	}

	// The comparison sorts a clone. The caller's groups are shared snapshots.
	unsorted := group("tools", "on", "on", "off")
	OptionGroupSetEqualExact([]*leapmuxv1.AvailableOptionGroup{unsorted}, []*leapmuxv1.AvailableOptionGroup{group("tools", "on", "off", "on")})
	assert.Equal(t, "on", unsorted.GetOptions()[0].GetId(), "the caller's option list keeps its order")
}
