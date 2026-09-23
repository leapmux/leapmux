package agent

import (
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/stretchr/testify/assert"
)

// TestOptions_Accessors locks the launch Options' by-id readers: model/effort/
// permission are NOT shadow scalar fields but accessors over the single option
// map, so a provider reading opts.Model()/Effort()/PermissionMode()/Get(id) sees
// exactly what the service resolved into Options -- and an empty map reads back as
// "" for every axis without panicking.
func TestOptions_Accessors(t *testing.T) {
	t.Parallel()

	o := Options{Options: map[string]string{
		OptionIDModel:          "opus[1m]",
		OptionIDEffort:         "xhigh",
		OptionIDPermissionMode: "plan",
		"sandbox_policy":       "workspace-write",
	}}
	assert.Equal(t, "opus[1m]", o.Model())
	assert.Equal(t, "xhigh", o.Effort())
	assert.Equal(t, "plan", o.PermissionMode())
	assert.Equal(t, "workspace-write", o.Get("sandbox_policy"), "Get reads any axis by id, not just the well-known ones")
	assert.Empty(t, o.Get("nonexistent"), "an absent id reads back empty")

	var empty Options
	assert.Empty(t, empty.Model())
	assert.Empty(t, empty.Effort())
	assert.Empty(t, empty.PermissionMode())
	assert.Empty(t, empty.Get(OptionIDModel), "a nil option map does not panic")
}

// A timeout that the caller did not set, or set to zero or less, takes the
// configured default. A positive one stays.
func TestOptions_EffectiveTimeouts(t *testing.T) {
	t.Parallel()

	for _, unset := range []time.Duration{0, -time.Second} {
		o := Options{StartupTimeout: unset, APITimeout: unset}
		assert.Equal(t, config.DefaultAgentStartupTimeout, o.EffectiveStartupTimeout(), "startup timeout %v", unset)
		assert.Equal(t, DefaultAPITimeout, o.EffectiveAPITimeout(), "API timeout %v", unset)
	}

	o := Options{StartupTimeout: 3 * time.Second, APITimeout: 2 * time.Second}
	assert.Equal(t, 3*time.Second, o.EffectiveStartupTimeout())
	assert.Equal(t, 2*time.Second, o.EffectiveAPITimeout())
}
