//go:build unix

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteExecutableWritesARunnableFile(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"an empty file":           "",
		"a script":                "#!/bin/sh\n",
		"bytes that are not text": "\x7fELF\x02\x00\xff",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "program")
			WriteExecutable(t, path, content)

			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.NotZero(t, info.Mode().Perm()&0o100, "the owner can run the file under any umask")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, content, string(data))
		})
	}
}

func TestWriteExecutableReplacesAnEarlierProgram(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "program")
	WriteExecutable(t, path, "#!/bin/sh\nexit 3\n")
	WriteExecutable(t, path, "#!/bin/sh\nexit 7\n")

	var exit *exec.ExitError
	require.ErrorAs(t, exec.Command(path).Run(), &exit)
	assert.Equal(t, 7, exit.ExitCode())
}
