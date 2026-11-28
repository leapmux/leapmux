//go:build !windows

package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

var cshTestExecutables = []string{"csh", "tcsh", "bsd-csh"}

func requireCshExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is unavailable", name)
	}
	return path
}

func TestCshInitialCommandPreservesTheLoginShell(t *testing.T) {
	t.Parallel()
	for _, name := range cshTestExecutables {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireCshExecutable(t, name)
			native, err := exec.CommandContext(t.Context(), shell, "-f", "-c", "echo $?tcsh").Output()
			require.NoError(t, err)
			profileKind := "cshrc"
			if strings.TrimSpace(string(native)) == "1" {
				profileKind = "tcshrc"
			}
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			home := filepath.Join(root, "home with spaces")
			worker := filepath.Join(root, "worker [!] space 'quote'")
			require.NoError(t, os.Mkdir(home, 0o700))
			require.NoError(t, os.Mkdir(worker, 0o700))
			const profileState = "alias leapmux_initial_command 'echo profile-alias'\nset history = 32\n"
			require.NoError(t, os.WriteFile(filepath.Join(home, ".cshrc"), []byte("set profile_kind = cshrc\n"+profileState), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(home, ".tcshrc"), []byte("set profile_kind = tcshrc\nalias sched 'echo unexpected sched alias'\n"+profileState), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(home, ".history"), []byte("echo original-csh-history\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(home, ".login"), []byte(`set login_value = loaded
alias source 'echo unexpected source alias'
history >! "$LM_TEST_HISTORY"
`), 0o600))
			first := filepath.Join(root, "first")
			next := filepath.Join(root, "next")
			history := filepath.Join(root, "history")
			term, err := Start(t.Context(), Options{
				ID: "csh-initial", Shell: shell, WorkingDir: root, InitialCommandDir: worker,
				ExtraEnv: []string{"HOME=" + home, "LM_TEST_FIRST=" + first, "LM_TEST_NEXT=" + next, "LM_TEST_HISTORY=" + history},
				InitialCommand: `if ( $?LEAPMUX_INIT_CSH_STARTUP || $?LEAPMUX_INIT_CSH_HOME_ENV || $?LEAPMUX_INIT_FILE ) exit 89
set retained_value = "initial command state"
alias retained_alias 'echo retained-alias'
echo "$$|$profile_kind|$login_value|$home|$HOME|$cwd" >! "$LM_TEST_FIRST"
`,
			}, quartz.NewReal(), func(Output) {})
			require.NoError(t, err)
			t.Cleanup(func() {
				term.Stop()
				term.Wait()
			})
			want := fmt.Sprintf("%d|%s|loaded|%s|%s|%s", term.shellPID, profileKind, home, home, root)
			testutil.RequireEventually(t, func() bool {
				got, err := os.ReadFile(first)
				return err == nil && strings.TrimSpace(string(got)) == want
			}, "the initial command sees both login profiles in the original shell")
			historyText, err := os.ReadFile(history)
			require.NoError(t, err)
			assert.Contains(t, string(historyText), "echo original-csh-history")
			require.NoError(t, term.SendInput([]byte(`echo "$$|$retained_value" >! "$LM_TEST_NEXT"; retained_alias >> "$LM_TEST_NEXT"; leapmux_initial_command >> "$LM_TEST_NEXT"`+"\r")))
			wantNext := fmt.Sprintf("%d|initial command state\nretained-alias\nprofile-alias\n", term.shellPID)
			testutil.RequireEventually(t, func() bool {
				got, err := os.ReadFile(next)
				return err == nil && string(got) == wantNext
			}, "the next command sees the initial command state")
			entries, err := os.ReadDir(filepath.Join(worker, initialCommandLaunchRoot))
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestCshInitialCommandCanReplaceTheShellAndExit(t *testing.T) {
	t.Parallel()
	for _, name := range cshTestExecutables {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireCshExecutable(t, name)
			dir := t.TempDir()
			assertInitialCommandExits(t, Options{
				ID: "csh-exit", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
				ExtraEnv:       []string{"HOME=" + dir},
				InitialCommand: `exec /bin/sh -c 'exit 19'`,
			}, 19, name)
		})
	}
}

func TestCshInitialCommandRefusalKeepsThePayloadPrivate(t *testing.T) {
	t.Parallel()
	for _, name := range cshTestExecutables {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			shell := requireCshExecutable(t, name)
			dir := t.TempDir()
			effect := filepath.Join(dir, "effect")
			assertInitialCommandRefusalKeepsPrivate(t, Options{
				ID: "csh-refused", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
				ExtraEnv:       []string{"HOME=" + dir, "LM_TEST_EFFECT=" + effect},
				InitialCommand: `echo exposed >! "$LM_TEST_EFFECT"`,
			}, effect, nil)
		})
	}
}
