package agenttest

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAbsPath_IsAbsoluteOnTheRunningOS keeps the test name from the agent
// package. The shared NativeAbsPath helper now supplies the implementation.
func TestAbsPath_IsAbsoluteOnTheRunningOS(t *testing.T) {
	t.Parallel()

	got := testutil.NativeAbsPath("/workspace/project")
	assert.True(t, filepath.IsAbs(got),
		"NativeAbsPath must return an absolute path on %s: %q", runtime.GOOS, got)
}

func TestFixtureJSONString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, `"plain"`, JSONString("plain"))

	// The case the helper exists for, stated as a literal so it is exercised on
	// every OS: a Windows path decodes back to itself.
	windows := `C:\Users\dev\project`
	var got string
	require.NoError(t, json.Unmarshal([]byte(JSONString(windows)), &got))
	assert.Equal(t, windows, got)

	// And the surrounding document stays decodable, which is what a fixture
	// that pasted the raw path lost.
	var record struct {
		Cwd string `json:"cwd"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"cwd":`+JSONString(windows)+`}`), &record))
	assert.Equal(t, windows, record.Cwd)

	var broken struct {
		Cwd string `json:"cwd"`
	}
	assert.Error(t, json.Unmarshal([]byte(`{"cwd":"`+windows+`"}`), &broken),
		"the unescaped form is what this helper replaces")
}
