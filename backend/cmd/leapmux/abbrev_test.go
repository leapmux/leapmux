package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/cli"
)

// TestAbbreviation_ResolvesInTheModeAndRecoverTrees pins the one abbreviation
// rule of cli.ExpandCommandPath on the trees of the lifecycle, the Worker and
// recover: a unique prefix resolves at every level, an exact name wins over a
// longer name that it prefixes, and a prefix of several names lists them.
func TestAbbreviation_ResolvesInTheModeAndRecoverTrees(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"rec", "res", "--help"}, "Usage:\n  leapmux recover reset-password <user> [flags]\n"},
		{[]string{"da", "hu", "stat", "--help"}, "Usage:\n  leapmux daemon hub status [flags]\n"},
		{[]string{"da", "wo", "cross", "l", "--help"}, "Usage:\n  leapmux daemon worker cross-worker-pins list [flags]\n"},
		{[]string{"da", "wo", "dereg", "--help"}, "Usage:\n  leapmux daemon worker deregister [flags]\n"},
		// "rotate" is also a prefix of "rotate-pepper"; the exact name wins.
		{[]string{"recover", "encryption-key", "rotate", "--help"}, "Usage:\n  leapmux recover encryption-key rotate [flags]\n"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			o := runTestCLI(t, tc.args...)
			assert.Equal(t, cli.ExitOK, o.code, o.stderr)
			assert.Contains(t, o.stdout, tc.want)
		})
	}
}

func TestAbbreviation_RefusesAnAmbiguousPrefix(t *testing.T) {
	o := runTestCLI(t, "daemon", "worker", "st")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `ambiguous command "st" for "leapmux daemon worker"; it matches: start, stop, status`)

	o = runTestCLI(t, "recover", "encryption-key", "r")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, "it matches: rotate, remove, reencrypt, rotate-pepper")
}
