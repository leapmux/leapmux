//go:build !windows

package terminal

import (
	"context"
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

func TestKsh93InitialCommandKeepsTheLoginAndInteractiveState(t *testing.T) {
	// KornShell seeds ENV with its default profile only when it is absent.
	t.Setenv("ENV", "")
	require.NoError(t, os.Unsetenv("ENV"))
	for _, shellName := range []string{"ksh", "ksh93"} {
		t.Run(shellName, func(t *testing.T) {
			shell, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s is unavailable", shellName)
			}
			version, err := exec.CommandContext(t.Context(), shell, "-c", `printf '%s\n' "${.sh.version}"`).Output()
			if err != nil || !strings.Contains(string(version), "93") {
				t.Skipf("%s does not run KornShell 93", shellName)
			}
			for _, tc := range []struct {
				name              string
				profile           string
				trace             string
				variable          string
				skipSystemProfile bool
			}{
				{name: "default profile", trace: "login\ndefault\n", variable: "login-default"},
				{
					name:     "replacement profile",
					profile:  `ENV='$HOME/selected"profile'; export ENV`,
					trace:    "login\nselected\n",
					variable: "login-selected",
				},
				{
					name:     "tilde profile",
					profile:  `ENV='~/selected"profile'; export ENV`,
					trace:    "login\nselected\n",
					variable: "login-selected",
				},
				{
					name:              "profile skips the system file",
					profile:           `ENV='/./${HOME#/}/selected"profile'; export ENV`,
					trace:             "login\nselected\n",
					variable:          "login-selected",
					skipSystemProfile: true,
				},
				{name: "unset profile", profile: "unset ENV", trace: "login\n", variable: "login"},
				{name: "empty profile", profile: "ENV=; export ENV", trace: "login\n", variable: "login"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					home := t.TempDir()
					worker := filepath.Join(home, "worker $HOME 'quote'")
					require.NoError(t, os.Mkdir(worker, 0o700))
					trace := filepath.Join(home, "trace")
					initial := filepath.Join(home, "initial")
					next := filepath.Join(home, "next")
					require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"), []byte(`printf 'login\n' >> "$LEAPMUX_TEST_TRACE"
lm_profile=login
function lm_ksh_value { printf '%s' "$lm_profile"; }
`+tc.profile+"\n"), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(home, ".kshrc"), []byte(`printf 'default\n' >> "$LEAPMUX_TEST_TRACE"
lm_profile="$lm_profile-default"
`), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(home, `selected"profile`), []byte(`printf 'selected\n' >> "$LEAPMUX_TEST_TRACE"
lm_profile="$lm_profile-selected"
`), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(home, "shadow"), []byte("printf 'unexpected replacement\n' >> \"$LEAPMUX_TEST_TRACE\"\n"), 0o600))
					wantTrace := tc.trace
					if os.Getenv("LEAPMUX_TEST_KSH_SYSTEM_PROFILE") == "1" && !tc.skipSystemProfile {
						wantTrace = strings.Replace(wantTrace, "login\n", "login\nsystem:login\n", 1)
					}
					term, err := Start(testutil.DeadlineContext(t), Options{
						ID: "ksh-initial", Shell: shell, WorkingDir: home, InitialCommandDir: worker,
						ExtraEnv: []string{"HOME=" + home, "LEAPMUX_TEST_TRACE=" + trace, "LEAPMUX_TEST_INITIAL=" + initial, "LEAPMUX_TEST_NEXT=" + next},
						InitialCommand: `lm_value=$(lm_ksh_value)
printf '%s|%s' "$$" "$lm_value" > "$LEAPMUX_TEST_INITIAL"
printf 'KSH_INITIAL_%s\n' "$lm_value"`,
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
						t.Fatalf("KornShell did not start the initial command: %v. Profile trace %q", err, profiles)
					}
					t.Cleanup(func() {
						term.Stop()
						term.Wait()
					})
					want := fmt.Sprintf("%d|%s", term.shellPID, tc.variable)
					testutil.RequireEventually(t, func() bool {
						got, err := os.ReadFile(initial)
						return err == nil && string(got) == want
					}, "the startup command sees the login function and selected environment")
					testutil.RequireEventually(t, func() bool {
						screen, _ := term.ScreenSnapshot()
						return strings.Contains(string(screen), "KSH_INITIAL_"+tc.variable)
					}, "the shell sends startup output to the terminal")
					require.NoError(t, term.SendInput([]byte(`printf '%s|%s' "$$" "$lm_value" > "$LEAPMUX_TEST_NEXT"`+"\r")))
					testutil.RequireEventually(t, func() bool {
						got, err := os.ReadFile(next)
						return err == nil && string(got) == want
					}, "the next prompt retains the startup command state")
				})
			}
		})
	}
}

func TestKsh93InitialCommandRefusesAnotherShellUsingTheKshName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	shell := filepath.Join(dir, "ksh")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nexec /bin/sh \"$@\"\n")
	term, err := Start(testutil.DeadlineContext(t), Options{
		ID: "other-ksh", Shell: shell, WorkingDir: dir, InitialCommandDir: dir, InitialCommand: "exit 19",
	}, quartz.NewReal(), func(Output) {})
	require.ErrorContains(t, err, "must run KornShell 93 or mksh for an initial command")
	assert.Nil(t, term)
	entries, err := os.ReadDir(filepath.Join(dir, initialCommandLaunchRoot))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestKornShellStartupCachesProbeByExecutableIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	shell := filepath.Join(dir, "ksh")
	testutil.WriteExecutable(t, shell, fmt.Sprintf("#!/bin/sh\nprintf 'x\\n' >> %q\nprintf 'Version AJM 93u+ 2012-08-01'\n", counter))

	bootstrap, err := newInitialCommandBootstrap(dir)
	require.NoError(t, err)
	t.Cleanup(bootstrap.close)

	env1, err := bootstrap.kornShellStartup(t.Context(), shell, nil)
	require.NoError(t, err)
	assert.Contains(t, env1, "ENV="+kshEnvironmentSelector)
	env2, err := bootstrap.kornShellStartup(t.Context(), shell, nil)
	require.NoError(t, err)
	assert.Contains(t, env2, "ENV="+kshEnvironmentSelector)

	raw, err := os.ReadFile(counter)
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(raw)), "\n"), 1, "repeated launches of the same ksh binary must reuse the cached probe")

	// Replacing the executable with a different size/content must invalidate the cache.
	testutil.WriteExecutable(t, shell, fmt.Sprintf("#!/bin/sh\n# replaced executable with mksh engine\nprintf 'x\\n' >> %q\nprintf 'MIRBSD KSH R59 2020/10/31'\n", counter))
	env3, err := bootstrap.kornShellStartup(t.Context(), shell, nil)
	require.NoError(t, err)
	assert.Contains(t, env3, "ENV="+filepath.Join(bootstrap.dir, "mksh-interactive"))
	raw, err = os.ReadFile(counter)
	require.NoError(t, err)
	assert.Len(t, strings.Split(strings.TrimSpace(string(raw)), "\n"), 2, "replaced executable must be probed again")

	// Failing probes must not be cached.
	failing := filepath.Join(dir, "ksh-fail")
	testutil.WriteExecutable(t, failing, "#!/bin/sh\nexit 1\n")
	_, err = bootstrap.kornShellStartup(t.Context(), failing, nil)
	require.Error(t, err)
	testutil.WriteExecutable(t, failing, "#!/bin/sh\nprintf 'MIRBSD KSH R59 2020/10/31'\n")
	_, err = bootstrap.kornShellStartup(t.Context(), failing, nil)
	require.NoError(t, err)
}
