package testutil_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// requireDockerChildEnv makes TestHelperProcessRequireDocker call
// RequireDocker. A child process lets a test observe a skip or a failure of
// RequireDocker without skipping or failing itself.
const requireDockerChildEnv = "LEAPMUX_TEST_REQUIRE_DOCKER_CHILD"

// requireDockerReturned is the line that the child prints when RequireDocker
// neither skips nor fails the test.
const requireDockerReturned = "require-docker-returned"

func TestHelperProcessRequireDocker(t *testing.T) {
	if os.Getenv(requireDockerChildEnv) != "1" {
		return
	}
	testutil.RequireDocker(t)
	_, _ = os.Stdout.WriteString(requireDockerReturned + "\n")
	assert.Equal(t, "unix:///leapmux-test-docker.sock", os.Getenv("DOCKER_HOST"), "RequireDocker keeps a DOCKER_HOST that is already set")
}

// runRequireDocker calls RequireDocker in a child process, with skipDocker as
// the value of SkipDockerEnv. DOCKER_HOST is set, so the child never runs the
// docker CLI to detect the socket.
func runRequireDocker(t *testing.T, skipDocker string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessRequireDocker$", "-test.v")
	cmd.Env = append(os.Environ(),
		requireDockerChildEnv+"=1",
		testutil.SkipDockerEnv+"="+skipDocker,
		"DOCKER_HOST=unix:///leapmux-test-docker.sock",
	)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestRequireDockerSkipsTheTestInARunWithoutDocker(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE"} {
		t.Run(value, func(t *testing.T) {
			output, err := runRequireDocker(t, value)
			require.NoError(t, err, output)
			assert.Contains(t, output, "--- SKIP: TestHelperProcessRequireDocker")
			assert.Contains(t, output, "LEAPMUX_TEST_SKIP_DOCKER is set, so this run has no Docker daemon")
			assert.NotContains(t, output, requireDockerReturned)
		})
	}
}

func TestRequireDockerRequiresDockerUnlessTheRunOptsOut(t *testing.T) {
	for _, value := range []string{"", "0", "false"} {
		t.Run(value, func(t *testing.T) {
			output, err := runRequireDocker(t, value)
			require.NoError(t, err, output)
			assert.Contains(t, output, "--- PASS: TestHelperProcessRequireDocker")
			assert.Contains(t, output, requireDockerReturned)
		})
	}
}

func TestRequireDockerFailsForAValueThatIsNotABoolean(t *testing.T) {
	for _, value := range []string{"yes", " 1", "2"} {
		t.Run(value, func(t *testing.T) {
			output, err := runRequireDocker(t, value)
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, output)
			assert.Contains(t, output, "--- FAIL: TestHelperProcessRequireDocker")
			assert.Contains(t, output, "LEAPMUX_TEST_SKIP_DOCKER must be a boolean such as 1 or 0, not \""+value+"\"")
			assert.NotContains(t, output, requireDockerReturned)
		})
	}
}
