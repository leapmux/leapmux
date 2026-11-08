package shellquote

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForOS(t *testing.T) {
	assert.Equal(t, PowerShell, ForOS("windows"))
	for _, goos := range []string{"darwin", "linux", "freebsd", ""} {
		assert.Equal(t, POSIX, ForOS(goos), goos)
	}
}

// Quote quotes every value, a literal one too, so a generated script never
// depends on which characters a shell reads literally.
func TestQuote(t *testing.T) {
	for _, tc := range []struct {
		value, posix, powerShell string
	}{
		{"hello", "'hello'", "'hello'"},
		{"", "''", "''"},
		{"it's", `'it'\''s'`, "'it''s'"},
		{"it\u2018s", "'it\u2018s'", "'it\u2018\u2018s'"},
		{"\u201ait\u201b", "'\u201ait\u201b'", "'\u201a\u201ait\u201b\u201b'"},
		{`C:\dir`, `'C:\dir'`, `'C:\dir'`},
	} {
		assert.Equal(t, tc.posix, POSIX.Quote(tc.value), tc.value)
		assert.Equal(t, tc.powerShell, PowerShell.Quote(tc.value), tc.value)
	}
}

func TestArg(t *testing.T) {
	for _, tc := range []struct {
		name, value, posix, powerShell string
	}{
		{"a plain path", "/home/alice/.config/leapmux/hub", "/home/alice/.config/leapmux/hub", "/home/alice/.config/leapmux/hub"},
		{"a flag", "--data-dir", "--data-dir", "--data-dir"},
		{"an empty value", "", "''", "''"},
		{"a space", "/srv/my data", "'/srv/my data'", "'/srv/my data'"},
		{"an apostrophe", "/srv/alice's data", `'/srv/alice'\''s data'`, "'/srv/alice''s data'"},
		{"a typographic quote", "/srv/alice\u2019s", "'/srv/alice\u2019s'", "'/srv/alice\u2019\u2019s'"},
		{"a Windows path", `C:\Users\alice\leapmux`, `'C:\Users\alice\leapmux'`, `C:\Users\alice\leapmux`},
		{"a dollar sign", "/srv/$HOME", "'/srv/$HOME'", "'/srv/$HOME'"},
		{"a comma", "a,b", "a,b", "'a,b'"},
		{"a non-ASCII letter", "/srv/données", "'/srv/données'", "'/srv/données'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.posix, POSIX.Arg(tc.value))
			assert.Equal(t, tc.powerShell, PowerShell.Arg(tc.value))
		})
	}
}

func TestCommand(t *testing.T) {
	assert.Equal(t, "leapmux hub start --data-dir '/srv/my data'",
		POSIX.Command("leapmux", "hub", "start", "--data-dir", "/srv/my data"))
	assert.Equal(t, "'/opt/my tools/leapmux' hub status",
		POSIX.Command("/opt/my tools/leapmux", "hub", "status"))
	assert.Equal(t, `C:\tools\leapmux.exe hub status`,
		PowerShell.Command(`C:\tools\leapmux.exe`, "hub", "status"),
		"an unquoted program runs without the call operator")
	assert.Equal(t, `& 'C:\Program Files\leapmux.exe' hub status --data-dir 'C:\my data'`,
		PowerShell.Command(`C:\Program Files\leapmux.exe`, "hub", "status", "--data-dir", `C:\my data`))
}

// A POSIX shell must read each rendered argument back as the original value.
func TestPOSIXArgRoundTripsThroughAShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX shell to read the arguments back")
	}
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	values := []string{"", "plain", "/srv/my data", "alice's", "'", `back\slash`, "$HOME", "`date`", "a\nb", "tab\there", "*", "~", "!", "données"}
	words := make([]string, len(values))
	for i, value := range values {
		words[i] = POSIX.Arg(value)
	}
	output, err := exec.Command(shell, "-c", `printf '%s\0' `+strings.Join(words, " ")).Output()
	require.NoError(t, err)
	got := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	assert.Equal(t, values, got)
}
