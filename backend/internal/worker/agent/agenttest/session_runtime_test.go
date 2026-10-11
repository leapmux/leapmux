package agenttest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRuntimeReceiptReadsCompleteFilesWithinItsLimit(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1, 64 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "receipt")
			expected := bytes.Repeat([]byte{'x'}, size)
			require.NoError(t, os.WriteFile(path, expected, 0o600))
			actual, err := readSessionRuntimeReceipt(path)
			require.NoError(t, err)
			assert.Equal(t, expected, actual)
		})
	}
}

func TestSessionRuntimeReceiptRejectsMissingNonregularAndOversizedFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	data, err := readSessionRuntimeReceipt(missing)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, data)
	data, err = readSessionRuntimeReceipt(root)
	assert.Error(t, err)
	assert.Nil(t, data)
	large := filepath.Join(root, "large")
	require.NoError(t, os.WriteFile(large, bytes.Repeat([]byte{'x'}, (64<<10)+1), 0o600))
	data, err = readSessionRuntimeReceipt(large)
	assert.ErrorContains(t, err, "size limit")
	assert.Nil(t, data)
}

func TestSessionRuntimeReceiptWritesOnlyOneActualInvocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	t.Setenv(sessionRuntimeReceiptEnv, path)
	t.Setenv(SessionRuntimeMarkerEnv, "controlled-peer")
	require.NoError(t, WriteSessionRuntimeInvocation())
	first, err := readSessionRuntimeReceipt(path)
	require.NoError(t, err)
	var observed sessionRuntimeInvocation
	require.NoError(t, json.Unmarshal(first, &observed))
	actualDirectory, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, actualDirectory, observed.WorkingDir)
	assert.Equal(t, os.Args, observed.Args)
	assert.Equal(t, "controlled-peer", observed.Marker)
	require.ErrorIs(t, WriteSessionRuntimeInvocation(), os.ErrExist)
	second, err := readSessionRuntimeReceipt(path)
	require.NoError(t, err)
	assert.Equal(t, first, second, "a repeated invocation must preserve the first receipt")
}

func TestSessionRuntimeReceiptRefusesAnAbsentOrRelativeDestination(t *testing.T) {
	for _, path := range []string{"", "relative-receipt.json"} {
		t.Setenv(sessionRuntimeReceiptEnv, path)
		require.Error(t, WriteSessionRuntimeInvocation())
	}
}

func TestConcurrentSessionRuntimeInvocationsKeepOneCompleteReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	t.Setenv(sessionRuntimeReceiptEnv, path)
	t.Setenv(SessionRuntimeMarkerEnv, "concurrent-peer")
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			results <- WriteSessionRuntimeInvocation()
		}()
	}
	close(start)
	var successes int
	for range 8 {
		err := <-results
		if err == nil {
			successes++
		} else {
			assert.ErrorIs(t, err, os.ErrExist)
		}
	}
	require.Equal(t, 1, successes)
	data, err := readSessionRuntimeReceipt(path)
	require.NoError(t, err)
	var receipt sessionRuntimeInvocation
	require.NoError(t, json.Unmarshal(data, &receipt))
	assert.Equal(t, "concurrent-peer", receipt.Marker)
}
