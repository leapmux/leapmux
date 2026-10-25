package envprobe

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCapturesOnlyRequestedNativeValues(t *testing.T) {
	t.Setenv("LEAPMUX_PROBE_VALUE", "日本語=é\nline\r\n%$'")
	t.Setenv("LEAPMUX_PROBE_SECRET", "not requested")
	t.Setenv("LEAPMUX_PROBE_PATH", " \t./child/../file \t")
	t.Chdir(t.TempDir())
	cwd, err := syscall.Getwd()
	require.NoError(t, err)
	args, err := (Request{Variables: []string{"LEAPMUX_PROBE_VALUE", "LEAPMUX_PROBE_UNSET"}, PathVariables: map[string]string{"LEAPMUX_PROBE_PATH": " \t"}}).Arguments()
	require.NoError(t, err)
	assert.NotContains(t, strings.Join(args, " "), os.Getenv("LEAPMUX_PROBE_VALUE"))
	var out, diagnostic bytes.Buffer
	code, handled := Run(args, &out, &diagnostic)
	require.True(t, handled)
	require.Zero(t, code, diagnostic.String())
	snapshot, err := Decode(append([]byte("profile output\r\n"), out.Bytes()...))
	require.NoError(t, err)
	assert.Equal(t, cwd, snapshot.WorkingDir)
	assert.Equal(t, os.Getenv("LEAPMUX_PROBE_VALUE"), snapshot.Values["LEAPMUX_PROBE_VALUE"])
	assert.Empty(t, snapshot.Values["LEAPMUX_PROBE_UNSET"])
	assert.NotContains(t, snapshot.Values, "LEAPMUX_PROBE_SECRET")
	assert.Equal(t, cwd+string(filepath.Separator)+"./child/../file", snapshot.Paths["LEAPMUX_PROBE_PATH"])
}

func TestRunRejectsInvalidRequestsAndPassesOrdinaryArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"worker"}, {"--other", Argument}} {
		var out bytes.Buffer
		_, handled := Run(args, &out, &out)
		assert.False(t, handled)
		assert.Empty(t, out.String())
	}
	for _, body := range []string{`{`, `{ "unknown":true}`, `{} {}`, `{"variables":["X;exit"]}`, `{"pathVariables":{"X-Y":" "}}`} {
		var out, diagnostic bytes.Buffer
		code, handled := Run([]string{Argument, body}, &out, &diagnostic)
		assert.True(t, handled)
		assert.Equal(t, 2, code)
		assert.Empty(t, out.String())
		assert.NotEmpty(t, diagnostic.String())
	}
	var diagnostic bytes.Buffer
	code, handled := Run([]string{Argument}, io.Discard, &diagnostic)
	assert.True(t, handled)
	assert.Equal(t, 2, code)
	_, err := (Request{Variables: []string{"9INVALID"}}).Arguments()
	assert.Error(t, err)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunReportsOutputFailure(t *testing.T) {
	t.Parallel()
	var diagnostic bytes.Buffer
	code, handled := Run([]string{Argument, `{}`}, failingWriter{}, &diagnostic)
	assert.True(t, handled)
	assert.Equal(t, 1, code)
	assert.Contains(t, diagnostic.String(), "could not complete")
}

func TestDecodeRejectsMissingIncompleteAndInvalidContext(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", responsePrefix + `{}`, responsePrefix + `{` + "\x00", responsePrefix + `{"values":{},"workingDir":"relative"}` + "\x00", responsePrefix + `{"workingDir":"/"}` + "\x00"} {
		_, err := Decode([]byte(value))
		assert.Error(t, err, value)
	}
}

func TestRootedReadPathPreservesNativeSegments(t *testing.T) {
	t.Parallel()
	cwd := filepath.Join(string(filepath.Separator), "workspace")
	if runtime.GOOS == "windows" {
		cwd = `C:\workspace`
		assert.Equal(t, `C:\workspace\child\..\file`, rootedReadPath(`C:child\..\file`, cwd))
		assert.Equal(t, `C:\child\..\file`, rootedReadPath(`\child\..\file`, cwd))
	}
	assert.Equal(t, cwd+string(filepath.Separator)+"child/../file", rootedReadPath("child/../file", cwd))
	absolute := cwd + string(filepath.Separator) + "link/../file"
	assert.Equal(t, absolute, rootedReadPath(absolute, cwd))
}
