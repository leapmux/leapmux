package service

import "regexp"

// agentAutoTitlePattern matches auto-generated agent titles like "Agent 1".
var agentAutoTitlePattern = regexp.MustCompile(`^Agent \d+$`)
