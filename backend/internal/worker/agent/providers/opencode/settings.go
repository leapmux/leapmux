package opencode

import (
	"encoding/json"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// AutoApproveGroup is the family's Auto-approve option.
//
// OpenCode and Kilo implement their `--auto` mode in their own clients: the
// client answers each permission request once, and a request that a rule
// denies never reaches it. The daemon has no such mode to switch on, and its
// session rule update only appends rules, so it cannot switch one off again.
// LeapMux is the client here, so it answers the requests itself while the
// option is on.
var AutoApproveGroup = providerkit.ClientAutoApproveGroup()

// acpPermissionKindAllowAlways is the ACP kind of a permission option that
// allows the call always. Only the worker reads it, so it stays out of the
// shared ACP contract.
const acpPermissionKindAllowAlways = "allow_always"

// autoApproveFromOptions reads the launch value of the Auto-approve option.
func autoApproveFromOptions(options optionmap.Map) (bool, error) {
	return options.Bool(contracts.ClientApprovalOptionAutoApprove)
}

// localOptionGroups reports the Auto-approve option with its current value,
// and the groups of the member (MemberOptionGroups).
func (b *FamilyBase) localOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	b.Mu.Lock()
	autoApprove := b.autoApprove
	b.Mu.Unlock()
	groups := []*leapmuxv1.AvailableOptionGroup{providerkit.LiveGroup(AutoApproveGroup, strconv.FormatBool(autoApprove))}
	if b.MemberOptionGroups != nil {
		groups = append(groups, b.MemberOptionGroups()...)
	}
	return groups
}

// applyLocalOption applies a new Auto-approve value at once. The base then
// approves each request that already waits (Base.UpdateSettings).
func (b *FamilyBase) applyLocalOption(id, value string) (bool, error) {
	if id != contracts.ClientApprovalOptionAutoApprove {
		return false, nil
	}
	autoApprove, err := optionmap.Map{id: value}.Bool(id)
	if err != nil {
		return true, err
	}
	b.setAutoApprove(autoApprove)
	return true, nil
}

// setAutoApprove sets the value of the Auto-approve option.
func (b *FamilyBase) setAutoApprove(autoApprove bool) {
	b.Mu.Lock()
	b.autoApprove = autoApprove
	b.Mu.Unlock()
}

// UpdateSettings refuses an Auto-approve value that is neither true nor
// false before any other setting changes. A failed local write would ask the
// service for a restart, and the restart would refuse the same value.
func (b *FamilyBase) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	if _, err := autoApproveFromOptions(options); err != nil {
		return b.SettingsSnapshot()
	}
	return b.Base.UpdateSettings(options)
}

// withAutoApprove runs claim while Auto-approve is on. The claim runs under
// the settings lock, so a change of the option cannot pass between the check
// and the approval.
func (b *FamilyBase) withAutoApprove(claim func()) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if b.autoApprove && !b.StoppedLocked() {
		claim()
	}
}

// permissionOffersAlways reports whether Auto-approve may answer a permission
// request: the request must offer an option that allows the call always.
//
// Kilo offers no such option for a request that it reserves for a person: a
// batch of skill shell commands, or a command that leaves the sandbox. Kilo's
// own `--auto` does not approve those either. OpenCode offers the option on
// every request, so Auto-approve answers each of them.
func permissionOffersAlways(line *providerkit.ParsedLine) bool {
	var request struct {
		Options []struct {
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(line.Params, &request) != nil {
		return false
	}
	for _, option := range request.Options {
		if option.Kind == acpPermissionKindAllowAlways {
			return true
		}
	}
	return false
}
