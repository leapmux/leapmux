package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
)

// TestQoderPermissionFlagCoversEveryMode pins the launch word of each offered
// mode. Qoder CLI 1.1.78 admits these words for --permission-mode: default,
// accept_edits, bypass_permissions, dont_ask, plan and auto. A mode without its
// word would restart the agent in Default.
func TestQoderPermissionFlagCoversEveryMode(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		contracts.QoderModeDefault:           "default",
		contracts.QoderModeAcceptEdits:       "accept_edits",
		contracts.QoderModeAuto:              "auto",
		contracts.QoderModeDontAsk:           "dont_ask",
		contracts.QoderModePlan:              "plan",
		contracts.QoderModeBypassPermissions: "bypass_permissions",
	}
	offered := make(map[string]string, len(qoderPermissionModes))
	for _, mode := range qoderPermissionModes {
		offered[mode.Id] = qoderPermissionFlag(mode.Id)
	}
	assert.Equal(t, want, offered, "each offered mode launches with its own native word")
	assert.Equal(t, "default", qoderPermissionFlag(""))
	assert.Equal(t, "default", qoderPermissionFlag("yolo"), "an unknown mode launches in Default")
}
