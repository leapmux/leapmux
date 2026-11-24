package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
			args:       []string{"daemon", "solo", "run", "-dev-frontend", "http://localhost:4328"},
			wantCode:   cli.ExitUsage,
			wantStderr: `hint: a long flag takes two dashes, as in "--version"`,
		},
		{
			name:       "a flag before the mode command states where the flag goes",
			args:       []string{"daemon", "solo", "--dev-frontend", "http://localhost:4328"},
			wantCode:   cli.ExitUsage,
			wantStderr: `hint: a flag goes after the command that takes it, as in "leapmux daemon solo <command> [flags]"`,
		},
		{
			name:       "the version flag belongs to the root alone",
			args:       []string{"daemon", "solo", "--version"},
			wantCode:   cli.ExitUsage,
			wantStderr: "unknown flag: --version",
		},
		{
			name:       "hub run resolves to the lifecycle command",
			args:       []string{"daemon", "hub", "run", "--help"},
			wantCode:   cli.ExitOK,
			wantStdout: lifecycleSummary("hub", "run") + "\n\nUsage:\n  leapmux daemon hub run [flags]\n",
		},
		{
			name:       "worker register checks its required flags before it runs",
			args:       []string{"daemon", "worker", "register", "--hub", "https://hub.example.com"},
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
			args:       []string{"recover", "db"},
			wantCode:   cli.ExitOK,
			wantStdout: "Database utilities\n\nUsage:\n  leapmux recover db <command>\n",
		},
		{
			name:       "unknown recover group is refused",
			args:       []string{"recover", "bogus"},
			wantCode:   cli.ExitUsage,
			wantStderr: `unknown command "bogus" for "leapmux recover"`,
		},
		{
			name:       "unknown recover group command is refused",
			args:       []string{"recover", "db", "bogus"},
			wantCode:   cli.ExitUsage,
			wantStderr: `unknown command "bogus" for "leapmux recover db"`,
		},
		{
			name:       "the old control prefix is gone",
			args:       []string{"control", "agent", "list"},
			wantCode:   cli.ExitUsage,
			wantStderr: `unknown command "control" for "leapmux"`,
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
	o := runTestCLI(t, "daemon", "hub")

	assert.Equal(t, cli.ExitOK, o.code)
	assert.Empty(t, o.stderr)
	assert.Contains(t, o.stdout, "Usage:\n  leapmux daemon hub <command>\n")
}

func TestRunCLISubcommandHelpReturnsSuccess(t *testing.T) {
	// The mode group's help prints its verbs without starting anything.
	o := runTestCLI(t, "daemon", "hub", "--help")

	assert.Equal(t, 0, o.code)
	assert.Contains(t, o.stdout, "run")
	assert.Contains(t, o.stdout, "enable")
}

