//go:build !windows

package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestPOSIXInitialCommandHonorsEnvironmentSelectedByLogin(t *testing.T) {
	t.Parallel()
	for _, shellName := range []string{"sh", "dash", "ash", "posh"} {
		t.Run(shellName, func(t *testing.T) {
			t.Parallel()
			shell, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s is unavailable", shellName)
			}
			for _, tc := range []struct {
				name     string
				profile  string
				trace    string
				variable string
			}{
				{
					name:     "the login profile replaces ENV with a path containing quotes",
					profile:  `ENV='${LEAPMUX_TEST_PROFILE_DIR}/selected"profile'; export ENV`,
					trace:    "login\nselected\n",
					variable: "login-selected",
				},
				{
					name:     "the login profile unsets ENV",
					profile:  "unset ENV",
					trace:    "login\n",
					variable: "login",
				},
				{
					name:     "the login profile selects a relative file",
					profile:  `ENV='selected"profile'; PATH="$LEAPMUX_TEST_PROFILE_DIR/shadow:$PATH"; export ENV PATH`,
					trace:    "login\nselected\n",
					variable: "login-selected",
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					wantTrace, wantValue := tc.trace, tc.variable
					if shellName == "posh" {
						wantTrace, wantValue = "login\n", "login"
					}
					home := t.TempDir()
					worker := filepath.Join(home, "Worker data $PATH 'quote'")
					require.NoError(t, os.Mkdir(worker, 0o700))
					trace := filepath.Join(home, "trace")
					initial := filepath.Join(home, "initial")
					next := filepath.Join(home, "next")
					inherited := filepath.Join(home, "inherited")
					selected := filepath.Join(home, `selected"profile`)
					shadow := filepath.Join(home, "shadow")
					require.NoError(t, os.Mkdir(shadow, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"), []byte(
						"printf 'login\\n' >> \"$LEAPMUX_TEST_PROFILE_TRACE\"\nleapmux_profile_value=login\n"+tc.profile+"\n",
					), 0o600))
					require.NoError(t, os.WriteFile(inherited, []byte("printf 'inherited\\n' >> \"$LEAPMUX_TEST_PROFILE_TRACE\"\n"), 0o600))
					require.NoError(t, os.WriteFile(selected, []byte(`printf 'selected\n' >> "$LEAPMUX_TEST_PROFILE_TRACE"
leapmux_profile_value="$leapmux_profile_value-selected"
`), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(shadow, filepath.Base(selected)), []byte(`printf 'shadow\n' >> "$LEAPMUX_TEST_PROFILE_TRACE"
leapmux_profile_value="$leapmux_profile_value-shadow"
`), 0o600))
					term, err := Start(testutil.DeadlineContext(t), Options{
						ID: "posix-profile", Shell: shell, WorkingDir: home, InitialCommandDir: worker,
						ExtraEnv: []string{
							"HOME=" + home, "ENV=" + inherited,
							"LEAPMUX_TEST_PROFILE_DIR=" + home, "LEAPMUX_TEST_PROFILE_TRACE=" + trace,
							"LEAPMUX_TEST_INITIAL=" + initial, "LEAPMUX_TEST_NEXT=" + next,
						},
						InitialCommand: `leapmux_startup_value="$leapmux_profile_value"
printf '%s|%s|%s' "$$" "$leapmux_startup_value" "$HOME" > "$LEAPMUX_TEST_INITIAL"`,
						ReleaseInitialCommand: func(_ context.Context, expose func() error) error {
							profiles, err := os.ReadFile(trace)
							require.NoError(t, err)
							assert.Equal(t, wantTrace, string(profiles))
							assert.NoFileExists(t, initial)
							return expose()
						},
					}, quartz.NewReal(), func(Output) {})
					if err != nil {
						profiles, _ := os.ReadFile(trace)
						t.Fatalf("the initial command did not start: %v. Profile trace %q", err, profiles)
					}
					t.Cleanup(func() {
						term.Stop()
						term.Wait()
					})
					want := fmt.Sprintf("%d|%s|%s", term.shellPID, wantValue, home)
					testutil.RequireEventually(t, func() bool {
						got, err := os.ReadFile(initial)
						return err == nil && string(got) == want
					}, "the initial command runs in the shell that loaded the profiles")
					require.NoError(t, term.SendInput([]byte(`printf '%s|%s|%s' "$$" "$leapmux_startup_value" "$HOME" > "$LEAPMUX_TEST_NEXT"`+"\r")))
					testutil.RequireEventually(t, func() bool {
						got, err := os.ReadFile(next)
						return err == nil && string(got) == want
					}, "the interactive prompt retains the initial command state")
				})
			}
		})
	}
}

func TestPOSIXInitialCommandCanExitAshAndPosh(t *testing.T) {
	t.Parallel()
	for _, shellName := range []string{"ash", "posh"} {
		t.Run(shellName, func(t *testing.T) {
			t.Parallel()
			shell, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s is unavailable", shellName)
			}
			home := t.TempDir()
			ctx := testutil.DeadlineContext(t)
			term, err := Start(ctx, Options{
				ID: "posix-exit", Shell: shell, WorkingDir: home, InitialCommandDir: home,
				ExtraEnv: []string{"HOME=" + home, "ENV="}, InitialCommand: "exit 19",
			}, quartz.NewReal(), func(Output) {})
			require.NoError(t, err)
			t.Cleanup(term.Stop)
			select {
			case <-term.exitCh:
				assert.Equal(t, 19, term.Wait())
			case <-ctx.Done():
				screen, _ := term.ScreenSnapshot()
				t.Fatalf("%s did not exit. PTY output %q", shellName, screen)
			}
		})
	}
}
