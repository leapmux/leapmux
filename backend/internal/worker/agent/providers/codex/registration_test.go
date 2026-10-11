package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The registration probes the names that contracts/codex-protocol.json lists, in
// the contract's order. The E2E suite decides whether the worker can start Codex
// from the same list, so a name that only one side knows makes the suite run specs
// against a CLI that the worker never starts, or skip specs that it can run.
func TestRegistrationLocatesEachContractExecutableInOrder(t *testing.T) {
	require.Len(t, contracts.CodexExecutableKeys, 2, "this test states one case for each contract name")
	preferred, hostTriple := contracts.CodexExecutableKeys[0], contracts.CodexExecutableKeys[1]

	for _, tc := range []struct {
		name    string
		present []string
		want    string
	}{
		{"the host triple alone", []string{hostTriple}, hostTriple},
		{"both names", []string{hostTriple, preferred}, preferred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The probe caches each answer by the shell path, so each case takes a
			// shell path of its own. Another test's answer then cannot stand in for
			// this probe.
			shell := uniqueShell(t)
			directory := t.TempDir()
			for _, name := range tc.present {
				require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
			}
			t.Setenv("PATH", directory)

			spec, err := Registration().Locator.Resolve(context.Background(), shell, false, "Codex")
			require.NoError(t, err)
			assert.Equal(t, tc.want, spec.Program)
		})
	}
}

// uniqueShell is a link to the POSIX shell at a path that no other test uses.
func uniqueShell(t *testing.T) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	link := filepath.Join(t.TempDir(), "sh")
	if err := os.Symlink(shell, link); err != nil {
		t.Skipf("this machine cannot link the POSIX shell: %v", err)
	}
	return link
}
