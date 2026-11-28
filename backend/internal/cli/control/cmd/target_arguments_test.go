package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

func TestRejectBlankTargets_PositionalAndScopeFlagsBeforeDialing(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{
			name:    "blank tile-a",
			args:    []string{"tile", "swap", "", "tile-b-id"},
			wantMsg: "the tile-a is empty",
		},
		{
			name:    "whitespace tile-a",
			args:    []string{"tile", "swap", " \t ", "tile-b-id"},
			wantMsg: "the tile-a is empty",
		},
		{
			name:    "blank tile-b",
			args:    []string{"tile", "swap", "tile-a-id", ""},
			wantMsg: "the tile-b is empty",
		},
		{
			name:    "whitespace tile-b",
			args:    []string{"tile", "swap", "tile-a-id", "  "},
			wantMsg: "the tile-b is empty",
		},
		{
			name:    "blank window on move",
			args:    []string{"window", "move", "", "--x", "10"},
			wantMsg: "the window is empty",
		},
		{
			name:    "whitespace window on resize",
			args:    []string{"window", "resize", " \t ", "--width", "200"},
			wantMsg: "the window is empty",
		},
		{
			name:    "blank window on set-opacity",
			args:    []string{"window", "set-opacity", "", "0.5"},
			wantMsg: "the window is empty",
		},
		{
			name:    "blank window on close",
			args:    []string{"window", "close", ""},
			wantMsg: "the window is empty",
		},
		{
			name:    "blank section on rename",
			args:    []string{"section", "rename", "", "New Name"},
			wantMsg: "the section is empty",
		},
		{
			name:    "whitespace section on move",
			args:    []string{"section", "move", "  ", "--first"},
			wantMsg: "the section is empty",
		},
		{
			name:    "blank section on delete",
			args:    []string{"section", "delete", ""},
			wantMsg: "the section is empty",
		},
		{
			name:    "blank --parent flag",
			args:    []string{"agent", "list", "--parent", ""},
			wantMsg: "--parent is empty",
		},
		{
			name:    "whitespace --parent flag",
			args:    []string{"agent", "list", "--parent", " \t "},
			wantMsg: "--parent is empty",
		},
		{
			name:    "blank --section flag on workspace move",
			args:    []string{"workspace", "move", "ws-1", "--section", ""},
			wantMsg: "--section is empty",
		},
		{
			name:    "whitespace --section flag on workspace unarchive",
			args:    []string{"workspace", "unarchive", "ws-1", "--section", "  "},
			wantMsg: "--section is empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &recordingHub{}
			startSpawnIPC(t, hub, nil)
			f := runControl(t, tc.args...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, tc.wantMsg)
			assert.Empty(t, hub.called(), "blank target must fail locally before any RPC")
		})
	}
}

func TestScopeTargetFlags_UnifiedCompletionRegistration(t *testing.T) {
	root := NewCommand()
	for _, tc := range []struct {
		path []string
		flag string
	}{
		{path: []string{"tab", "list"}, flag: "workspace"},
		{path: []string{"agent", "list"}, flag: "worker"},
		{path: []string{"tab", "move"}, flag: "tile"},
		{path: []string{"agent", "list"}, flag: "parent"},
		{path: []string{"workspace", "move"}, flag: "section"},
	} {
		cmd, _, err := root.Find(tc.path)
		require.NoError(t, err, "path=%v", tc.path)
		_, ok := cmd.GetFlagCompletionFunc(tc.flag)
		assert.True(t, ok, "path=%v missing completion for --%s", tc.path, tc.flag)
	}
}
