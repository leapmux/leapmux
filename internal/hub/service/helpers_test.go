package service

import (
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
)

func TestSanitizeGitStatus_Nil(t *testing.T) {
	assert.Nil(t, sanitizeGitStatus(nil))
}

func TestSanitizeGitStatus_PassthroughClean(t *testing.T) {
	input := &leapmuxv1.AgentGitStatus{
		Branch:      "main",
		Ahead:       3,
		Behind:      1,
		Conflicted:  true,
		Stashed:     true,
		Deleted:     true,
		Renamed:     true,
		Modified:    true,
		TypeChanged: true,
		Added:       true,
		Untracked:   true,
	}
	result := sanitizeGitStatus(input)
	assert.Equal(t, "main", result.Branch)
	assert.Equal(t, int32(3), result.Ahead)
	assert.Equal(t, int32(1), result.Behind)
	assert.True(t, result.Conflicted)
	assert.True(t, result.Stashed)
	assert.True(t, result.Deleted)
	assert.True(t, result.Renamed)
	assert.True(t, result.Modified)
	assert.True(t, result.TypeChanged)
	assert.True(t, result.Added)
	assert.True(t, result.Untracked)
}

func TestSanitizeGitStatus_StripControlChars(t *testing.T) {
	input := &leapmuxv1.AgentGitStatus{
		Branch: "main\n\r\x00\x1f\x7f",
	}
	result := sanitizeGitStatus(input)
	assert.Equal(t, "main", result.Branch)
}

func TestSanitizeGitStatus_TruncateBranch(t *testing.T) {
	longBranch := strings.Repeat("a", 300)
	input := &leapmuxv1.AgentGitStatus{
		Branch: longBranch,
	}
	result := sanitizeGitStatus(input)
	assert.Equal(t, 256, len(result.Branch))
	assert.Equal(t, strings.Repeat("a", 256), result.Branch)
}

func TestSanitizeGitStatus_ClampAheadBehind(t *testing.T) {
	tests := []struct {
		name       string
		ahead      int32
		behind     int32
		wantAhead  int32
		wantBehind int32
	}{
		{"normal values", 5, 10, 5, 10},
		{"zero values", 0, 0, 0, 0},
		{"negative clamped to zero", -5, -10, 0, 0},
		{"large clamped to max", 2000000, 1500000, 999999, 999999},
		{"max boundary", 999999, 999999, 999999, 999999},
		{"just over max", 1000000, 1000000, 999999, 999999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &leapmuxv1.AgentGitStatus{
				Ahead:  tt.ahead,
				Behind: tt.behind,
			}
			result := sanitizeGitStatus(input)
			assert.Equal(t, tt.wantAhead, result.Ahead)
			assert.Equal(t, tt.wantBehind, result.Behind)
		})
	}
}

func TestStripControlChars(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no control chars", "feature/branch", "feature/branch"},
		{"newline", "branch\nname", "branchname"},
		{"carriage return", "branch\rname", "branchname"},
		{"tab", "branch\tname", "branchname"},
		{"null byte", "branch\x00name", "branchname"},
		{"mixed control chars", "\x01\x02hello\x7fworld", "helloworld"},
		{"latin-1 controls", "hello\u0080world\u009f", "helloworld"},
		{"empty string", "", ""},
		{"unicode preserved", "feature/日本語", "feature/日本語"},
		{"slashes preserved", "feature/JIRA-123/fix", "feature/JIRA-123/fix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripControlChars(tt.input))
		})
	}
}

func TestValidateBranchName(t *testing.T) {
	validNames := []string{
		"feature-branch",
		"fix/login-bug",
		"v1.0.0",
		"my_branch",
		"a",
		"feature/deep/nesting",
		"UPPERCASE",
		"mixed-Case_123",
		"release/2024.01",
		strings.Repeat("a", 256), // max length
	}

	for _, name := range validNames {
		t.Run("valid: "+name[:min(len(name), 30)], func(t *testing.T) {
			assert.NoError(t, ValidateBranchName(name))
		})
	}

	invalidTests := []struct {
		name    string
		input   string
		wantMsg string
	}{
		{"empty", "", "must not be empty"},
		{"too long", strings.Repeat("a", 257), "at most 256"},
		{"space", "foo bar", "must not contain ' '"},
		{"tilde", "foo~bar", "must not contain '~'"},
		{"caret", "foo^bar", "must not contain '^'"},
		{"colon", "foo:bar", "must not contain ':'"},
		{"question", "foo?bar", "must not contain '?'"},
		{"asterisk", "foo*bar", "must not contain '*'"},
		{"open bracket", "foo[bar", "must not contain '['"},
		{"close bracket", "foo]bar", "must not contain ']'"},
		{"backslash", "foo\\bar", "must not contain '\\'"},
		{"null byte", "foo\x00bar", "control characters"},
		{"newline", "foo\nbar", "control characters"},
		{"tab", "foo\tbar", "control characters"},
		{"DEL", "foo\x7fbar", "control characters"},
		{"leading dot", ".foo", "must not start with '.'"},
		{"leading dash", "-foo", "must not start with '-'"},
		{"leading slash", "/foo", "must not start with '/'"},
		{"leading @", "@foo", "must not start with '@'"},
		{"trailing slash", "foo/", "must not end with"},
		{"trailing dot", "foo.", "must not end with"},
		{"trailing .lock", "foo.lock", "must not end with"},
		{"double dot", "foo..bar", "must not contain '..'"},
		{"double slash", "foo//bar", "must not contain '//'"},
		{"slash-dot", "foo/.bar", "must not contain '/.'"},
	}

	for _, tt := range invalidTests {
		t.Run("invalid: "+tt.name, func(t *testing.T) {
			err := ValidateBranchName(tt.input)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

func TestBuildPlanExecMessage_WithFilePath(t *testing.T) {
	msg := buildPlanExecMessage("/home/user/.claude/plans/plan.md", "step 1\nstep 2")
	expected := "Execute the following plan:\n\n---\n\nstep 1\nstep 2\n\n---\n\nThe above plan has been written to /home/user/.claude/plans/plan.md — re-read it if needed."
	assert.Equal(t, expected, msg)
}

func TestBuildPlanExecMessage_WithoutFilePath(t *testing.T) {
	msg := buildPlanExecMessage("", "step 1\nstep 2")
	assert.Equal(t, "Execute the following plan:\n\n---\n\nstep 1\nstep 2", msg)
}

func TestClampInt32(t *testing.T) {
	tests := []struct {
		name     string
		v        int32
		min, max int32
		want     int32
	}{
		{"within range", 50, 0, 100, 50},
		{"at min", 0, 0, 100, 0},
		{"at max", 100, 0, 100, 100},
		{"below min", -5, 0, 100, 0},
		{"above max", 150, 0, 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, clampInt32(tt.v, tt.min, tt.max))
		})
	}
}
