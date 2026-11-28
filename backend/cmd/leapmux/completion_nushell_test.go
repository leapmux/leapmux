package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestCompletionNushellUsesTheNativeCompleter(t *testing.T) {
	nu, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("Nushell is unavailable")
	}
	version, err := exec.CommandContext(testutil.DeadlineContext(t), nu, "-n", "-c", "version | get version").Output()
	require.NoError(t, err)
	if semver.Compare("v"+strings.TrimSpace(string(version)), "v0.116.0") < 0 {
		t.Skipf("Nushell %s predates the completion context API", strings.TrimSpace(string(version)))
	}
	generated := runTestCLI(t, "completion", "nushell")
	require.Zero(t, generated.code, generated.stderr)
	assert.Empty(t, generated.stderr)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "leapmux-completion.nu"), []byte(generated.stdout), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "not-a-candidate.txt"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spaced file.txt"), nil, 0o600))
	current, err := os.Executable()
	require.NoError(t, err)
	for _, base := range []string{"leapmux", "lmx"} {
		filename := base
		if runtime.GOOS == "windows" {
			filename += ".exe"
		}
		target := filepath.Join(dir, filename)
		if err := os.Link(current, target); err != nil {
			if symErr := os.Symlink(current, target); symErr != nil {
				data, readErr := os.ReadFile(current)
				require.NoError(t, readErr)
				testutil.WriteExecutable(t, target, string(data))
			}
		}
	}

	const token = "nushell-completion-test"
	workspaces, err := proto.Marshal(&leapmuxv1.ListWorkspacesResponse{Workspaces: []*leapmuxv1.Workspace{
		{Id: "workspace-1", Title: "Live workspace"},
		{Id: "other-1", Title: "Other workspace"},
	}})
	require.NoError(t, err)
	hub := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		assert.Equal(t, leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, r.URL.Path)
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(workspaces)
	}))
	configDir := filepath.Join(dir, "config")
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", configDir)
	require.NoError(t, control.SaveCredentials(hub.URL, control.CredentialFile{
		HubURL: hub.URL, UserID: "test-owner", AccessToken: token, ExpiresAt: time.Now().Add(time.Hour),
	}))
	command := exec.CommandContext(testutil.DeadlineContext(t), nu, "-n", "-c", `
$env.config.completions.external.completer = {|place| [previous-completer] }
use leapmux-completion.nu *
alias lm = leapmux
let live = $'leapmux tab list --hub ($env.LEAPMUX_TEST_HUB) "--workspace=work'
let raw_live = $"leapmux tab list --hub ($env.LEAPMUX_TEST_HUB) r#'--workspace=work"
let value_quoted_live = $'leapmux tab list --hub ($env.LEAPMUX_TEST_HUB) --workspace="work'
let outer_quoted_literal = $"leapmux tab list --hub ($env.LEAPMUX_TEST_HUB) \"--workspace='work"
{
    shell: ('leapmux completion nu' | commandline complete)
    lmx_shell: ('lmx completion nu' | commandline complete)
    commands: ('lm wo' | commandline complete)
    descriptions: ('leapmux guide tar' | commandline complete --detailed | select value description)
    flag: ('leapmux agent wait --unt' | commandline complete)
    enum: ('leapmux agent wait --until=wait' | commandline complete)
    live: ($live | commandline complete --detailed | select value description)
    raw_live: ($raw_live | commandline complete --detailed | select value description)
    value_quoted_live: ($value_quoted_live | commandline complete --detailed | select value description)
    outer_quoted_literal: ($outer_quoted_literal | commandline complete)
    attachment: ('leapmux agent send --file not-a' | commandline complete)
    inline_attachment: ('leapmux agent send --file=spa' | commandline complete)
    files: ('leapmux completion not-a' | commandline complete)
    previous: ('some-other-external arg' | commandline complete)
    help: (leapmux --help | lines | first)
} | to json --raw`)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LEAPMUX_TEST_HUB="+hub.URL,
		runMainEnv+"=1",
		// Each completion starts the test binary. Skip the race runtime's
		// one-second exit delay on those short-lived subprocesses.
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"),
		"LEAPMUX_HUB=", controlenv.CLIVariable+"=", controlenv.SocketVariable+"=", controlenv.TokenVariable+"=",
	)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	type candidate struct{ Value, Description string }
	var result struct {
		Shell, Commands, Flag, Enum, Files, Previous, Attachment []string
		LmxShell                                                 []string `json:"lmx_shell"`
		InlineAttachment                                         []string `json:"inline_attachment"`
		OuterQuotedLiteral                                       []string `json:"outer_quoted_literal"`
		Descriptions, Live                                       []candidate
		RawLive                                                  []candidate `json:"raw_live"`
		ValueQuotedLive                                          []candidate `json:"value_quoted_live"`
		Help                                                     string
	}
	require.NoError(t, json.Unmarshal(output, &result), "%s", output)
	assert.Equal(t, []string{"nushell"}, result.Shell)
	assert.Equal(t, []string{"nushell"}, result.LmxShell)
	assert.ElementsMatch(t, []string{"worker", "workspace", "worktree"}, result.Commands)
	assert.Equal(t, []candidate{{"targets", "How a command finds its object"}}, result.Descriptions)
	assert.Contains(t, result.Flag, "--until")
	assert.Equal(t, []string{"--until=waiting_for_user"}, result.Enum)
	assert.Equal(t, []candidate{{"--workspace=workspace-1", "Live workspace"}}, result.Live)
	assert.Equal(t, []candidate{{"--workspace=workspace-1", "Live workspace"}}, result.RawLive)
	assert.Equal(t, []candidate{{"--workspace=workspace-1", "Live workspace"}}, result.ValueQuotedLive)
	assert.Empty(t, result.OuterQuotedLiteral, "outer-quoted token must not double-unquote a literal single quote after =")
	assert.Equal(t, []string{"not-a-candidate.txt"}, result.Attachment)
	assert.Equal(t, []string{`"--file=spaced file.txt"`}, result.InlineAttachment)
	assert.Empty(t, result.Files)
	assert.Equal(t, []string{"previous-completer"}, result.Previous)
	assert.Equal(t, strings.SplitN(rootHelpGolden, "\n", 2)[0], result.Help)
}
