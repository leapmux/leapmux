//go:build windows

package launch

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/windows"
)

var workerWindowsPath = newWindowsPathRefresh(readWindowsAccountPath)

func readWindowsAccountPath() ([]string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		return nil, fmt.Errorf("open the Worker account token: %w", err)
	}
	defer token.Close()

	// Windows merges and expands the User and System values for this account.
	env, err := token.Environ(false)
	if err != nil {
		return nil, fmt.Errorf("read the Worker account environment: %w", err)
	}
	path, _ := windowsEnvironmentPath(env)
	return filepath.SplitList(path), nil
}

// RefreshCommandPath updates this child process from the saved Windows PATH.
func RefreshCommandPath(cmd *exec.Cmd) error {
	return workerWindowsPath.apply(cmd)
}

func (r *windowsPathRefresh) apply(cmd *exec.Cmd) error {
	env := cmd.Environ()
	path, present := windowsEnvironmentPath(env)
	if !present && cmd.Env != nil {
		return nil
	}
	inherited := filepath.SplitList(path)
	current, err := r.directories(inherited)
	if slices.Equal(current, inherited) {
		return err
	}
	for index, directory := range current {
		if strings.Contains(directory, ";") {
			current[index] = "\"" + directory + "\""
		}
	}
	cmd.Env = append(env, "Path="+strings.Join(current, ";"))
	return err
}

func windowsEnvironmentPath(env []string) (string, bool) {
	for index := len(env) - 1; index >= 0; index-- {
		key, value, found := strings.Cut(env[index], "=")
		if found && strings.EqualFold(key, "PATH") {
			return value, true
		}
	}
	return "", false
}

func resolvePreparedShell(cmd *exec.Cmd, shell string) (string, error) {
	if cmd.Env == nil || strings.ContainsAny(shell, "/\\") || filepath.VolumeName(shell) != "" {
		return shell, nil
	}
	path, present := windowsEnvironmentPath(cmd.Env)
	if !present {
		return shell, nil
	}
	for _, directory := range filepath.SplitList(path) {
		if !filepath.IsAbs(directory) {
			continue
		}
		if executable, err := exec.LookPath(filepath.Join(directory, shell)); err == nil {
			return executable, nil
		}
	}
	return shell, &exec.Error{Name: shell, Err: exec.ErrNotFound}
}
