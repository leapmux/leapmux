package codewhale

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodewhaleNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	const hidden = "NATIVE_EXTERNAL_TEXT_MUST_NOT_ENTER_WORKER_7841"
	f := nativeSavedTextFixture(t, hidden)
	original, supplement := f.close(t)
	assert.NotContains(t, string(original), hidden)
	assert.NotContains(t, string(supplement), hidden, "external output text must not enter Worker storage")
	assert.Contains(t, string(original), f.path)
}
