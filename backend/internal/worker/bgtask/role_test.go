package bgtask

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleCapabilitiesAndValidation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		role                      Role
		valid, canonical, counted bool
	}{
		{RoleUnspecified, false, false, false},
		{RoleTaskOutcome, true, false, true},
		{RoleChildTask, true, true, true},
		{RoleChildActivity, true, true, false},
		{Role(-1), false, false, false},
		{Role(99), false, false, false},
		{Role(1<<31 - 1), false, false, false},
	} {
		assert.Equal(t, test.canonical, test.role.IsCanonicalChildActivity(), "role=%d", test.role)
		assert.Equal(t, test.counted, test.role.CountsAsTask(), "role=%d", test.role)
		if test.valid {
			assert.NoError(t, test.role.Validate())
		} else {
			assert.Error(t, test.role.Validate())
		}
	}
}

func TestRoleRequiresCanonicalChildLink(t *testing.T) {
	t.Parallel()
	require.NoError(t, (Item{Role: RoleTaskOutcome}).ValidateRole())
	for _, role := range []Role{RoleChildTask, RoleChildActivity} {
		assert.Error(t, (Item{Role: role}).ValidateRole())
		assert.NoError(t, (Item{Role: role, ChildAgentID: "child"}).ValidateRole())
	}
}

func TestRolePartialUpsertsPreserveCanonicalAuthority(t *testing.T) {
	t.Parallel()
	for _, role := range []Role{RoleChildTask, RoleChildActivity} {
		existing := Item{Role: role, RowKey: "child", ChildAgentID: "child-id", Kind: KindSubagent, Title: "Retained"}
		merged := (Upsert{RowKey: "child", Status: StatusRunning}).ToItem().PreservingBlanksFrom(existing)
		assert.Equal(t, role, merged.Role)
		assert.Equal(t, "child-id", merged.ChildAgentID)
		require.NoError(t, merged.ValidateRole())
	}
	ordinary := (Upsert{RowKey: "task", Status: StatusRunning}).ToItem().PreservingBlanksFrom(Item{})
	assert.Equal(t, RoleTaskOutcome, ordinary.Role)
	require.NoError(t, ordinary.ValidateRole())
}

func TestRoleFirstChildLinkRequiresExplicitPurpose(t *testing.T) {
	t.Parallel()
	for _, existing := range []Item{{}, {Role: RoleTaskOutcome, RowKey: "task", Kind: KindSubagent}} {
		linked := (Upsert{RowKey: "task", Kind: KindSubagent, ChildAgentID: "child", Status: StatusRunning}).ToItem().PreservingBlanksFrom(existing)
		assert.Error(t, linked.ValidateRole(), "a first child link cannot silently select outcome or activity authority")
	}
}
