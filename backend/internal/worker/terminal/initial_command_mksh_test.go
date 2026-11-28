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

func TestMkshInitialCommandPreservesNativeProfileSelection(t *testing.T) {
	t.Setenv("ENV", "")
	require.NoError(t, os.Unsetenv("ENV"))
	for _, shellName := range []string{"mksh", "ksh"} {
		t.Run(shellName, func(t *testing.T) {
			shell, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s is unavailable", shellName)
			}
			version, err := exec.CommandContext(t.Context(), shell, "-c", `printf '%s' "${KSH_VERSION-}"`).Output()
			require.NoError(t, err)
			if !strings.Contains(string(version), "MIRBSD KSH ") {
				t.Skipf("%s does not select mksh", shellName)
			}
			for _, tc := range []struct {
				name      string
				profile   string
				trace     string
				retained  string
				inherited bool
			}{
				{name: "default profile", trace: "login\ndefault\n", retained: "login-default"},
				{name: "unset environment", profile: "unset ENV", trace: "login\ndefault\n", retained: "login-default", inherited: true},
				{name: "empty environment", profile: "ENV=; export ENV", trace: "login\ndefault\n", retained: "login-default", inherited: true},
				{name: "replacement path", profile: `ENV="$HOME/selected rc"; export ENV`, trace: "login\nselected\n", retained: "login-selected", inherited: true},
				{name: "relative path", profile: `ENV='selected rc'; export ENV`, trace: "login\nselected\n", retained: "login-selected"},
				{name: "literal variable", profile: `ENV='$HOME/selected rc'; export ENV`, trace: "login\n", retained: "login"},
				{name: "literal tilde", profile: `ENV='~/selected rc'; export ENV`, trace: "login\n", retained: "login"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					home := filepath.Join(root, "home with spaces")
					worker := filepath.Join(root, "worker $HOME 'quote'")
					require.NoError(t, os.Mkdir(home, 0o700))
					require.NoError(t, os.Mkdir(worker, 0o700))
					trace, initial, next := filepath.Join(root, "trace"), filepath.Join(root, "initial"), filepath.Join(root, "next")
					for filename, content := range map[string]string{
						".profile": `printf 'login\n' >> "$LEAPMUX_TEST_TRACE"
lm_profile=login
lm_profile_value() { printf '%s' "$lm_profile"; }
` + tc.profile + "\n",
						".mkshrc": `printf 'default\n' >> "$LEAPMUX_TEST_TRACE"
lm_profile="$lm_profile-default"
`,
						"selected rc": `printf 'selected\n' >> "$LEAPMUX_TEST_TRACE"
lm_profile="$lm_profile-selected"
`,
						"inherited rc": `printf 'inherited\n' >> "$LEAPMUX_TEST_TRACE"
`,
					} {
						require.NoError(t, os.WriteFile(filepath.Join(home, filename), []byte(content), 0o600))
					}
					env := []string{"HOME=" + home, "LEAPMUX_TEST_TRACE=" + trace, "LEAPMUX_TEST_INITIAL=" + initial, "LEAPMUX_TEST_NEXT=" + next}
					if tc.inherited {
						env = append(env, "ENV="+filepath.Join(home, "inherited rc"))
					}
					term, err := Start(testutil.DeadlineContext(t), Options{
						ID: "mksh-startup", Shell: shell, WorkingDir: home, InitialCommandDir: worker, ExtraEnv: env,
						InitialCommand: `lm_retained=$(lm_profile_value)
printf 'MKSH_VALUE_%s\n' "$lm_retained"
printf '%s|%s' "$$" "$lm_retained" > "$LEAPMUX_TEST_INITIAL"`,
						ReleaseInitialCommand: func(_ context.Context, expose func() error) error {
							content, err := os.ReadFile(trace)
							require.NoError(t, err)
							assert.Equal(t, tc.trace, string(content))
							assert.NoFileExists(t, initial)
							return expose()
						},
					}, quartz.NewReal(), func(Output) {})
					if err != nil {
						profiles, _ := os.ReadFile(trace)
						t.Fatalf("mksh did not start the command: %v. Profile trace %q", err, profiles)
					}
					t.Cleanup(func() {
						term.Stop()
						term.Wait()
					})
					want := fmt.Sprintf("%d|%s", term.shellPID, tc.retained)
					testutil.RequireEventually(t, func() bool {
						data, err := os.ReadFile(initial)
						screen, _ := term.ScreenSnapshot()
						return err == nil && string(data) == want && strings.Contains(string(screen), "MKSH_VALUE_"+tc.retained)
					}, "the original shell runs the command after its profiles")
					require.NoError(t, term.SendInput([]byte(`printf '%s|%s' "$$" "$lm_retained" > "$LEAPMUX_TEST_NEXT"`+"\r")))
					testutil.RequireEventually(t, func() bool {
						data, err := os.ReadFile(next)
						return err == nil && string(data) == want
					}, "the next prompt keeps the command state")
				})
			}
		})
	}
}
