package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// SkipDockerEnv turns off every test that needs Docker. `task test-no-docker`
// and the native CI jobs set it to 1, and the Linux amd64 job runs the
// container tests instead. A macOS runner has no Docker daemon, and the Docker
// daemon of a Windows runner runs no Linux container.
const SkipDockerEnv = "LEAPMUX_TEST_SKIP_DOCKER"

// RequireDocker prepares a test that runs a container. It skips the test when
// SkipDockerEnv is true. Otherwise it sets the DOCKER_HOST and
// TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE environment variables for
// testcontainers-go, and detects the path of the Docker socket.
//
// Call it before the test starts a container. storetest.SQLContainer calls it,
// and the depguard rule in .golangci.yml permits testcontainers-go in no other
// file, so every test that starts a container skips under SkipDockerEnv.
func RequireDocker(t *testing.T) {
	t.Helper()
	skip, err := dockerSkipped(os.Getenv(SkipDockerEnv))
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Skipf("%s is set, so this run has no Docker daemon", SkipDockerEnv)
	}
	configureDockerHost(t)
}

// dockerSkipped reads the value of SkipDockerEnv. An empty value requires
// Docker. It returns an error for a value that is not a boolean, because a
// mistyped value must not make the run require Docker without a notice.
func dockerSkipped(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	skip, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean such as 1 or 0, not %q", SkipDockerEnv, value)
	}
	return skip, nil
}

func configureDockerHost(t *testing.T) {
	t.Helper()

	// If DOCKER_HOST is already set, respect it.
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}

	// Try to auto-detect from `docker context inspect`.
	// This works for Rancher Desktop, Docker Desktop, and standard Docker.
	out, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err == nil {
		host := strings.TrimSpace(string(out))
		if host != "" {
			setDockerEnv(t, host)
			return
		}
	}

	// Fallback: check common socket paths.
	sockets := []string{
		os.Getenv("HOME") + "/.rd/docker.sock",         // Rancher Desktop
		os.Getenv("HOME") + "/.docker/run/docker.sock", // Docker Desktop (macOS)
		"/var/run/docker.sock",                         // Standard Linux
	}
	for _, sock := range sockets {
		if _, err := os.Stat(sock); err == nil {
			setDockerEnv(t, "unix://"+sock)
			return
		}
	}

	// No socket found; let testcontainers try its default.
}

func setDockerEnv(t *testing.T, host string) {
	t.Helper()
	t.Setenv("DOCKER_HOST", host)
	// TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE is the path where the Docker
	// socket is mounted inside the Ryuk reaper container. Testcontainers
	// bind-mounts the host socket to this path, so it should always be
	// /var/run/docker.sock regardless of where the socket lives on the host.
	t.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock")
}
