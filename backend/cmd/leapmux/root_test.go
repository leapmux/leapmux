package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/clitest"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func TestRunCLIExplicitRouting(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "bare command prints the root help",
			args:       nil,
			wantCode:   cli.ExitOK,
			wantStdout: rootHelpGolden,
		},
		{
			name:       "top-level help flag prints help",
			args:       []string{"--help"},
			wantCode:   cli.ExitOK,
			wantStdout: rootHelpGolden,
		},
		{
			name:       "top-level help command prints help",
			args:       []string{"help"},
			wantCode:   cli.ExitOK,
			wantStdout: rootHelpGolden,
		},
		{
			name:       "a single-dash long flag states the two-dash form",
			args:       []string{"-dev-frontend", "http://localhost:4328"},
			wantCode:   cli.ExitUsage,
			wantStderr: `hint: a long flag takes two dashes, as in "--version"`,
		},
		{
			name:       "a mode command parses with the parser of every command",
			args:       []string{"solo", "run", "-dev-frontend", "http://localhost:4328"},
			wantCode:   cli.ExitUsage,
			wantStderr: `hint: a long flag takes two dashes, as in "--version"`,
		},
		{
			name:       "a flag before the mode command states where the flag goes",
			args:       []string{"solo", "--dev-frontend", "http://localhost:4328"},
			wantCode:   cli.ExitUsage,
			wantStderr: `hint: a flag goes after the command that takes it, as in "leapmux solo <command> [flags]"`,
		},
		{
			name:       "the version flag belongs to the root alone",
			args:       []string{"solo", "--version"},
			wantCode:   cli.ExitUsage,
			wantStderr: "unknown flag: --version",
		},
		{
			name:       "hub run resolves to the lifecycle command",
			args:       []string{"hub", "run", "--help"},
			wantCode:   cli.ExitOK,
			wantStdout: lifecycleSummary("hub", "run") + "\n\nUsage:\n  leapmux hub run [flags]\n",
		},
		{
			name:       "worker register checks its required flags before it runs",
			args:       []string{"worker", "register", "--hub", "https://hub.example.com"},
			wantCode:   cli.ExitUsage,
			wantStderr: "missing required flag --registration-key",
		},
		{
			name:       "recover help prints the recover usage",
			args:       []string{"recover", "--help"},
			wantCode:   cli.ExitOK,
			wantStdout: "Offline break-glass recovery (opens the database directly).\n",
		},
		{
			name:       "a recover group alone prints its help",
			args:       []string{"recover", "password"},
			wantCode:   cli.ExitOK,
			wantStdout: "Reset passwords offline\n\nUsage:\n  leapmux recover password <command>\n",
		},
		{
			name:       "unknown recover group is refused",
			args:       []string{"recover", "bogus"},
			wantCode:   cli.ExitUsage,
			wantStderr: `unknown command "bogus" for "leapmux recover"`,
		},
		{
			name:       "unknown recover group command is refused",
			args:       []string{"recover", "password", "bogus"},
			wantCode:   cli.ExitUsage,
			wantStderr: `unknown command "bogus" for "leapmux recover password"`,
		},
		{
			name:       "the old admin tree is gone",
			args:       []string{"admin", "user", "list"},
			wantCode:   cli.ExitUsage,
			wantStderr: `unknown command "admin" for "leapmux"`,
		},
		{
			name:       "help of a nested group goes through the help command",
			args:       []string{"help", "recover", "encryption-key"},
			wantCode:   cli.ExitOK,
			wantStdout: "Manage encryption keys\n\nUsage:\n  leapmux recover encryption-key <command>\n",
		},
		{
			name:       "unknown command is rejected",
			args:       []string{"bogus"},
			wantCode:   cli.ExitUsage,
			wantStderr: `unknown command "bogus" for "leapmux"`,
		},
		{
			name:       "version command prints version",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
		{
			name:       "top-level long version flag prints version",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
		{
			name:       "top-level short version flag prints version",
			args:       []string{"-v"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := runTestCLI(t, tt.args...)

			assert.Equal(t, tt.wantCode, o.code, "stderr: %s", o.stderr)
			assert.True(t, strings.HasPrefix(o.stdout, tt.wantStdout), "stdout: %s", o.stdout)
			if tt.wantStdout == "" {
				assert.Empty(t, o.stdout)
			}
			assert.Contains(t, o.stderr, tt.wantStderr)
		})
	}
}

func TestRunCLIBareModePrintsItsHelp(t *testing.T) {
	// A mode alone is a group: it prints its commands and starts nothing.
	o := runTestCLI(t, "hub")

	assert.Equal(t, cli.ExitOK, o.code)
	assert.Empty(t, o.stderr)
	assert.Contains(t, o.stdout, "Usage:\n  leapmux hub <command>\n")
}

