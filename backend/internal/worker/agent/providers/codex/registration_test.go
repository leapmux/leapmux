package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"unicode"

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
				filename := name
				if runtime.GOOS == "windows" {
					// PowerShell resolves through PATHEXT, so an executable on
					// Windows carries a suffix the contract's neutral name does
					// not state. Get-Command only resolves; it never runs it.
					filename += ".exe"
				}
				require.NoError(t, os.WriteFile(filepath.Join(directory, filename), []byte("#!/bin/sh\nexit 0\n"), 0o755))
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
	if runtime.GOOS == "windows" {
		// Each case must probe through a shell path no other case used: the
		// probe answer cache keys on the path, and the cases seed different
		// PATHs for the same names. A case variant of the one system path
		// names the same shell in its own directory -- NTFS preserves case but
		// matches it insensitively -- so every variant launches exactly the
		// shell the probe uses in production. Linking the shell into the test's
		// temp directory instead would run it from a directory its engine
		// assemblies do not live in.
		shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		return caseVariant(shell, windowsShellSerial.Add(1))
	}
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

// windowsShellSerial hands each uniqueShell call a case pattern of its own.
var windowsShellSerial atomic.Int32

// caseVariant spells path with the letter-case pattern its serial selects, so
// two callers name the same file by two strings. Case is the one path edit
// Windows treats as no edit at all: the filesystem matches insensitively and
// keeps the spelling, and no launcher along the way normalizes it. The pattern
// keys on LETTER positions, so two serials always differ in some letter: a bit
// keyed on raw positions could land only on separators, which carry no case.
func caseVariant(path string, serial int32) string {
	variant := []rune(path)
	letters := 0
	for i, r := range variant {
		if !unicode.IsLetter(r) {
			continue
		}
		if (serial>>uint(letters%32))&1 == 1 {
			variant[i] = unicode.ToUpper(r)
		} else {
			variant[i] = unicode.ToLower(r)
		}
		letters++
	}
	return string(variant)
}

func TestCaseVariantSpellsEachCallerDifferently(t *testing.T) {
	t.Parallel()

	path := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	seen := make(map[string]bool, 8)
	for serial := int32(1); serial <= 8; serial++ {
		variant := caseVariant(path, serial)
		assert.NotEqual(t, path, variant, "serial %d must leave the original spelling", serial)
		assert.False(t, seen[variant], "serial %d repeats an earlier variant", serial)
		seen[variant] = true
	}
}
