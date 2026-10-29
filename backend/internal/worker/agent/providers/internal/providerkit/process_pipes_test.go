package providerkit

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupProcessPipesClosesChildEndsWhenALaterPipeFails(t *testing.T) {
	t.Parallel()
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("unused-command")
			if stream == "stdout" {
				cmd.Stdout = &bytes.Buffer{}
			} else {
				cmd.Stderr = &bytes.Buffer{}
			}
			cancelled := 0
			pipes, err := SetupProcessPipes(cmd, func() { cancelled++ })
			require.Error(t, err)
			assert.Nil(t, pipes)
			assert.Equal(t, 1, cancelled)
			_, err = cmd.Stdin.(*os.File).Stat()
			assert.ErrorIs(t, err, os.ErrClosed)
			if stream == "stderr" {
				_, err = cmd.Stdout.(*os.File).Stat()
				assert.ErrorIs(t, err, os.ErrClosed)
			}
		})
	}
}