func TestRunCLISubcommandHelpReturnsSuccess(t *testing.T) {
	// The mode group's help prints its verbs without starting anything.
	o := runTestCLI(t, "hub", "--help")

	assert.Equal(t, 0, o.code)
	assert.Contains(t, o.stdout, "run")
	assert.Contains(t, o.stdout, "enable")
}

// TestRunCLI_TopLevelAbbreviation verifies the top-level command dispatch
// resolves unambiguous prefixes.
func TestRunCLI_TopLevelAbbreviation(t *testing.T) {
	t.Run("abbreviated solo resolves its run verb", func(t *testing.T) {
		o := runTestCLI(t, "sol", "run", "--help")
		assert.Equal(t, 0, o.code, o.stderr)
		assert.Contains(t, o.stdout, "Usage:\n  leapmux solo run [flags]\n")
	})

	t.Run("abbreviated version dispatches", func(t *testing.T) {
		o := runTestCLI(t, "ver")
		assert.Equal(t, 0, o.code)
		assert.Equal(t, "test-version\n", o.stdout)
	})

	t.Run("abbreviated recover dispatches", func(t *testing.T) {
		dataDir := t.TempDir()
		o := runTestCLI(t, "rec", "db", "path", "--data-dir", dataDir)
		assert.Equal(t, 0, o.code, o.stderr)
		assert.Equal(t, filepath.Join(dataDir, "hub.db")+"\n", o.stdout)
	})

	t.Run("abbreviated control dispatches", func(t *testing.T) {
		// "con" is a unique prefix of "control"; "c" also matches
		// "commands" and "completion". `guide` prints without a hub.
		o := runTestCLI(t, "con", "gu")
		assert.Equal(t, 0, o.code, "stderr: %s", o.stderr)
		assert.Contains(t, o.stdout, "leapmux control")
	})

	t.Run("abbreviated dev resolves its run verb", func(t *testing.T) {
		o := runTestCLI(t, "d", "run", "--help")
		assert.Equal(t, 0, o.code, o.stderr)
		assert.Contains(t, o.stdout, "Usage:\n  leapmux dev run [flags]\n")
	})
}

