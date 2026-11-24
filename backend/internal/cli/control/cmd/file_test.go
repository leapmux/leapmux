package cmd

import (
	"fmt"
	"strings"
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
	want := fmt.Sprintf("the default of %d KiB", contracts.FileReadLimitBytes/1024)

	for _, path := range [][]string{{"file", "read"}, {"git", "read"}} {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			o := runControlText(t, append(path, "--help")...)
			require.Equal(t, 0, o.Code)
			assert.Contains(t, o.Stdout, "--limit int")
			assert.Contains(t, o.Stdout, want)
		})
	}
}
