package bgtask

import (
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Role separates task outcomes from canonical child activity.
type Role leapmuxv1.BackgroundTaskRole

const (
	RoleUnspecified   Role = Role(leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_UNSPECIFIED)
	RoleTaskOutcome   Role = Role(leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_TASK_OUTCOME)
	RoleChildTask     Role = Role(leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_TASK)
	RoleChildActivity Role = Role(leapmuxv1.BackgroundTaskRole_BACKGROUND_TASK_ROLE_CHILD_ACTIVITY)
)

// IsCanonicalChildActivity reports whether this row controls a child's activity and routing.
func (role Role) IsCanonicalChildActivity() bool {
	info, _ := contracts.BackgroundTaskRoleCapabilitiesFor(leapmuxv1.BackgroundTaskRole(role))
	return info.CanonicalChildActivity
}

// CountsAsTask excludes activity markers from task counters.
func (role Role) CountsAsTask() bool {
	info, _ := contracts.BackgroundTaskRoleCapabilitiesFor(leapmuxv1.BackgroundTaskRole(role))
	return info.CountsAsTask
}

// Validate rejects absent and unsupported roles.
func (role Role) Validate() error {
	if _, valid := contracts.BackgroundTaskRoleCapabilitiesFor(leapmuxv1.BackgroundTaskRole(role)); valid {
		return nil
	}
	return fmt.Errorf("invalid background task role %d", role)
}

// ValidateRole checks the persisted row's role and child-link invariant.
func (item Item) ValidateRole() error {
	if err := item.Role.Validate(); err != nil {
		return err
	}
	if item.Role.IsCanonicalChildActivity() && item.ChildAgentID == "" {
		return fmt.Errorf("a child activity row requires a child agent ID")
	}
	return nil
}
