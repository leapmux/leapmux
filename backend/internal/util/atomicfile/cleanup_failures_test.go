package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type removalFailureOps struct {
	defaultFileOps
	failures map[string]error
	attempts []string
}

func (f *removalFailureOps) remove(path string) error {
	f.attempts = append(f.attempts, path)
	if err := f.failures[path]; err != nil {
		return err
	}
	return f.defaultFileOps.remove(path)
}

func TestRemoveTempFilesReportsAllFailures(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "credentials.json")
	first, second := destination+".tmp-first", destination+".tmp-second"
	for _, path := range []string{first, second} {
		require.NoError(t, os.WriteFile(path, []byte("private credentials"), 0o600))
	}
	firstErr, secondErr := errors.New("first copy removal failed"), errors.New("second copy removal failed")
	failing := &removalFailureOps{failures: map[string]error{first: firstErr, second: secondErr}}
	withOps(t, failing)
	err := RemoveTempFiles(destination)
	assert.ErrorIs(t, err, firstErr)
	assert.ErrorIs(t, err, secondErr)
	assert.ElementsMatch(t, []string{first, second}, failing.attempts)
	assert.FileExists(t, first)
	assert.FileExists(t, second)
}
