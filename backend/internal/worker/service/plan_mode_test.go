package service

import "testing"

func TestAgentAutoTitlePattern(t *testing.T) {
	matches := []string{
		"Agent Olivia",
		"Agent Liam",
		"Agent Emma",
	}
	rejects := []string{
		"",
		"Agent",
		"Agent ",
		"Agent A",            // single letter — frontend never produces this; regex requires 2+ chars
		"Agent 1",            // legacy numeric form is no longer treated as auto-generated
		"Agent  Olivia",      // double space
		"agent Olivia",       // lowercase prefix
		"Agent olivia",       // lowercase first letter of name
		"Agent Olivia ",      // trailing space
		" Agent Olivia",      // leading space
		"Agent Olivia Smith", // multi-word
		"Agent Liam2",        // digit in name
		"Terminal Olivia",    // wrong prefix
		"Refactor auth",
	}

	for _, s := range matches {
		if !agentAutoTitlePattern.MatchString(s) {
			t.Errorf("expected auto-title pattern to match %q", s)
		}
	}
	for _, s := range rejects {
		if agentAutoTitlePattern.MatchString(s) {
			t.Errorf("expected auto-title pattern to reject %q", s)
		}
	}
}
