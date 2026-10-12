package commandcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNativeTextKeepsEmptyAndExactBlockContents(t *testing.T) {
	assert.Equal(t, "", nativeText(json.RawMessage(`[{"type":"text","text":""}]`)))
	assert.Equal(t, "  first\nsecond", nativeText(json.RawMessage(`[{"type":"text","text":"  first\n"},{"type":"image","text":"hidden"},{"type":"text","text":"second"}]`)))
	assert.Equal(t, "", nativeText(json.RawMessage(`null`)))
	assert.Equal(t, "", nativeText(json.RawMessage(`{"type":"text"}`)))
}

func TestNativeErrorsKeepStringAndObjectCauses(t *testing.T) {
	assert.Equal(t, "native failure", nativeErrorText(json.RawMessage(`"native failure"`)))
	assert.Equal(t, "native failure", nativeErrorText(json.RawMessage(`{"message":"native failure"}`)))
	assert.Empty(t, nativeErrorText(json.RawMessage(`null`)))
}
