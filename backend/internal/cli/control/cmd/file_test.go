package cmd

import (
	"flag"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// `file read --help` and `git read --help` state the default of --limit from the
// file-read contract, so the help cannot fall behind the default that the worker
// reads. The contract keeps the default a whole number of KiB.
func TestReadCommands_HelpStatesTheDefaultReadLimit(t *testing.T) {
	require.Zero(t, contracts.FileReadLimitBytes%1024, "the contract default is a whole number of KiB")
	want := fmt.Sprintf("max bytes (0 = default %d KiB)", contracts.FileReadLimitBytes/1024)

	for name, run := range map[string]func(any, []string) error{
		"file read": RunFileRead,
		"git read":  RunGitRead,
	} {
		t.Run(name, func(t *testing.T) {
			out := captureOSStdout(t, func() {
				require.ErrorIs(t, run(fakeCmdCtx{}, []string{"--help"}), flag.ErrHelp)
			})

			assert.Contains(t, out, "-limit int")
			assert.Contains(t, out, want)
		})
	}
}
