package agenttest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
