package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ConfigureDockerHost sets the DOCKER_HOST and TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE
// environment variables for testcontainers-go, auto-detecting the Docker socket path.
// Call this at the start of integration tests that use testcontainers.
func ConfigureDockerHost(t *testing.T) {
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
