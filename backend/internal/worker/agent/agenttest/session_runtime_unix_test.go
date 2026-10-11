//go:build unix

package agenttest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRuntimeReceiptRejectsASymlinkToAnotherReceipt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "actual")
	link := filepath.Join(root, "receipt")
	require.NoError(t, os.WriteFile(target, []byte("actual receipt"), 0o600))
	require.NoError(t, os.Symlink(target, link))
	data, err := readSessionRuntimeReceipt(link)
	assert.Error(t, err)
	assert.Nil(t, data)
}
