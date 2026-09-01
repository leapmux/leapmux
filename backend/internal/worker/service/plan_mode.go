package service

import (
	"regexp"

	"github.com/leapmux/leapmux/generated/contracts"
)

// agentAutoTitlePattern matches auto-generated agent titles like
// "Agent Olivia". Used by plan-mode auto-rename to detect titles that
// are safe to overwrite with the agent's plan title.
//
// Built FROM the contract rather than spelled out, so the prefix cannot drift
// from the one pickAgentTitle joins. The name half stays a literal, and the
// contract's schema holds every pooled name to the same ^[A-Z][A-Za-z]+$
// shape -- a name that stopped matching would silently disable auto-rename
// for every tab it named, so generation refuses it instead.
var agentAutoTitlePattern = regexp.MustCompile(`^` + regexp.QuoteMeta(contracts.AgentTitlePrefix) + ` [A-Z][A-Za-z]+$`)
