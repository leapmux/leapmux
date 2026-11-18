package launch

import (
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLaunchDiagnosticsRecognizesOnlyACompleteExhaustionRecord(t *testing.T) {
	t.Parallel()
	shell := testutil.TestShell()
	status := exec.CommandContext(t.Context(), shell, terminal.CommandArgs(shell, false, "exit 127")...).Run()
	var exit *exec.ExitError
	require.ErrorAs(t, status, &exit)
	require.Equal(t, 127, exit.ExitCode())

	const marker = "__EXHAUSTED_THIS_LAUNCH__"
	prepared := &PreparedLaunch{candidates: []string{"first", "second"}, exhaustedMarker: marker}
	for _, test := range []struct {
		name    string
		chunks  []string
		ready   bool
		missing bool
	}{
		{"finds a split line after the display cap", []string{strings.Repeat("x", 512), "\n" + marker[:6], marker[6:] + "\r", "\n"}, false, true},
		{"accepts a complete LF line", []string{"diagnostic\n" + marker + "\n"}, false, true},
		{"rejects an unterminated record", []string{marker}, false, false},
		{"rejects duplicates", []string{marker + "\n" + marker + "\n"}, false, false},
		{"rejects a mismatched nonce", []string{"__EXHAUSTED_ANOTHER_LAUNCH__\n"}, false, false},
		{"rejects an embedded marker", []string{"profile" + marker + "\n"}, false, false},
		{"prefers readiness over an exhaustion marker", []string{marker + "\n"}, true, false},
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
			assert.LessOrEqual(t, len(diagnostics.String()), 32)
			err := diagnostics.ExitError(status, test.ready)
			if test.missing {
				require.ErrorIs(t, err, ErrNoCandidate)
				assert.ErrorContains(t, err, "first, second")
				assert.NotContains(t, diagnostics.String(), marker)
			} else {
				require.NotErrorIs(t, err, ErrNoCandidate)
			}
			var actualExit *exec.ExitError
			require.ErrorAs(t, err, &actualExit)
			assert.Equal(t, 127, actualExit.ExitCode())
		})
	}
}
