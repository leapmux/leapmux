package service

import "regexp"

// agentAutoTitlePattern matches auto-generated agent titles like
// "Agent Olivia". Used by plan-mode auto-rename to detect titles that
// are safe to overwrite with the agent's plan title.
//
// The single-name shape mirrors the frontend's pickTabName output
// (single ASCII token, capitalized). Keep these in sync.
var agentAutoTitlePattern = regexp.MustCompile(`^Agent [A-Z][A-Za-z]+$`)
