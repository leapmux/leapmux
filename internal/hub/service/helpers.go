package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"unicode"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
)

// DefaultModel is the model used when none is specified.
// Configurable via LEAPMUX_DEFAULT_MODEL environment variable.
var DefaultModel = getEnvOrDefault("LEAPMUX_DEFAULT_MODEL", "opus")

// DefaultEffort is the effort level used when none is specified.
// Configurable via LEAPMUX_DEFAULT_EFFORT environment variable.
var DefaultEffort = getEnvOrDefault("LEAPMUX_DEFAULT_EFFORT", "high")

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getVisibleWorkspace looks up a workspace by ID and org, then verifies the user can see it.
func getVisibleWorkspace(ctx context.Context, queries *db.Queries, user *auth.UserInfo, orgID, workspaceID string) (*db.Workspace, error) {
	ws, err := queries.GetWorkspaceByID(ctx, db.GetWorkspaceByIDParams{
		ID:    workspaceID,
		OrgID: orgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	_, err = queries.GetVisibleWorkspace(ctx, db.GetVisibleWorkspaceParams{
		UserID:      user.ID,
		WorkspaceID: workspaceID,
		OrgID:       ws.OrgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return &ws, nil
}

// getVisibleNonArchivedWorkspace combines visibility and archived checks.
// It verifies the user can see the workspace and that it is not in an archived section.
func getVisibleNonArchivedWorkspace(ctx context.Context, queries *db.Queries, user *auth.UserInfo, orgID, workspaceID string) (*db.Workspace, error) {
	ws, err := getVisibleWorkspace(ctx, queries, user, orgID, workspaceID)
	if err != nil {
		return nil, err
	}
	archived, err := queries.IsWorkspaceInArchivedSection(ctx, db.IsWorkspaceInArchivedSectionParams{
		UserID:      user.ID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check archived: %w", err))
	}
	if archived {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace is archived"))
	}
	return ws, nil
}

// sanitizeGitStatus validates and sanitizes git status data received from a worker.
// Defends against malicious or buggy worker implementations sending bad values.
func sanitizeGitStatus(gs *leapmuxv1.AgentGitStatus) *leapmuxv1.AgentGitStatus {
	if gs == nil {
		return nil
	}

	// Strip control characters and truncate branch name.
	branch := stripControlChars(gs.Branch)
	if len(branch) > 256 {
		branch = branch[:256]
	}

	return &leapmuxv1.AgentGitStatus{
		Branch:      branch,
		Ahead:       clampInt32(gs.Ahead, 0, 999999),
		Behind:      clampInt32(gs.Behind, 0, 999999),
		Conflicted:  gs.Conflicted,
		Stashed:     gs.Stashed,
		Deleted:     gs.Deleted,
		Renamed:     gs.Renamed,
		Modified:    gs.Modified,
		TypeChanged: gs.TypeChanged,
		Added:       gs.Added,
		Untracked:   gs.Untracked,
	}
}

// stripControlChars removes ASCII and Latin-1 control characters
// (U+0000–U+001F, U+007F–U+009F) from a string.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// ValidateBranchName validates a git branch name according to git-check-ref-format rules.
// Returns nil if valid, or an error describing the problem.
func ValidateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name must not be empty")
	}
	if len(name) > 256 {
		return fmt.Errorf("branch name must be at most 256 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("branch name must not contain control characters")
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', ']', '\\':
			return fmt.Errorf("branch name must not contain '%c'", r)
		}
	}
	if name[0] == '/' || name[0] == '.' || name[0] == '-' || name[0] == '@' {
		return fmt.Errorf("branch name must not start with '%c'", name[0])
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("branch name must not end with /, ., or .lock")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name must not contain '..'")
	}
	if strings.Contains(name, "//") {
		return fmt.Errorf("branch name must not contain '//'")
	}
	if strings.Contains(name, "/.") {
		return fmt.Errorf("branch name must not contain '/.'")
	}
	return nil
}

// clampInt32 clamps a value to the range [min, max].
func clampInt32(v, min, max int32) int32 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