// TestRunCLI_TopLevelAmbiguity verifies that an ambiguous top-level prefix
// fails with every candidate, and that a token that matches nothing fails
// as an unknown command.
func TestRunCLI_TopLevelAmbiguity(t *testing.T) {
	t.Run("an ambiguous prefix lists the candidates", func(t *testing.T) {
		o := runTestCLI(t, "c")
		assert.Equal(t, cli.ExitUsage, o.code)
		assert.Contains(t, o.stderr, `ambiguous command "c" for "leapmux"`)
		for _, name := range []string{"control", "commands", "completion"} {
			assert.Contains(t, o.stderr, name)
		}
	})

	t.Run("unknown command reports error", func(t *testing.T) {
		o := runTestCLI(t, "xyz")
		assert.Equal(t, cli.ExitUsage, o.code)
		assert.Contains(t, o.stderr, `unknown command "xyz" for "leapmux"`)
	})

	t.Run("an unknown flag is a usage error", func(t *testing.T) {
		o := runTestCLI(t, "-x")
		assert.Equal(t, cli.ExitUsage, o.code)
		assert.Contains(t, o.stderr, "unknown shorthand flag")
	})

	t.Run("a failure prints its envelope on stdout with --json", func(t *testing.T) {
		o := runTestCLI(t, "xyz", "--json")
		assert.Equal(t, cli.ExitUsage, o.code)
		var env struct {
			Error map[string]any `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(o.stdout), &env), "stdout: %s", o.stdout)
		assert.Equal(t, "usage", env.Error["code"])
	})
}

// rootHelpGolden is the top-level help, byte for byte.
//
// This is the first text a new operator reads, so the column width, the
// blank lines and the order of the rows are pinned here, independently of
// the renderer that prints them.
const rootHelpGolden = "LeapMux runs coding agents and terminals on your machines and shows them in\n" +
	"your browser.\n" +
	"\n" +
	"Start with \"leapmux solo start\" on one machine. \"leapmux control\" drives a\n" +
	"running LeapMux from a script or from an agent.\n" +
	"\n" +
	"Usage:\n" +
	"  leapmux <command>\n" +
	"\n" +
	"Commands:\n" +
	"  solo        Run Hub + Worker locally for single-user use\n" +
	"  hub         Run the Hub service\n" +
	"  worker      Run a Worker connected to a Hub\n" +
	"  dev         Run Hub + Worker for development\n" +
	"  recover     Offline break-glass recovery (opens the database directly)\n" +
	"  control     Control LeapMux from a script or from an agent\n" +
	"  version     Print the version of this binary\n" +
	"  commands    List every command; with --json, with its arguments, flags and examples\n" +
	"  completion  Print the shell completion script\n" +
	"  help        Print the help of a command\n" +
	"\n" +
	"Flags:\n" +
	"  -v, --version  print the version of this binary\n" +
	"\n" +
	"Run \"leapmux <command> --help\" for the help of a command.\n" +
	"Any command name can be shortened as far as it stays unambiguous. A later\n" +
	"release can make a short form ambiguous, so a script uses full names.\n"

// cliReferenceUsagePath is the reference page that reproduces the top-level
// help. The path is relative to this package's directory.
const cliReferenceUsagePath = "../../../site/content/docs/reference/cli-reference.md"

// TestRootHelp_MatchesTheReferenceDoc proves the reference page quotes the
// help the binary actually prints.
//
// The page reproduces the whole block verbatim, and a reader takes it as the
// output. It drifted twice already: a rename of the `control` summary left the
// page quoting a sentence the binary no longer printed, and the page never
// carried the abbreviation line at all.
func TestRootHelp_MatchesTheReferenceDoc(t *testing.T) {
	page, err := os.ReadFile(cliReferenceUsagePath)
	require.NoError(t, err, "the reference page has to be readable from this package")

	// The block is the one fence that holds the "Usage:" line of the root.
	// Take it by its own first line rather than by a fence count, so an added
	// fence earlier in the page does not move the match.
	first, _, _ := strings.Cut(rootHelpGolden, "\n")
	start := strings.Index(string(page), first+"\n")
	require.GreaterOrEqual(t, start, 0, "the reference page no longer quotes the top-level help")
	rest := string(page)[start:]
	end := strings.Index(rest, "```")
	require.GreaterOrEqual(t, end, 0, "the quoted help block is not closed")

	assert.Equal(t, rootHelpGolden, rest[:end],
		"the reference page and the binary have to print the same help")
}

// TestRootCommands_EveryCommandRuns proves that every top-level command does
// something when it runs alone: a group prints its help, and a leaf prints
// its result or its refusal.
func TestRootCommands_EveryCommandRuns(t *testing.T) {
	for _, cmd := range newRootCommand(testCLIRunners(t)).Commands() {
		t.Run(cmd.Name(), func(t *testing.T) {
			o := runTestCLI(t, cmd.Name())
			assert.NotContains(t, o.stderr, "unknown command",
				"the abbreviation must resolve every command to itself")
			assert.True(t, o.stdout != "" || o.stderr != "", "a command prints something")
		})
	}
}

// `leapmux completion <shell>` prints a script for each supported shell, and
// refuses any other shell with the list of supported ones.
func TestCompletion_PrintsAScriptForEachShell(t *testing.T) {
	marks := map[string]string{
		"bash":       "__start_leapmux",
		"zsh":        "#compdef leapmux",
		"fish":       "complete -c leapmux",
		"powershell": "Register-ArgumentCompleter",
	}
	require.Len(t, marks, len(shells), "every supported shell has a mark here")
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			o := runTestCLI(t, "completion", shell)
			assert.Equal(t, 0, o.code, "stderr: %s", o.stderr)
			assert.Contains(t, o.stdout, marks[shell])
		})
	}

	o := runTestCLI(t, "completion", "tcsh")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `unknown shell "tcsh"`)
	assert.Contains(t, o.stderr, "bash, zsh, fish, powershell")
}

// The completion asks the binary for its candidates, so a prefix completes
// to the command names it can still become.
func TestCompletion_CompletesCommandNames(t *testing.T) {
	o := runTestCLI(t, "__complete", "control", "wo")
	assert.Equal(t, 0, o.code, "stderr: %s", o.stderr)
	assert.Contains(t, o.stdout, "workspace")
	assert.Contains(t, o.stdout, "worker")
	assert.NotContains(t, o.stdout, "agent", "a candidate must start with the typed prefix")
}

