package bgtask

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAliasPreservesOutcomeEvidence(t *testing.T) {
	t.Parallel()
	sourceEnd := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	targetEnd := sourceEnd.Add(time.Hour)
	for _, test := range []struct {
		name                 string
		source, target, want Status
		sourceWins           bool
	}{
		{"success replaces running", StatusSucceeded, StatusRunning, StatusSucceeded, true},
		{"failure replaces interrupted", StatusFailed, StatusInterrupted, StatusFailed, true},
		{"stop replaces unknown", StatusStopped, StatusEndedWithUnknownOutcome, StatusStopped, true},
		{"interrupted replaces unknown", StatusInterrupted, StatusEndedWithUnknownOutcome, StatusInterrupted, true},
		{"unknown replaces paused", StatusEndedWithUnknownOutcome, StatusPaused, StatusEndedWithUnknownOutcome, true},
		{"active replay preserves success", StatusRunning, StatusSucceeded, StatusSucceeded, false},
		{"unknown preserves native failure", StatusEndedWithUnknownOutcome, StatusFailed, StatusFailed, false},
		{"interrupted preserves native stop", StatusInterrupted, StatusStopped, StatusStopped, false},
		{"same success preserves target time", StatusSucceeded, StatusSucceeded, StatusSucceeded, false},
		{"same unknown preserves target time", StatusEndedWithUnknownOutcome, StatusEndedWithUnknownOutcome, StatusEndedWithUnknownOutcome, false},
		{"active target remains authoritative", StatusRunning, StatusPending, StatusPending, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := Item{Role: RoleTaskOutcome, RowKey: "invocation", ChildAgentID: "child", ParentAgentID: "parent", Status: test.source, ActiveForm: "Old activity"}
			target := Item{Role: RoleTaskOutcome, RowKey: "native-task", ChildAgentID: "child", ParentAgentID: "parent", Status: test.target, ActiveForm: "Current activity"}
			if source.Status.IsFinished() {
				source.EndedAt = sourceEnd
			}
			if target.Status.IsFinished() {
				target.EndedAt = targetEnd
			}
			resolved, err := source.ResolveAlias(target)
			require.NoError(t, err)
			assert.Equal(t, test.want, resolved.Status)
			assert.Equal(t, target.RowKey, resolved.RowKey)
			expectedEnd := target.EndedAt
			if test.sourceWins {
				expectedEnd = source.EndedAt
			}
			assert.Equal(t, expectedEnd, resolved.EndedAt)
			if test.want.IsFinished() {
				assert.Empty(t, resolved.ActiveForm)
			}
		})
	}
}

func TestResolveAliasRejectsEveryConflictingNativeOutcome(t *testing.T) {
	t.Parallel()
	for _, sourceStatus := range []Status{StatusSucceeded, StatusFailed, StatusStopped} {
		for _, targetStatus := range []Status{StatusSucceeded, StatusFailed, StatusStopped} {
			if sourceStatus == targetStatus {
				continue
			}
			t.Run(sourceStatus.String()+" to "+targetStatus.String(), func(t *testing.T) {
				t.Parallel()
				source := Item{Role: RoleTaskOutcome, RowKey: "source", Status: sourceStatus}
				target := Item{Role: RoleTaskOutcome, RowKey: "target", Status: targetStatus}
				_, err := source.ResolveAlias(target)
				require.ErrorIs(t, err, ErrConflictingOutcomes)
			})
		}
	}
}

func TestResolveAliasKeepsMetadataAndIdentityCoherent(t *testing.T) {
	t.Parallel()
	source := Item{Role: RoleTaskOutcome, RowKey: "source", Kind: KindWorkflow, ChildAgentID: "child", ParentAgentID: "parent", GroupKey: "old-group", GroupLabel: "Old group", Title: "printf source", TitleIsCommand: true, Description: "Source description", Status: StatusSucceeded}
	for _, test := range []struct {
		name         string
		target       Item
		group, label string
		refused      bool
	}{
		{name: "missing metadata", target: Item{Role: RoleTaskOutcome, RowKey: "target", Status: StatusRunning}, group: "old-group", label: "Old group"},
		{name: "same group missing label", target: Item{Role: RoleTaskOutcome, RowKey: "target", Status: StatusRunning, GroupKey: "old-group"}, group: "old-group", label: "Old group"},
		{name: "different group keeps its blank label", target: Item{Role: RoleTaskOutcome, RowKey: "target", Status: StatusRunning, GroupKey: "new-group"}, group: "new-group", label: ""},
		{name: "different child", target: Item{Role: RoleTaskOutcome, RowKey: "target", Status: StatusRunning, ChildAgentID: "another-child"}, refused: true},
		{name: "different parent", target: Item{Role: RoleTaskOutcome, RowKey: "target", Status: StatusRunning, ParentAgentID: "another-parent"}, refused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolved, err := source.ResolveAlias(test.target)
			if test.refused {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "target", resolved.RowKey)
			assert.Equal(t, RoleTaskOutcome, resolved.Role)
			assert.Equal(t, KindWorkflow, resolved.Kind)
			assert.Equal(t, "child", resolved.ChildAgentID)
			assert.Equal(t, "parent", resolved.ParentAgentID)
			assert.Equal(t, test.group, resolved.GroupKey)
			assert.Equal(t, test.label, resolved.GroupLabel)
			assert.Equal(t, source.Title, resolved.Title)
			assert.True(t, resolved.TitleIsCommand)
			assert.Equal(t, source.Description, resolved.Description)
		})
	}
}

func TestResolveAliasRespectsTaskRoleBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                   string
		sourceRole, targetRole Role
		sourceChild            string
		refused                bool
		wantRole               Role
	}{
		{"unlinked outcome into activity", RoleTaskOutcome, RoleChildActivity, "", true, RoleUnspecified},
		{"linked outcome into activity", RoleTaskOutcome, RoleChildActivity, "child", true, RoleUnspecified},
		{"combined task into activity", RoleChildTask, RoleChildActivity, "child", true, RoleUnspecified},
		{"activity into outcome", RoleChildActivity, RoleTaskOutcome, "child", true, RoleUnspecified},
		{"activity into combined task", RoleChildActivity, RoleChildTask, "child", true, RoleUnspecified},
		{"outcome into combined task", RoleTaskOutcome, RoleChildTask, "child", false, RoleChildTask},
		{"combined task into outcome", RoleChildTask, RoleTaskOutcome, "child", false, RoleChildTask},
		{"activity into activity", RoleChildActivity, RoleChildActivity, "child", false, RoleChildActivity},
	} {
		for _, status := range []Status{StatusRunning, StatusSucceeded} {
			t.Run(test.name+"/"+status.String(), func(t *testing.T) {
				t.Parallel()
				source := Item{Role: test.sourceRole, RowKey: "source", ChildAgentID: test.sourceChild, ParentAgentID: "parent", Kind: KindSubagent, Status: status}
				target := Item{Role: test.targetRole, RowKey: "target", ChildAgentID: "child", ParentAgentID: "parent", Kind: KindSubagent, Status: StatusRunning}
				resolved, err := source.ResolveAlias(target)
				if test.refused {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, test.wantRole, resolved.Role)
				assert.Equal(t, "target", resolved.RowKey)
				assert.Equal(t, "child", resolved.ChildAgentID)
				assert.Equal(t, StatusRunning, resolved.Status, "canonical destination activity stays authoritative")
			})
		}
	}
}

func TestResolveAliasRejectsUnknownRoles(t *testing.T) {
	t.Parallel()
	for _, role := range []Role{RoleUnspecified, Role(-1), Role(99), Role(1<<31 - 1)} {
		for _, sourceInvalid := range []bool{false, true} {
			t.Run(fmt.Sprintf("role %d source %t", role, sourceInvalid), func(t *testing.T) {
				t.Parallel()
				source := Item{Role: RoleChildActivity, RowKey: "source", ChildAgentID: "child"}
				target := Item{Role: RoleChildActivity, RowKey: "target", ChildAgentID: "child"}
				if sourceInvalid {
					source.Role = role
				} else {
					target.Role = role
				}
				_, err := source.ResolveAlias(target)
				require.Error(t, err, "an unknown role cannot authorize deletion during alias resolution")
			})
		}
	}
}

func TestResolveAliasRejectsDifferentCanonicalIdentities(t *testing.T) {
	t.Parallel()
	for _, roles := range [][2]Role{{RoleTaskOutcome, RoleTaskOutcome}, {RoleTaskOutcome, RoleChildTask}, {RoleChildTask, RoleTaskOutcome}, {RoleChildTask, RoleChildTask}, {RoleChildActivity, RoleChildActivity}} {
		for _, differentChild := range []bool{false, true} {
			t.Run(fmt.Sprintf("roles %d %d child %t", roles[0], roles[1], differentChild), func(t *testing.T) {
				t.Parallel()
				source := Item{Role: roles[0], RowKey: "source", ChildAgentID: "child", ParentAgentID: "parent", Status: StatusSucceeded}
				target := Item{Role: roles[1], RowKey: "target", ChildAgentID: "child", ParentAgentID: "parent", Status: StatusRunning}
				if differentChild {
					target.ChildAgentID = "another-child"
				} else {
					target.ParentAgentID = "another-parent"
				}
				_, err := source.ResolveAlias(target)
				require.Error(t, err, "canonical destinations cannot discard another child's or parent's fact")
			})
		}
	}
}
