//go:build !windows

package terminal

import (
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

func TestYashInitialCommandKeepsLoginAndInteractiveState(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("yash")
	if err != nil {
		t.Skip("Yash is unavailable")
	}
	for _, tc := range []struct {
		name    string
		profile string
		rc      string
		posix   bool
	}{
		{"configured login and new interactive directory", "current config/yash/profile", "next config/yash/rc", false},
		{"home configuration fallback", ".config/yash/profile", ".config/yash/rc", false},
		{"home startup file", ".yash_profile", ".yashrc", false},
		{"default startup file", ".yash_profile", "load files/initialization/default", false},
		{"POSIX mode selected by the login profile", ".yash_profile", "mode rc", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			receipt := filepath.Join(dir, "receipt")
			writeStartup := func(relative, content string) {
				t.Helper()
				path := filepath.Join(dir, relative)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			login := `test -o login || exit 1
test -o interactive || exit 2
leapmux_test_login=login
leapmux_test_login_count=$((${leapmux_test_login_count:-0} + 1))
ENV=\${HOME}/mode\ rc
XDG_CONFIG_HOME="$HOME/next config"
YASH_LOADPATH="$HOME/load files"
`
			if tc.posix {
				login += "set -o posixly-correct\n"
			}
			writeStartup(tc.profile, login)
			writeStartup(tc.rc, `leapmux_test_rc_count=$((${leapmux_test_rc_count:-0} + 1))
leapmux_test_startup() {
  printf '%s:%s:%s' "$leapmux_test_login" "$leapmux_test_login_count" "$leapmux_test_rc_count"
}
`)
			term, err := Start(t.Context(), Options{
				ID: "yash-startup", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
				InitialCommand: `leapmux_test_result="$(leapmux_test_startup):$((6 * 7))"`,
				ExtraEnv: []string{"HOME=" + dir, "XDG_CONFIG_HOME=" + filepath.Join(dir, "current config"),
					"LEAPMUX_TEST_RECEIPT=" + receipt},
			}, quartz.NewReal(), func(Output) {})
			require.NoError(t, err)
			t.Cleanup(func() {
				term.Stop()
				term.Wait()
			})
			require.NoError(t, term.SendInput([]byte(`printf '%s:%s:%s' "$$" "$leapmux_test_result" "$ENV" > "$LEAPMUX_TEST_RECEIPT.pending" && mv "$LEAPMUX_TEST_RECEIPT.pending" "$LEAPMUX_TEST_RECEIPT"`+"\r")))
			testutil.RequireEventually(t, func() bool {
				_, err := os.Stat(receipt)
				return err == nil
			})
			result, err := os.ReadFile(receipt)
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("%d:login:1:1:42:${HOME}/mode rc", term.shellPID), string(result))
		})
	}
}

func TestYashInitialCommandWithoutStartupFiles(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("yash")
	if err != nil {
		t.Skip("Yash is unavailable")
	}
	dir := t.TempDir()
	assertInitialCommandExits(t, Options{
		ID: "yash-no-profile", Shell: shell, WorkingDir: dir, InitialCommandDir: dir,
		ExtraEnv: []string{
			"HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "YASH_LOADPATH=" + dir,
		},
		InitialCommand: "exit 19",
	}, 19, "Yash")
}
