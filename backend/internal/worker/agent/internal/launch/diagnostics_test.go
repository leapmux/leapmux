package launch

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const diagnosticsExitPeer = "-test.run=^TestLaunchDiagnosticsExitPeer$"

func TestLaunchDiagnosticsExitPeer(t *testing.T) {
	if len(os.Args) != 4 || os.Args[1] != diagnosticsExitPeer || os.Args[2] != "--" {
		return
	}
	code, err := strconv.Atoi(os.Args[3])
	require.NoError(t, err)
	os.Exit(code)
}

func TestLaunchDiagnosticsRecognizesOnlyACompleteExhaustionRecord(t *testing.T) {
	t.Parallel()
	executable := profileCommandExecutable(t)
	statuses := make(map[int]error)
	for _, code := range []int{127, 37} {
		status := exec.CommandContext(t.Context(), executable, diagnosticsExitPeer, "--", strconv.Itoa(code)).Run()
		var exit *exec.ExitError
		require.ErrorAs(t, status, &exit)
		require.Equal(t, code, exit.ExitCode())
		statuses[code] = status
	}

	const marker = "__EXHAUSTED_THIS_LAUNCH__"
	prepared := &PreparedLaunch{candidates: []string{"first", "second"}, exhaustedMarker: marker}
	for _, test := range []struct {
		name       string
		chunks     []string
		ready      bool
		missing    bool
		diagnostic string
		exitCode   int
	}{
		{"finds a split line after the display cap", []string{strings.Repeat("x", 512), "\n" + marker[:6], marker[6:] + "\r", "\n"}, false, true, strings.Repeat("x", 32), 127},
		{"accepts a complete LF line", []string{"diagnostic\n" + marker + "\n"}, false, true, "diagnostic\n", 127},
		{"keeps a CRLF diagnostic and partial final line", []string{"diagnostic\r\n" + marker + "\r\n", "par", "tial"}, false, true, "diagnostic\r\npartial", 127},
		{"rejects an unterminated record", []string{marker}, false, false, marker, 127},
		{"rejects duplicates", []string{marker + "\n" + marker + "\n"}, false, false, "", 127},
		{"rejects a mismatched nonce", []string{"__EXHAUSTED_ANOTHER_LAUNCH__\n"}, false, false, "__EXHAUSTED_ANOTHER_LAUNCH__\n", 127},
		{"rejects an embedded marker", []string{"prefix" + marker + "\n"}, false, false, "prefix" + marker + "\n", 127},
		{"prefers readiness over an exhaustion marker", []string{marker + "\n"}, true, false, "", 127},
		{"requires exit 127 even with one complete record", []string{marker + "\n"}, false, false, "", 37},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			diagnostics := prepared.NewDiagnostics(32)
			for _, chunk := range test.chunks {
				written, err := io.WriteString(diagnostics, chunk)
				require.NoError(t, err)
				assert.Equal(t, len(chunk), written)
			}
			diagnostics.Finish()
			assert.Equal(t, test.diagnostic, diagnostics.String())
			err := diagnostics.ExitError(statuses[test.exitCode], test.ready)
			if test.missing {
				require.ErrorIs(t, err, ErrNoCandidate)
				assert.ErrorContains(t, err, "first, second")
				assert.NotContains(t, diagnostics.String(), marker)
			} else {
				require.NotErrorIs(t, err, ErrNoCandidate)
			}
			var actualExit *exec.ExitError
			require.ErrorAs(t, err, &actualExit)
			assert.Equal(t, test.exitCode, actualExit.ExitCode())
		})
	}
}
