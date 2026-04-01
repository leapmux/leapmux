package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractPlanTitle(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty",
			content: "",
			want:    "",
		},
		{
			name:    "simple heading",
			content: "# Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "heading with bold",
			content: "# **Refactor auth middleware**",
			want:    "Refactor auth middleware",
		},
		{
			name:    "Plan: prefix",
			content: "# Plan: Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "Plan - prefix",
			content: "# Plan - Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "[Plan] prefix",
			content: "# [Plan] Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "plan: lowercase",
			content: "# plan: fix login bug",
			want:    "fix login bug",
		},
		{
			name:    "PLAN: uppercase",
			content: "# PLAN: Fix login bug",
			want:    "Fix login bug",
		},
		{
			name:    "Plan with em dash",
			content: "# Plan — Migrate to new API",
			want:    "Migrate to new API",
		},
		{
			name:    "Plan with en dash",
			content: "# Plan – Migrate to new API",
			want:    "Migrate to new API",
		},
		{
			name:    "(Plan) prefix",
			content: "# (Plan) Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "*Plan* prefix",
			content: "# *Plan* Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "**Plan** prefix",
			content: "## **Plan** - Refactor auth",
			want:    "Refactor auth",
		},
		{
			name:    "{Plan} prefix",
			content: "# {Plan} Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "<Plan> prefix",
			content: "# <Plan> Add dark mode toggle",
			want:    "Add dark mode toggle",
		},
		{
			name:    "no prefix left untouched",
			content: "# Implement caching layer",
			want:    "Implement caching layer",
		},
		{
			name:    "plan word in middle is not stripped",
			content: "# Implement plan caching",
			want:    "Implement plan caching",
		},
		{
			name:    "frontmatter skipped",
			content: "---\ntitle: test\n---\n# Plan: My title",
			want:    "My title",
		},
		{
			name:    "blank lines before heading",
			content: "\n\n# Plan: Real title",
			want:    "Real title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractPlanTitle(tt.content))
		})
	}
}
