package agent

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Init / buffer helpers run on all platforms (no process spawn).
func TestAcpStandardInitParams_ClientCapabilitiesTerminal_AllGOOS(t *testing.T) {
	raw, err := acpStandardInitParams()
	require.NoError(t, err)

	var params map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &params))

	_, hasLegacy := params["capabilities"]
	assert.False(t, hasLegacy, "legacy capabilities key must not be sent")

	caps, ok := params["clientCapabilities"].(map[string]interface{})
	require.True(t, ok, "clientCapabilities must be present")
	assert.Equal(t, true, caps["terminal"])

	fs, ok := caps["fs"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, fs["readTextFile"])
	assert.Equal(t, false, fs["writeTextFile"])
}

func TestTruncateACPTerminalOutput_AllGOOS(t *testing.T) {
	// "xy" + "é" (2-byte UTF-8) + "abc". Keeping the last 4 bytes starts on
	// the trailing byte of "é"; truncation must advance to the next rune
	// start, retaining "abc" rather than a broken leading byte.
	in := append([]byte("xy"), []byte("éabc")...)
	out := truncateACPTerminalOutput(in, 4)
	assert.Equal(t, "abc", string(out))
	assert.True(t, utf8.Valid(out))

	hello := []byte("hello")
	assert.Equal(t, hello, truncateACPTerminalOutput(hello, 10))
	assert.Nil(t, truncateACPTerminalOutput(hello, 0), "limit 0 retains nothing")
}

func TestMergeACPTerminalEnv_AllGOOS(t *testing.T) {
	base := []string{"PATH=/bin", "FOO=1", "BAR=2"}
	out := mergeACPTerminalEnv(base, []acpTerminalEnvVar{
		{Name: "FOO", Value: "9"},
		{Name: "BAZ", Value: "z"},
		{Name: "", Value: "ignored"},
	})
	// PinEnv drops overridden keys then appends assignments.
	assert.Equal(t, []string{"PATH=/bin", "BAR=2", "FOO=9", "BAZ=z"}, out)
	assert.Equal(t, base, []string{"PATH=/bin", "FOO=1", "BAR=2"}, "base must not be mutated")

	same := mergeACPTerminalEnv(base, nil)
	assert.Equal(t, base, same)
	assert.Equal(t, base, mergeACPTerminalEnv(base, []acpTerminalEnvVar{}))
}

func TestAppendOutput_ZeroByteLimitDiscards(t *testing.T) {
	sess := &acpTerminalSession{byteLimit: 0}
	sess.appendOutput([]byte("hello"))
	out, truncated, _, _, _ := sess.snapshot()
	assert.Equal(t, "", out)
	assert.True(t, truncated)
}

func TestExitStatusFromProcessState_Nil(t *testing.T) {
	code, sig := exitStatusFromProcessState(nil)
	assert.Nil(t, code)
	assert.Nil(t, sig)
}

func TestTruncateACPTerminalOutput_DropsIncompleteTrailingStart(t *testing.T) {
	// A lone continuation byte at the cut point advances past the whole
	// buffer when nothing remains that starts a rune.
	in := []byte{0x80, 0x80, 0x80}
	out := truncateACPTerminalOutput(in, 2)
	assert.Nil(t, out)
}

func TestSplitEnvKV(t *testing.T) {
	name, value, ok := splitEnvKV("FOO=bar=baz")
	assert.True(t, ok)
	assert.Equal(t, "FOO", name)
	assert.Equal(t, "bar=baz", value)

	_, _, ok = splitEnvKV("NOEQUALS")
	assert.False(t, ok)
}