// TestRunCLI_TopLevelAbbreviation verifies the top-level command dispatch
// resolves unambiguous prefixes.
func TestRunCLI_TopLevelAbbreviation(t *testing.T) {
	t.Run("abbreviated daemon solo resolves its run verb", func(t *testing.T) {
		o := runTestCLI(t, "da", "sol", "run", "--help")
		assert.Equal(t, 0, o.code, o.stderr)
		assert.Contains(t, o.stdout, "Usage:\n  leapmux daemon solo run [flags]\n")
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

	t.Run("recover supports structured --json output", func(t *testing.T) {
		dataDir := t.TempDir()
		o := runTestCLI(t, "recover", "db", "path", "--data-dir", dataDir, "--json")
		assert.Equal(t, 0, o.code, o.stderr)
		var env struct {
			Data struct {
				OK      bool   `json:"ok"`
				Command string `json:"command"`
				Message string `json:"message"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(o.stdout), &env), "stdout: %s", o.stdout)
		assert.True(t, env.Data.OK)
		assert.Equal(t, "leapmux recover db path", env.Data.Command)
		assert.Equal(t, filepath.Join(dataDir, "hub.db"), env.Data.Message)
	})

	t.Run("abbreviated guide dispatches", func(t *testing.T) {
		o := runTestCLI(t, "gu")
		assert.Equal(t, 0, o.code, "stderr: %s", o.stderr)
		assert.Contains(t, o.stdout, "leapmux agent")
	})

	t.Run("abbreviated daemon dev resolves its run verb", func(t *testing.T) {
		o := runTestCLI(t, "da", "d", "run", "--help")
		assert.Equal(t, 0, o.code, o.stderr)
		assert.Contains(t, o.stdout, "Usage:\n  leapmux daemon dev run [flags]\n")
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
		for _, name := range []string{"commands", "completion"} {
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
	"Start a local instance with \"leapmux up\" (or \"leapmux daemon solo start\").\n" +
	"Drive workspaces, agents, terminals, files, and layouts directly with\n" +
	"\"leapmux <command>\" (or the short \"lmx\" alias).\n" +
	"\n" +
	"Usage:\n" +
	"  leapmux <command>\n" +
	"\n" +
	"Daily Work:\n" +
	"  tree          Show a hierarchical overview of sections, workspaces, tiles, agents, terminals and files\n" +
	"  workspace     List, create, organize, export, apply, and delete workspaces\n" +
	"  section       List, create, rename, move, and delete sidebar sections\n" +
	"  tab           List, close, rename, move and reveal tabs of every type, and set their status\n" +
	"  agent         Start, message, watch and configure coding agents\n" +
	"  terminal      Open, type into, read and resize terminals\n" +
	"  events        Print what happens in your workspaces as it happens, one event per line\n" +
	"  notify        Show a notification in every LeapMux window that you have open\n" +
	"  notification  Send, list and clear recent Worker notifications\n" +
	"\n" +
	"Files & Git:\n" +
	"  file          List, read, search, glob, and open files on a worker\n" +
	"  git           Read and change the git state of a directory on a worker\n" +
	"  worktree      List, inspect, create, and remove git worktrees on a worker\n" +
	"\n" +
	"Layout & Windows:\n" +
	"  tile          Split, close and arrange the tiles of a workspace\n" +
	"  window        List, create, position, resize, and close floating windows\n" +
	"  layout        Read, replace and watch the whole tile tree of a workspace\n" +
	"\n" +
	"Fleet & Auth:\n" +
	"  whoami        Show the identity and tab context that this CLI acts as\n" +
	"  doctor        Check local daemons, Hub connectivity, auth, pins, and online Workers\n" +
	"  auth          Sign this CLI in to a hub, and manage its credentials\n" +
	"  context       Show, set, or clear persistent default hub, worker, workspace, and provider outside a tab\n" +
	"  settings      Read and change your account preferences on the hub\n" +
	"  worker        List workers, read what they report, and register new ones\n" +
	"  tunnel        Forward TCP ports or connect stdio to a host on a worker over E2EE\n" +
	"\n" +
	"Admin & Recovery:\n" +
	"  admin         Administer the hub; needs an administrator's login (offline repair: \"leapmux recover\")\n" +
	"  rpc           Call one procedure of the hub or of a Worker with a request in protobuf JSON\n" +
	"  recover       Offline break-glass recovery (opens the database directly)\n" +
	"\n" +
	"Meta:\n" +
	"  guide         Print the guide for an agent that drives LeapMux\n" +
	"  skill         Inspect and install LeapMux agent skills locally or on a Worker\n" +
	"  version       Print the version of this CLI, and of the hub and worker when connected\n" +
	"  commands      List commands; with --json, include arguments, flags and examples\n" +
	"  completion    Print the shell completion script\n" +
	"  help          Print the help of a command\n" +
	"\n" +
	"Local Daemon:\n" +
	"  daemon        Manage local LeapMux services (solo, hub, worker, dev)\n" +
	"  up            Start the local solo LeapMux daemon (shortcut for daemon solo start)\n" +
	"  down          Stop the local solo LeapMux daemon (shortcut for daemon solo stop)\n" +
	"\n" +
	"Flags:\n" +
	"      --color string   when to emit ANSI colors on TTY output (one of: auto, always, never; default: auto)\n" +
	"      --fields string  comma-separated columns or fields to print\n" +
	"      --format string  output format (one of: text, json, ndjson, tsv; default: text)\n" +
	"      --hub string     hub URL (default: tab socket, else $LEAPMUX_HUB, else context/default credential, else local solo/dev Hub)\n" +
	"      --json           print the result as one JSON envelope per line\n" +
	"      --no-header      omit the header row from table and TSV output\n" +
	"  -q, --quiet          print only the ids of the result, one per line\n" +
	"  -v, --version        print the version of this binary\n" +
	"  -w, --wide           do not truncate table columns\n" +
	"\n" +
	"Run \"leapmux <command> --help\" for the help of a command.\n" +
	"Any command name can be shortened as far as it stays unambiguous. A later\n" +
	"release can make an ad-hoc prefix ambiguous, so scripts use full names or\n" +
	"documented aliases (such as ag, term, ws, ls, rm, mv).\n"

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
			args := []string{cmd.Name()}
			if cmd.Name() == "up" || cmd.Name() == "down" {
				// up and down touch the host service manager when invoked without --help.
				args = append(args, "--help")
			}
			o := runTestCLI(t, args...)
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
		"elvish":     "set edit:completion:arg-completer[leapmux]",
		"nushell":    "export extern leapmux",
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
	assert.Contains(t, o.stderr, "bash, zsh, fish, powershell, elvish, nushell")
}

// The completion asks the binary for its candidates, so a prefix completes
// to the command names it can still become.
func TestCompletion_CompletesCommandNames(t *testing.T) {
	o := runTestCLI(t, "__complete", "wo")
	assert.Equal(t, 0, o.code, "stderr: %s", o.stderr)
	assert.Contains(t, o.stdout, "workspace")
	assert.Contains(t, o.stdout, "worktree")
	assert.Contains(t, o.stdout, "worker")
	assert.NotContains(t, o.stdout, "agent", "a candidate must start with the typed prefix")
}

func TestCompletionElvishUsesTheLiveResolver(t *testing.T) {
	generated := runTestCLI(t, "completion", "elvish")
	require.Equal(t, 0, generated.code, generated.stderr)
	assert.Contains(t, generated.stdout, "set edit:completion:arg-completer[leapmux]")
	assert.Contains(t, generated.stdout, "__complete")
	assert.Contains(t, generated.stdout, "edit:complex-candidate")

	completed := runTestCLI(t, "__complete", "completion", "el")
	require.Equal(t, 0, completed.code, completed.stderr)
	assert.Equal(t, "elvish\n:4\n", completed.stdout)
}

func TestCompletionListsGuideTopicIDsAndTitles(t *testing.T) {
	completed := runTestCLI(t, "__complete", "guide", "tar")
	require.Equal(t, cli.ExitOK, completed.code, completed.stderr)
	assert.Equal(t, "targets\tHow a command finds its object\n:4\n", completed.stdout)

	completed = runTestCLI(t, "__complete", "guide", "--topics", "")
	require.Equal(t, cli.ExitOK, completed.code, completed.stderr)
	assert.Equal(t, ":4\n", completed.stdout)
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
	for _, path := range []string{"daemon", "daemon solo", "up", "down", "recover", "agent send", "admin user get", "worktree list", "version", "completion"} {
		assert.Contains(t, byPath, path)
	}
	send := byPath["agent send"]
	assert.True(t, send.Mutates)
	require.Len(t, send.Args, 2)
	assert.Equal(t, "agent", send.Args[0].Name)
	assert.True(t, send.Args[0].Required)
	assert.NotEmpty(t, send.Args[0].Help, "a target argument states its forms")
	assert.NotEmpty(t, send.Examples)
	assert.NotContains(t, byPath, "help", "the help command is not part of the grammar")
}

// Each top-level control group alone prints the help of the group on stdout
// and exits 0: the caller asked what the group holds.
func TestControlGroups_PrintTheirHelp(t *testing.T) {
	o := runTestCLI(t, "admin")
	assert.Equal(t, 0, o.code)
	assert.Contains(t, o.stdout, "Usage:\n  leapmux admin <command>\n")
	assert.Contains(t, o.stdout, "\n  user ")
	assert.Empty(t, o.stderr)

	o = runTestCLI(t, "worker", "pins")
	assert.Equal(t, 0, o.code)
	for _, verb := range []string{"list", "get", "remove"} {
		assert.Contains(t, o.stdout, "\n  "+verb+" ")
	}
}

// A token that resolves to nothing under a group is refused with the path of
// the group, at every depth.
func TestControlGroups_RefuseAnUnknownCommand(t *testing.T) {
	o := runTestCLI(t, "worker", "xyz")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `unknown command "xyz" for "leapmux worker"`)

	o = runTestCLI(t, "admin", "xyz")
	assert.Equal(t, cli.ExitUsage, o.code)
	assert.Contains(t, o.stderr, `unknown command "xyz" for "leapmux admin"`)
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
	clitest.CheckTree(t, newRootCommand(testCLIRunners(t)), clitest.TreeRules{
		Targets:                targets,
		RequireCompleteTargets: true,
		RequireFlagGroupsAt:    8,
	})
}

// TestRootCommandTree_Snapshot verifies that every command in the full binary
// tree parses cleanly via cli.Describe without duplicate paths, empty summaries,
// or missing usage lines, and covers all expected top-level command groups.
func TestRootCommandTree_Snapshot(t *testing.T) {
	tree := cli.Describe(newRootCommand(testCLIRunners(t)))
	require.NotEmpty(t, tree.Commands)
	seen := map[string]bool{}
	topLevel := map[string]bool{}
	for _, c := range tree.Commands {
		assert.NotEmpty(t, c.Path)
		assert.False(t, seen[c.Path], "duplicate command path %q", c.Path)
		seen[c.Path] = true
		assert.NotEmpty(t, c.Summary, "missing summary on %q", c.Path)
		assert.NotEmpty(t, c.Usage, "missing usage on %q", c.Path)
		top, _, _ := strings.Cut(c.Path, " ")
		topLevel[top] = true
	}
	for _, wantTop := range []string{
		"tree", "agent", "terminal", "tab", "workspace", "section", "events",
		"notify", "notification", "file", "git", "worktree", "tile", "window",
		"layout", "worker", "tunnel", "whoami", "auth", "context", "settings",
		"doctor", "up", "down", "daemon", "admin", "recover", "rpc", "guide",
		"version", "commands", "completion",
	} {
		assert.True(t, topLevel[wantTop], "expected top-level command %q in binary tree", wantTop)
	}
	// Verify relative depth filtering on commands [prefix...] --depth.
	agentDepth1 := cli.DescribeFiltered(newRootCommand(testCLIRunners(t)), cli.DescribeOptions{
		Prefix: "agent",
		Depth:  1,
	})
	var paths []string
	for _, c := range agentDepth1.Commands {
		paths = append(paths, c.Path)
	}
	assert.Contains(t, paths, "agent")
	assert.Contains(t, paths, "agent list")
	assert.Contains(t, paths, "agent queue")
	assert.NotContains(t, paths, "agent queue list", "--depth 1 relative to prefix 'agent' must exclude depth-2 leaves")
}

var updateGolden = flag.Bool("update-golden", false, "rewrite the reference pages of the docs site that the command tree generates")

const (
	// controlReferenceDir holds the generated per-group control command
	// reference pages. The path is relative to this package's directory.
	controlReferenceDir = "../../../site/content/docs/reference/commands"
	// legacyControlReferencePath is the superseded single-page reference file.
	legacyControlReferencePath = "../../../site/content/docs/reference/control-commands.md"
)

const controlReferenceBanner = `<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->
`

type controlReferenceGroup struct {
	slug        string
	title       string
	description string
	icon        string
	subtitle    string
	weight      int
	commands    []string
}

var controlReferenceGroups = []controlReferenceGroup{
	{
		slug:        "agents",
		title:       "Agents",
		description: "Reference for leapmux agent commands: start, message, queue, watch, and configure coding agents.",
		icon:        "cube",
		subtitle:    "Start, message, queue, watch, and configure coding agents.",
		weight:      1,
		commands:    []string{"agent"},
	},
	{
		slug:        "terminals",
		title:       "Terminals",
		description: "Reference for leapmux terminal commands: open, type into, read, resize, and manage terminals.",
		icon:        "terminal",
		subtitle:    "Open, type into, read, resize, and manage shell terminals.",
		weight:      2,
		commands:    []string{"terminal"},
	},
	{
		slug:        "workspaces-and-tabs",
		title:       "Workspaces & Tabs",
		description: "Reference for leapmux workspace, section, tab, and tree commands.",
		icon:        "collection",
		subtitle:    "Manage workspaces, sidebar sections, tabs, and the tree overview.",
		weight:      3,
		commands:    []string{"workspace", "section", "tab", "tree"},
	},
	{
		slug:        "layout-and-windows",
		title:       "Layout & Windows",
		description: "Reference for leapmux tile, window, and layout commands.",
		icon:        "view-grid",
		subtitle:    "Split tiles, arrange layouts, and manage floating windows.",
		weight:      4,
		commands:    []string{"tile", "window", "layout"},
	},
	{
		slug:        "git-and-files",
		title:       "Git & Files",
		description: "Reference for leapmux git, worktree, and file commands.",
		icon:        "code",
		subtitle:    "Inspect git state, manage worktrees, and browse or search files on a Worker.",
		weight:      5,
		commands:    []string{"git", "worktree", "file"},
	},
	{
		slug:        "workers-and-events",
		title:       "Workers & Events",
		description: "Reference for leapmux worker, tunnel, events, notify, and notification commands.",
		icon:        "chip",
		subtitle:    "Manage Workers, TCP/stdio tunnels, live event streams, and notifications.",
		weight:      6,
		commands:    []string{"worker", "tunnel", "events", "notify", "notification"},
	},
	{
		slug:        "auth-and-session",
		title:       "Auth & Session",
		description: "Reference for leapmux auth, context, settings, whoami, doctor, skill, guide, version, and rpc commands.",
		icon:        "key",
		subtitle:    "Sign in, set CLI context and preferences, run diagnostics, inspect skills, and call RPCs.",
		weight:      7,
		commands:    []string{"auth", "context", "settings", "whoami", "doctor", "skill", "guide", "version", "rpc"},
	},
	{
		slug:        "admin",
		title:       "Admin",
		description: "Reference for leapmux admin commands: administer Hub users, sessions, settings, and tokens.",
		icon:        "shield-check",
		subtitle:    "Administer Hub users, sessions, settings, OAuth apps, and Workers.",
		weight:      8,
		commands:    []string{"admin"},
	},
}

func renderControlReferenceIndex(globalFlags []cli.FlagInfo, hasPartialGlobal bool) (string, error) {
	var out bytes.Buffer
	out.WriteString(`---
title: "Control Command Reference"
description: "Every leapmux control command with its arguments, flags, and examples, generated from the command tree of the binary."
type: docs
weight: 2
sidebar:
  open: false
---

`)
	out.WriteString(controlReferenceBanner)
	out.WriteString("\nThis section lists every control command of `leapmux` with its arguments, flags, and examples, generated directly from the binary. The binary prints the same help with `--help`, and `leapmux commands <prefix> --json` prints it for a program. See [CLI Guide](/docs/using/cli-guide/) for targeting rules, output formats, exit codes, and authentication.\n\n")
	out.WriteString(`## Common tasks

| Task | Command |
|---|---|
| Open an agent in a new worktree | ` + "`leapmux agent open --provider claude --worktree feat/name --base main --dir .`" + ` |
| Send a prompt and wait for the reply | ` + "`leapmux agent send <agent> \"...\" --wait`" + ` |
| Request structured JSON from an agent | ` + "`leapmux agent send <agent> \"...\" --wait --output-schema schema.json`" + ` |
| Wait for any of several agents to finish | ` + "`leapmux agent wait <a1> <a2> --any`" + ` |
| Answer a pending permission or question prompt | ` + "`leapmux agent request answer <agent> <request-id> ...`" + ` |
| Run a command in a terminal and wait for exit | ` + "`leapmux terminal open --dir . --command \"make test; exit\" --wait --exit`" + ` |
| Inspect git status and diffs on a Worker | ` + "`leapmux git status --dir .`" + ` |
| Stream workspace and agent events with checkpoints | ` + "`leapmux events --checkpoint <name>`" + ` |
| Forward a TCP port to a remote Worker | ` + "`leapmux tunnel forward 8888:127.0.0.1:8888 --worker name:<worker>`" + ` |
| Rotate an OAuth/OIDC provider client secret | ` + "`leapmux admin idp rotate-secret <idp> --client-secret <secret>`" + ` |

`)
	if err := cli.WriteSharedFlags(&out, globalFlags, hasPartialGlobal); err != nil {
		return "", err
	}
	out.WriteString("## Command groups\n\n{{< cards >}}\n")
	for _, g := range controlReferenceGroups {
		fmt.Fprintf(&out, "  {{< card link=\"/docs/reference/commands/%s/\" title=%q icon=%q subtitle=%q >}}\n",
			g.slug, g.title, g.icon, g.subtitle)
	}
	out.WriteString("{{< /cards >}}\n")
	return out.String(), nil
}

func renderControlReferenceGroup(tree cli.CommandTree, g controlReferenceGroup, globalFlags []cli.FlagInfo) (string, error) {
	var body bytes.Buffer
	if err := cli.WriteReferenceForGroups(&body, tree, g.commands, globalFlags); err != nil {
		return "", err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "---\ntitle: %q\ndescription: %q\ntype: docs\nweight: %d\n---\n\n",
		g.title, g.description, g.weight)
	out.WriteString(controlReferenceBanner)
	if g.slug == "auth-and-session" {
		out.WriteString("\nOnline commands on this page also take the [flags of every command](/docs/reference/commands/#flags-of-every-command); offline commands (`guide`, `context show`, `context clear`) do not contact a Hub and reject `--hub`. See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.\n\n")
	} else {
		out.WriteString("\nEvery command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.\n\n")
	}
	out.WriteString(strings.TrimRight(body.String(), "\n"))
	out.WriteString("\n")
	return out.String(), nil
}

// TestControlReference_IsCurrent keeps the generated per-group command
// reference pages equal to the command tree. The pages are also the reviewed
// record of the grammar: a change to a command shows in their diff.
func TestControlReference_IsCurrent(t *testing.T) {
	exclude := map[string]bool{
		"daemon":     true,
		"up":         true,
		"down":       true,
		"recover":    true,
		"commands":   true,
		"completion": true,
		"help":       true,
	}
	tree := cli.Describe(newRootCommand(testCLIRunners(t)))

	// Verify every non-excluded top-level command in the binary belongs to
	// exactly one group in controlReferenceGroups.
	nonExcluded := map[string]bool{}
	for _, c := range tree.Commands {
		top, _, _ := strings.Cut(c.Path, " ")
		if !exclude[top] {
			nonExcluded[top] = true
		}
	}
	assigned := map[string]string{}
	seenSlugs := map[string]bool{}
	for _, g := range controlReferenceGroups {
		require.NotEmpty(t, g.slug)
		require.False(t, seenSlugs[g.slug], "duplicate controlReferenceGroups slug %q", g.slug)
		seenSlugs[g.slug] = true
		for _, cmd := range g.commands {
			require.False(t, exclude[cmd], "group %q lists excluded top-level command %q", g.slug, cmd)
			require.True(t, nonExcluded[cmd], "group %q lists unknown top-level command %q", g.slug, cmd)
			prev, dup := assigned[cmd]
			require.False(t, dup, "top-level command %q is assigned to both %q and %q", cmd, prev, g.slug)
			assigned[cmd] = g.slug
		}
	}
	for top := range nonExcluded {
		require.Contains(t, assigned, top, "top-level command %q is not assigned to any group in controlReferenceGroups", top)
	}

	globalFlags, hasPartialGlobal := cli.SharedGlobalFlags(tree, exclude)

	type generatedFile struct {
		name    string
		content string
	}
	indexContent, err := renderControlReferenceIndex(globalFlags, hasPartialGlobal)
	require.NoError(t, err)
	files := []generatedFile{{name: "_index.md", content: indexContent}}
	expectedNames := map[string]bool{"_index.md": true}
	for _, g := range controlReferenceGroups {
		content, err := renderControlReferenceGroup(tree, g, globalFlags)
		require.NoError(t, err)
		name := g.slug + ".md"
		files = append(files, generatedFile{name: name, content: content})
		expectedNames[name] = true
	}

	// The pages are published, so a value that a flag reads from this machine
	// must never reach any of them.
	localValues := []string{os.Getenv("USER"), os.Getenv("USERNAME"), hostname(t)}
	for _, f := range files {
		for _, local := range localValues {
			if len(local) >= 3 {
				require.NotContains(t, f.content, local, "generated page %s holds a value of this machine", f.name)
			}
		}
	}

	if *updateGolden {
		require.NoError(t, os.MkdirAll(controlReferenceDir, 0o755))
		entries, err := os.ReadDir(controlReferenceDir)
		require.NoError(t, err)
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") && !expectedNames[entry.Name()] {
				require.NoError(t, os.Remove(filepath.Join(controlReferenceDir, entry.Name())))
			}
		}
		for _, f := range files {
			require.NoError(t, os.WriteFile(filepath.Join(controlReferenceDir, f.name), []byte(f.content), 0o644))
		}
		if err := os.Remove(legacyControlReferencePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
		return
	}

	_, err = os.Stat(legacyControlReferencePath)
	assert.ErrorIs(t, err, os.ErrNotExist,
		"legacy single-page reference %s still exists; run: go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden",
		legacyControlReferencePath)

	entries, err := os.ReadDir(controlReferenceDir)
	require.NoError(t, err, "run: go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden")
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			assert.True(t, expectedNames[entry.Name()],
				"stale reference file %q in %s; run: go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden",
				entry.Name(), controlReferenceDir)
		}
	}
	for _, f := range files {
		want, err := os.ReadFile(filepath.Join(controlReferenceDir, f.name))
		require.NoError(t, err, "missing %s; run: go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden", f.name)
		assert.Equal(t, string(want), f.content,
			"%s differs from the command tree; run: go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden", f.name)
	}
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
