package envprobe

import (
	"bytes"
	"encoding/base64"
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
	t.Setenv("LEAPMUX_PROBE_PATH", " \t\"./child/../file\" \t")
	t.Chdir(t.TempDir())
	cwd, err := syscall.Getwd()
	require.NoError(t, err)
	// The cutset holds a double quote, which the request must carry intact.
	args, err := (Request{Variables: []string{"LEAPMUX_PROBE_VALUE", "LEAPMUX_PROBE_UNSET"}, PathVariables: map[string]string{"LEAPMUX_PROBE_PATH": " \t\""}}).Arguments()
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
	assert.False(t, snapshot.Present["LEAPMUX_PROBE_UNSET"])
	assert.True(t, snapshot.Present["LEAPMUX_PROBE_VALUE"])
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
	const invalidRequest = "invalid internal environment probe request"
	for request, wantDiagnostic := range map[string]string{
		// Valid base64url that holds an invalid request
		encodedRequest(``):                              invalidRequest,
		encodedRequest(`{`):                             invalidRequest,
		encodedRequest(`{ "unknown":true}`):             invalidRequest,
		encodedRequest(`{} {}`):                         invalidRequest,
		encodedRequest(`{"variables":["X;exit"]}`):      `environment variable "X;exit" is not a plain identifier`,
		encodedRequest(`{"pathVariables":{"X-Y":" "}}`): `environment variable "X-Y" is not a plain identifier`,
		// Not the canonical encoding of any request: plain JSON, padding,
		// non-zero trailing bits, and a character of the standard alphabet
		`{}`:   invalidRequest,
		`e30=`: invalidRequest,
		`e31`:  invalidRequest,
		`e3/`:  invalidRequest,
	} {
		var out, diagnostic bytes.Buffer
		code, handled := Run([]string{Argument, request}, &out, &diagnostic)
		assert.True(t, handled, request)
		assert.Equal(t, 2, code, request)
		assert.Empty(t, out.String(), request)
		assert.Contains(t, diagnostic.String(), wantDiagnostic, request)
	}
	var diagnostic bytes.Buffer
	code, handled := Run([]string{Argument}, io.Discard, &diagnostic)
	assert.True(t, handled)
	assert.Equal(t, 2, code)
	_, err := (Request{Variables: []string{"9INVALID"}}).Arguments()
	assert.Error(t, err)
}

// A shell passes the request to the probe as a native argument. Windows
// PowerShell 5.1 passes each embedded double quote unescaped, so the probe's
// command-line parser drops it. The request must reach the probe in an
// alphabet that no shell and no command-line parser rewrites.
func TestArgumentsEncodeTheRequestInAShellInertAlphabet(t *testing.T) {
	t.Parallel()
	for _, request := range []Request{
		{},
		{Variables: []string{"A", "B"}},
		{Variables: []string{"A"}, PathVariables: map[string]string{"B": " \t\"'\\$%`"}},
	} {
		args, err := request.Arguments()
		require.NoError(t, err)
		require.Len(t, args, 2)
		assert.Equal(t, Argument, args[0])
		assert.Regexp(t, `^[A-Za-z0-9_-]+$`, args[1])
	}
}

// encodedRequest is the probe argument that carries body, in the encoding
// that Request.Arguments uses.
func encodedRequest(body string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(body))
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunReportsOutputFailure(t *testing.T) {
	t.Parallel()
	args, err := (Request{}).Arguments()
	require.NoError(t, err)
	var diagnostic bytes.Buffer
	code, handled := Run(args, failingWriter{}, &diagnostic)
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

func TestRunDistinguishesUnsetAndEmptyNativeValues(t *testing.T) {
	const name = "LEAPMUX_PROBE_PRESENCE"
	t.Setenv(name, "")
	args, err := (Request{Variables: []string{name}}).Arguments()
	require.NoError(t, err)
	capture := func() string {
		t.Helper()
		var output, diagnostic bytes.Buffer
		code, handled := Run(args, &output, &diagnostic)
		require.True(t, handled)
		require.Zero(t, code, diagnostic.String())
		return output.String()
	}
	empty := capture()
	require.NoError(t, os.Unsetenv(name))
	unset := capture()
	assert.NotEqual(t, empty, unset, "native consumers distinguish an unset value from an explicitly empty value")
}

func TestRunMetadataKeepsNativeValuesOnOneLine(t *testing.T) {
	const prefix = "__LEAPMUX_META_test__ "
	t.Setenv("LEAPMUX_PROBE_METADATA", "日本語\nline\r\n%$'")
	args, err := (Request{Variables: []string{"LEAPMUX_PROBE_METADATA"}, MetadataPrefix: prefix}).Arguments()
	require.NoError(t, err)
	var output, diagnostic bytes.Buffer
	code, handled := Run(args, &output, &diagnostic)
	require.True(t, handled)
	require.Zero(t, code, diagnostic.String())
	require.Equal(t, 1, strings.Count(output.String(), "\n"))
	require.True(t, strings.HasPrefix(output.String(), prefix+MetadataKey+"="))
	value := strings.TrimSuffix(strings.TrimPrefix(output.String(), prefix+MetadataKey+"="), "\n")
	snapshot, err := DecodeMetadata(value)
	require.NoError(t, err)
	assert.Equal(t, os.Getenv("LEAPMUX_PROBE_METADATA"), snapshot.Values["LEAPMUX_PROBE_METADATA"])
	assert.True(t, snapshot.Present["LEAPMUX_PROBE_METADATA"])
	assert.NotEmpty(t, snapshot.WorkingDir)
}

func TestMetadataRejectsInvalidFramingAndPresence(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"line\nnext", "line\rnext", "line\x00next"} {
		_, err := (Request{MetadataPrefix: prefix}).Arguments()
		assert.Error(t, err)
	}
	for _, value := range []string{"", "e30=", "e31", encodedRequest(`{}`), encodedRequest(`{"values":{},"present":{},"workingDir":"relative"}`), encodedRequest(`{"values":{"A":"value"},"present":{"A":false},"workingDir":"/"}`), encodedRequest(`{"values":{"A":""},"present":{},"workingDir":"/"}`)} {
		_, err := DecodeMetadata(value)
		assert.Error(t, err, value)
	}
}

type shortProbeWriter struct{}

func (shortProbeWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestRunRejectsAProbeOutputThatWasOnlyPartlyWritten(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "__LEAPMUX_META_test__ "} {
		args, err := (Request{MetadataPrefix: prefix}).Arguments()
		require.NoError(t, err)
		var diagnostic bytes.Buffer
		code, handled := Run(args, shortProbeWriter{}, &diagnostic)
		assert.True(t, handled)
		assert.Equal(t, 1, code)
		assert.Contains(t, diagnostic.String(), "could not complete")
	}
}

func TestRunRejectsANativeValueThatJSONCannotPreserve(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows converts an invalid UTF-8 value before native capture")
	}
	t.Setenv("LEAPMUX_PROBE_INVALID", string([]byte{255}))
	args, err := (Request{Variables: []string{"LEAPMUX_PROBE_INVALID"}}).Arguments()
	require.NoError(t, err)
	var output, diagnostic bytes.Buffer
	code, handled := Run(args, &output, &diagnostic)
	assert.True(t, handled)
	assert.Equal(t, 1, code)
	assert.Empty(t, output.String())
}
