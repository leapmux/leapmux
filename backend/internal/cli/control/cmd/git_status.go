package cmd

import (
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// gitStatus is a working tree's git state in a result.
type gitStatus struct {
	Branch     string `json:"branch,omitempty"`
	Ahead      int32  `json:"ahead,omitempty"`
	Behind     int32  `json:"behind,omitempty"`
	Toplevel   string `json:"toplevel,omitempty"`
	OriginURL  string `json:"origin_url,omitempty"`
	IsWorktree bool   `json:"is_worktree,omitempty"`
	// Changes lists the kinds of change that the working tree holds:
	// "modified", "added", "deleted", "renamed", "type_changed",
	// "untracked", "conflicted" and "stashed".
	Changes []string `json:"changes,omitempty"`
}

var _ cli.TextValuer = gitStatus{}

// TextValue returns a compact single-line summary of the git state for text
// and TSV rendering. JSON output keeps the full struct fields.
func (g gitStatus) TextValue() string {
	var parts []string
	switch {
	case g.Branch != "":
		parts = append(parts, g.Branch)
	case g.Toplevel != "":
		parts = append(parts, g.Toplevel)
	case g.OriginURL != "":
		parts = append(parts, g.OriginURL)
	}
	switch {
	case g.Ahead > 0 && g.Behind > 0:
		parts = append(parts, fmt.Sprintf("+%d/-%d", g.Ahead, g.Behind))
	case g.Ahead > 0:
		parts = append(parts, fmt.Sprintf("+%d", g.Ahead))
	case g.Behind > 0:
		parts = append(parts, fmt.Sprintf("-%d", g.Behind))
	}
	if len(g.Changes) > 0 {
		parts = append(parts, "("+strings.Join(g.Changes, ", ")+")")
	}
	if g.IsWorktree {
		parts = append(parts, "[worktree]")
	}
	return strings.Join(parts, " ")
}

func gitStatusOf(g *leapmuxv1.GitRepoStatus) *gitStatus {
	if g == nil {
		return nil
	}
	out := &gitStatus{
		Branch:     g.GetBranch(),
		Ahead:      g.GetAhead(),
		Behind:     g.GetBehind(),
		Toplevel:   g.GetToplevel(),
		OriginURL:  g.GetOriginUrl(),
		IsWorktree: g.GetIsWorktree(),
	}
	for _, c := range []struct {
		set  bool
		name string
	}{
		{g.GetModified(), "modified"},
		{g.GetAdded(), "added"},
		{g.GetDeleted(), "deleted"},
		{g.GetRenamed(), "renamed"},
		{g.GetTypeChanged(), "type_changed"},
		{g.GetUntracked(), "untracked"},
		{g.GetConflicted(), "conflicted"},
		{g.GetStashed(), "stashed"},
	} {
		if c.set {
			out.Changes = append(out.Changes, c.name)
		}
	}
	if out.Toplevel == "" && out.Branch == "" && out.OriginURL == "" &&
		out.Ahead == 0 && out.Behind == 0 && !out.IsWorktree && len(out.Changes) == 0 {
		return nil
	}
	return out
}
