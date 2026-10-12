//go:build unix

package fastagent

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// fast-agent checks PyPI for a newer release, and prints a notice, unless the
// root option `--no-update-check` says otherwise. It has no environment variable
// and no configuration key for this. Typer reads the option only before the
// subcommand: `acp` declares `ignore_unknown_options`, so the same option after
// `acp` is swallowed and stops nothing. The `acp` mode never runs the check
// today, so the option is the only guard against a later release that does.
func TestStartPassesTheUpdateCheckOptionBeforeTheSubcommand(t *testing.T) {
	runs := agenttest.RunLaunch(t, agenttest.LaunchProbe{Binary: "fast-agent", Start: Start})
	require.Len(t, runs, 1)
	option := slices.Index(runs[0].Args, "--no-update-check")
	subcommand := slices.Index(runs[0].Args, "acp")
	require.NotEqual(t, -1, option, "the launch must pass --no-update-check")
	require.NotEqual(t, -1, subcommand, "the launch must run the acp subcommand")
	assert.Less(t, option, subcommand, "the option must come before the subcommand")
}
