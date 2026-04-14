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
			name:    "Design prefix",
			content: "# Design: Renderer fixes",
			want:    "Renderer fixes",
		},
		{
			name:    "Design Doc prefix",
			content: "# Design Doc: Renderer fixes",
			want:    "Renderer fixes",
		},
		{
			name:    "Design Doc stripped before Design",
			content: "# Design Doc: API changes",
			want:    "API changes",
		},
		{
			name:    "design doc mixed case",
			content: "# dEsIgN dOc - API changes",
			want:    "API changes",
		},
		{
			name:    "wrapped Design Doc prefix",
			content: "# [Design Doc] API changes",
			want:    "API changes",
		},
		{
			name:    "wrapped Design prefix",
			content: "# (Design) Renderer fixes",
			want:    "Renderer fixes",
		},
		{
			name:    "Design with em dash",
			content: "# Design — Migrate renderer",
			want:    "Migrate renderer",
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

func TestSanitizePlanFilenameTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "removes invalid characters",
			title: `A/B\C:D*E?F"G<H>I|J`,
			want:  "ABCDEFGHIJ",
		},
		{
			name:  "trims trailing dots and spaces",
			title: "Plan Name.  ",
			want:  "Plan Name",
		},
		{
			name:  "falls back when empty",
			title: " \t\r\n ",
			want:  "Untitled Plan",
		},
		{
			name:  "retains unicode",
			title: "설계 문서 渲染修复",
			want:  "설계 문서 渲染修复",
		},
		{
			name:  "collapses whitespace and strips controls",
			title: "Plan\t\x00  Name\n\r",
			want:  "Plan Name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizePlanFilenameTitle(tt.title))
		})
	}
}