// `leapmux commands --json` lists every command of the tree with its
// arguments and flags, so a caller learns the grammar without parsing help.
func TestCommands_DescribesTheWholeTree(t *testing.T) {
	o := runTestCLI(t, "commands", "--json")
	require.Equal(t, 0, o.code, "stderr: %s", o.stderr)
	var env struct {
		Data cli.CommandTree `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(o.stdout), &env))

	byPath := map[string]cli.CommandInfo{}
	for _, c := range env.Data.Commands {
		byPath[c.Path] = c
	}
	for _, path := range []string{"solo", "recover", "control", "control agent send", "control admin user get", "version", "completion"} {
		assert.Contains(t, byPath, path)
	}
	send := byPath["control agent send"]
	assert.True(t, send.Mutates)
	require.Len(t, send.Args, 2)
	assert.Equal(t, "agent", send.Args[0].Name)
	assert.True(t, send.Args[0].Required)
	assert.NotEmpty(t, send.Args[0].Help, "a target argument states its forms")
	assert.NotEmpty(t, send.Examples)
	assert.NotContains(t, byPath, "help", "the help command is not part of the grammar")
}

// `leapmux control` alone, and each of its groups alone, prints the help of
// the group on stdout and exits 0: the caller asked what the group holds.
func TestControlGroups_PrintTheirHelp(t *testing.T) {
	o := runTestCLI(t, "control")
	assert.Equal(t, 0, o.code)
	assert.Contains(t, o.stdout, "Usage:\n  leapmux control <command>\n")
	assert.Contains(t, o.stdout, "\n  admin ")
	assert.Empty(t, o.stderr)

	o = runTestCLI(t, "control", "worker", "pins")
	assert.Equal(t, 0, o.code)
	for _, verb := range []string{"list", "get", "remove"} {
		assert.Contains(t, o.stdout, "\n  "+verb+" ")
	}
}

// A token that resolves to nothing under a group is refused with the path of
// the group, at every depth.
func TestControlGroups_RefuseAnUnknownCommand(t *testing.T) {
	o := runTestCLI(t, "control", "xyz")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `unknown command "xyz" for "leapmux control"`)

	o = runTestCLI(t, "control", "admin", "xyz")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `unknown command "xyz" for "leapmux control admin"`)
}

// TestCommandTree_KeepsTheRulesOfEveryLeaf runs the checks of every leaf of
// the binary: a summary, examples that parse against the grammar, flag help
// written one way, and no optional target on a command that changes state.
func TestCommandTree_KeepsTheRulesOfEveryLeaf(t *testing.T) {
	var targets []string
	for kind := resolve.KindTab; kind <= resolve.KindWorker; kind++ {
		targets = append(targets, kind.String())
	}
	require.Contains(t, targets, "worker", "the loop must reach the last kind")
	clitest.CheckTree(t, newRootCommand(testCLIRunners(t)), clitest.TreeRules{Targets: targets})
}

var updateGolden = flag.Bool("update-golden", false, "rewrite the reference pages of the docs site that the command tree generates")

// controlReferencePath is the reference page of `leapmux control` that the
// command tree generates. The path is relative to this package's directory.
const controlReferencePath = "../../../site/content/docs/reference/control-commands.md"

// controlReferenceHeader opens the generated page: its front matter, and the
// sentence that sends a reader to the chapter that explains the concepts.
const controlReferenceHeader = `---
title: "Control Command Reference"
description: "Every leapmux control command with its arguments, flags and examples, generated from the command tree of the binary."
type: docs
weight: 2
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

This page lists every command of ` + "`leapmux control`" + ` with its arguments, its flags and its examples. The binary prints the same text with ` + "`--help`" + `, and ` + "`leapmux commands --json`" + ` prints it for a program. [Control CLI](/docs/using/control-cli/) explains the concepts: how a command selects its target, what it prints, how it fails, and how it signs in.

`

// TestControlReference_IsCurrent keeps the generated page equal to the
// command tree. The page is also the reviewed record of the grammar: a change
// to a command shows in its diff.
func TestControlReference_IsCurrent(t *testing.T) {
	var body bytes.Buffer
	require.NoError(t, cli.WriteReference(&body, cli.Describe(newRootCommand(testCLIRunners(t))), "control"))
	got := controlReferenceHeader + strings.TrimRight(body.String(), "\n") + "\n"
	// The page is published, so a value that a flag reads from this machine
	// must never reach it.
	for _, local := range []string{os.Getenv("USER"), os.Getenv("USERNAME"), hostname(t)} {
		if len(local) >= 3 {
			require.NotContains(t, got, local, "the generated page holds a value of this machine")
		}
	}
	if *updateGolden {
		require.NoError(t, os.WriteFile(controlReferencePath, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(controlReferencePath)
	require.NoError(t, err, "run: go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden")
	assert.Equal(t, string(want), got,
		"the page differs from the command tree; run: go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden")
}

// hostname returns the host name of this machine, or "" when it is unknown.
func hostname(t *testing.T) string {
	t.Helper()
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}
