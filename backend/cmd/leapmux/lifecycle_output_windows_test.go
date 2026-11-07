//go:build windows

package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func fillConsolePipe(t *testing.T, reader, writer *os.File) {
	t.Helper()
	var outputSize, inputSize uint32
	require.NoError(t, windows.GetNamedPipeInfo(windows.Handle(writer.Fd()), nil, &outputSize, &inputSize, nil))
	size := max(outputSize, inputSize)
	require.Positive(t, size)
	require.LessOrEqual(t, size, uint32(16<<20))
	done := make(chan error, 1)
	var written uint32
	go func() { done <- windows.WriteFile(windows.Handle(writer.Fd()), make([]byte, int(size)), &written, nil) }()
	select {
	case err := <-done:
		require.NoError(t, err)
		require.Equal(t, size, written)
	case <-testutil.DeadlineContext(t).Done():
		_ = reader.Close()
		releaseErr := <-done
		t.Fatalf("the console pipe could not accept its reported capacity: %v", releaseErr)
	}
}
