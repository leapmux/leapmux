package providerkit

import (
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ClientAutoApproveGroup describes LeapMux's once-only approval policy.
// Providers decide which native requests are eligible and preserve deny rules.
func ClientAutoApproveGroup() *leapmuxv1.AvailableOptionGroup {
	return agent.SelectGroup(contracts.ClientApprovalOptionAutoApprove, "Auto-approve", agent.OptionOrderPermissionMode, "false", []agent.OptionDef{
		{Id: "false", Name: "Off", Description: "Ask before each tool call that no rule allows", Default: true},
		{Id: "true", Name: "On", Description: "Approve each eligible permission request once. Explicit deny rules still apply."},
	})
}
