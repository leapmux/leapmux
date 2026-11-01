package service

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Builders for the GitSetup oneof that OpenAgentRequest and
// OpenTerminalRequest carry. An empty base branch means "the current branch".

func createWorktreeSetup(branch, baseBranch string) *leapmuxv1.GitSetup {
	return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CreateWorktree{
		CreateWorktree: &leapmuxv1.GitCreateWorktree{Branch: branch, BaseBranch: baseBranch},
	}}
}

func useWorktreePathSetup(path string) *leapmuxv1.GitSetup {
	return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_UseWorktreePath{UseWorktreePath: path}}
}

func checkoutBranchSetup(branch string) *leapmuxv1.GitSetup {
	return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CheckoutBranch{CheckoutBranch: branch}}
}

func createBranchSetup(name, baseBranch string) *leapmuxv1.GitSetup {
	return &leapmuxv1.GitSetup{Mode: &leapmuxv1.GitSetup_CreateBranch{
		CreateBranch: &leapmuxv1.GitCreateBranch{Name: name, BaseBranch: baseBranch},
	}}
}

// Builders for the TabIdent that CloseTabRequest carries.

func agentTabIdent(agentID string) *leapmuxv1.TabIdent {
	return &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}
}

func terminalTabIdent(terminalID string) *leapmuxv1.TabIdent {
	return &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID}
}

func fileTabIdent(tabID string) *leapmuxv1.TabIdent {
	return &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: tabID}
}
