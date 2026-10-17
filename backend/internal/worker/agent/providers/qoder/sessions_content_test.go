package qoder

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
)

func TestQoderSessionTitleRejectsANoticeAfterNullContent(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/repo/project")
	content := append([]any{nil}, qoderTextBlocks("No response requested.", "incorrect title")...)
	title := readQoderTitle(t, dir,
		qoderUserRecord(t, dir, content, qoderHuman),
		qoderUserRecord(t, dir, "the actual prompt", qoderHuman),
	)
	assert.Equal(t, "the actual prompt", title)
}
