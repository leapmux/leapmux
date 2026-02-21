package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminal_StartAndStop(t *testing.T) {
	var mu sync.Mutex
	var output []byte

	term, err := Start(Options{
		ID:         "test-1",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte) {
		mu.Lock()
		output = append(output, data...)
		mu.Unlock()
	})
	require.NoError(t, err, "Start")

	// Send a command.
	require.NoError(t, term.SendInput([]byte("echo hello\n")), "SendInput")

	// Wait for output.
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(output), "hello")
	}, "expected output to contain 'hello'")

	// Stop the terminal.
	term.Stop()
	exitCode := term.Wait()
	t.Logf("exit code: %d", exitCode)

	// Double stop is safe.
	term.Stop()
}

func TestTerminal_Resize(t *testing.T) {
	term, err := Start(Options{
		ID:         "test-resize",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func([]byte) {})
	require.NoError(t, err, "Start")
	defer func() {
		term.Stop()
		term.Wait()
	}()

	assert.NoError(t, term.Resize(120, 40), "Resize")
}

func TestTerminal_SendInputAfterStop(t *testing.T) {
	term, err := Start(Options{
		ID:         "test-stopped",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
	}, func([]byte) {})
	require.NoError(t, err, "Start")

	term.Stop()
	term.Wait()

	assert.Error(t, term.SendInput([]byte("echo fail\n")), "expected error sending input after stop")
}

func TestTerminal_IsExited(t *testing.T) {
	term, err := Start(Options{
		ID:         "test-exited",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
	}, func([]byte) {})
	require.NoError(t, err, "Start")

	assert.False(t, term.IsExited(), "expected IsExited = false before stop")

	term.Stop()
	term.Wait()

	assert.True(t, term.IsExited(), "expected IsExited = true after stop")
}

func TestManager_StartAndRemove(t *testing.T) {
	m := NewManager()

	err := m.StartTerminal(Options{
		ID:         "tm-1",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
	}, func([]byte) {}, nil)
	require.NoError(t, err, "StartTerminal")

	assert.True(t, m.HasTerminal("tm-1"), "expected HasTerminal = true")

	// Duplicate should fail.
	err = m.StartTerminal(Options{
		ID:         "tm-1",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
	}, func([]byte) {}, nil)
	assert.Error(t, err, "expected error for duplicate terminal")

	// StopTerminal keeps it in the map (exited but not removed).
	m.StopTerminal("tm-1")
	testutil.AssertEventually(t, func() bool {
		return m.IsExited("tm-1")
	}, "expected IsExited = true after StopTerminal")

	assert.True(t, m.HasTerminal("tm-1"), "expected HasTerminal = true after StopTerminal (kept in map)")

	// RemoveTerminal removes from the map.
	m.RemoveTerminal("tm-1")

	assert.False(t, m.HasTerminal("tm-1"), "expected HasTerminal = false after RemoveTerminal")
}

func TestManager_ExitedTerminalRejectsInput(t *testing.T) {
	m := NewManager()

	err := m.StartTerminal(Options{
		ID:         "tm-exit",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
	}, func([]byte) {}, nil)
	require.NoError(t, err, "StartTerminal")

	// Stop and wait for exit.
	m.StopTerminal("tm-exit")
	testutil.AssertEventually(t, func() bool {
		return m.IsExited("tm-exit")
	}, "expected terminal to exit")

	// Input should fail on exited terminal.
	assert.Error(t, m.SendInput("tm-exit", []byte("test")), "expected error sending input to exited terminal")

	// Resize should also fail.
	assert.Error(t, m.Resize("tm-exit", 80, 24), "expected error resizing exited terminal")

	m.RemoveTerminal("tm-exit")
}

func TestManager_ExitNotification(t *testing.T) {
	m := NewManager()
	exitCh := make(chan struct{})
	var gotID string
	var gotCode int

	err := m.StartTerminal(Options{
		ID:         "tm-notify",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
	}, func([]byte) {}, func(id string, code int) {
		gotID = id
		gotCode = code
		close(exitCh)
	})
	require.NoError(t, err, "StartTerminal")

	m.StopTerminal("tm-notify")

	select {
	case <-exitCh:
	case <-time.After(5 * time.Second):
		require.Fail(t, "timed out waiting for exit notification")
	}

	assert.Equal(t, "tm-notify", gotID, "exit notification ID")
	// Exit code from kill is typically -1 or non-zero.
	t.Logf("exit code: %d", gotCode)

	m.RemoveTerminal("tm-notify")
}

func TestManager_StopAll(t *testing.T) {
	m := NewManager()

	for _, id := range []string{"a", "b"} {
		err := m.StartTerminal(Options{
			ID:         id,
			Shell:      "/bin/sh",
			WorkingDir: t.TempDir(),
		}, func([]byte) {}, nil)
		require.NoError(t, err, "StartTerminal(%s)", id)
	}

	m.StopAll()

	for _, id := range []string{"a", "b"} {
		id := id
		testutil.AssertEventually(t, func() bool {
			return !m.HasTerminal(id)
		}, "HasTerminal(%s) = true after StopAll", id)
	}
}

func TestManager_StopUnknown(t *testing.T) {
	m := NewManager()
	// Should not panic.
	m.StopTerminal("nonexistent")
	m.RemoveTerminal("nonexistent")
}

func TestManager_SendInput_UnknownTerminal(t *testing.T) {
	m := NewManager()
	err := m.SendInput("nonexistent", []byte("hello"))
	assert.Error(t, err, "expected error for unknown terminal")
	assert.Contains(t, err.Error(), "no terminal", "error should indicate unknown terminal")
}

func TestManager_Resize_UnknownTerminal(t *testing.T) {
	m := NewManager()
	err := m.Resize("nonexistent", 80, 24)
	assert.Error(t, err, "expected error for unknown terminal")
	assert.Contains(t, err.Error(), "no terminal", "error should indicate unknown terminal")
}

func TestManager_ScreenSnapshot(t *testing.T) {
	m := NewManager()
	var mu sync.Mutex
	var output []byte

	err := m.StartTerminal(Options{
		ID:         "tm-snap",
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte) {
		mu.Lock()
		output = append(output, data...)
		mu.Unlock()
	}, nil)
	require.NoError(t, err, "StartTerminal")

	// Send a command to produce output.
	require.NoError(t, m.SendInput("tm-snap", []byte("echo snapshot_test\n")), "SendInput")

	// Wait for the output to arrive.
	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(output), "snapshot_test")
	}, "expected output to contain 'snapshot_test'")

	// Verify ScreenSnapshot returns non-empty data.
	snap := m.ScreenSnapshot("tm-snap")
	assert.NotEmpty(t, snap, "expected non-empty screen snapshot")
	assert.Contains(t, string(snap), "snapshot_test", "snapshot should contain the echoed text")

	m.StopTerminal("tm-snap")
	testutil.AssertEventually(t, func() bool {
		return m.IsExited("tm-snap")
	}, "expected terminal to exit")
	m.RemoveTerminal("tm-snap")
}

func TestManager_ScreenSnapshot_UnknownTerminal(t *testing.T) {
	m := NewManager()
	snap := m.ScreenSnapshot("nonexistent")
	assert.Nil(t, snap, "expected nil snapshot for unknown terminal")
}

func TestManager_IsExited_UnknownTerminal(t *testing.T) {
	m := NewManager()
	assert.False(t, m.IsExited("nonexistent"), "expected IsExited = false for unknown terminal")
}

func TestManager_HasTerminal_UnknownTerminal(t *testing.T) {
	m := NewManager()
	assert.False(t, m.HasTerminal("nonexistent"), "expected HasTerminal = false for unknown terminal")
}

func TestManager_SaveAndLoadScreens(t *testing.T) {
	m := NewManager()
	var mu sync.Mutex
	var output []byte

	termID := "tm-save"
	wsID := "ws-1"

	err := m.StartTerminal(Options{
		ID:         termID,
		Shell:      "/bin/sh",
		WorkingDir: t.TempDir(),
		Cols:       80,
		Rows:       24,
	}, func(data []byte) {
		mu.Lock()
		output = append(output, data...)
		mu.Unlock()
	}, nil)
	require.NoError(t, err, "StartTerminal")

	// Send a command so there is screen buffer content.
	require.NoError(t, m.SendInput(termID, []byte("echo save_load_test\n")), "SendInput")

	testutil.AssertEventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(output), "save_load_test")
	}, "expected output to contain 'save_load_test'")

	dataDir := t.TempDir()
	getMeta := func(id string) (string, uint32, uint32) {
		if id == termID {
			return wsID, 80, 24
		}
		return "", 0, 0
	}

	// Save screens and metadata.
	require.NoError(t, m.SaveScreens(dataDir, getMeta), "SaveScreens")
	require.NoError(t, m.SaveTerminalMeta(dataDir, getMeta), "SaveTerminalMeta")

	// Verify files were written.
	termDir := filepath.Join(dataDir, "workspaces", wsID, "terminals", termID)
	bufferPath := filepath.Join(termDir, "screen.buffer")
	metaPath := filepath.Join(termDir, "screen.json")

	bufferData, err := os.ReadFile(bufferPath)
	require.NoError(t, err, "read screen.buffer")
	assert.NotEmpty(t, bufferData, "screen.buffer should not be empty")
	assert.Contains(t, string(bufferData), "save_load_test", "screen.buffer should contain echoed text")

	metaData, err := os.ReadFile(metaPath)
	require.NoError(t, err, "read screen.json")
	var meta savedScreenMeta
	require.NoError(t, json.Unmarshal(metaData, &meta), "unmarshal screen.json")
	assert.Equal(t, uint32(80), meta.Cols, "saved cols")
	assert.Equal(t, uint32(24), meta.Rows, "saved rows")

	// Stop the manager before loading.
	m.StopAll()

	// Load saved terminals from disk.
	loaded, err := LoadSavedTerminals(dataDir)
	require.NoError(t, err, "LoadSavedTerminals")
	require.Contains(t, loaded, termID, "loaded terminals should contain the terminal ID")

	saved := loaded[termID]
	assert.Equal(t, wsID, saved.WorkspaceID, "loaded workspace ID")
	assert.Equal(t, uint32(80), saved.Cols, "loaded cols")
	assert.Equal(t, uint32(24), saved.Rows, "loaded rows")
	assert.NotEmpty(t, saved.Screen, "loaded screen should not be empty")
	assert.Contains(t, string(saved.Screen), "save_load_test", "loaded screen should contain echoed text")

	// Verify that LoadSavedTerminals cleaned up the terminals directory.
	_, err = os.Stat(filepath.Join(dataDir, "workspaces", wsID, "terminals"))
	assert.True(t, os.IsNotExist(err), "terminals dir should be removed after load")
}

func TestLoadSavedTerminals_EmptyDir(t *testing.T) {
	dataDir := t.TempDir()
	// No workspaces directory exists; should return nil, nil.
	result, err := LoadSavedTerminals(dataDir)
	assert.NoError(t, err, "LoadSavedTerminals on empty dir")
	assert.Nil(t, result, "expected nil result for non-existent workspaces dir")
}

func TestLoadSavedTerminals_LegacyCleanup(t *testing.T) {
	dataDir := t.TempDir()

	// Create a legacy screens/ directory.
	legacyDir := filepath.Join(dataDir, "screens")
	require.NoError(t, os.MkdirAll(legacyDir, 0755), "create legacy screens dir")

	// Also create an empty workspaces dir so the function progresses past the ReadDir call.
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "workspaces"), 0755), "create workspaces dir")

	result, err := LoadSavedTerminals(dataDir)
	assert.NoError(t, err, "LoadSavedTerminals with legacy dir")
	assert.Empty(t, result, "expected empty result with no saved terminals")

	// Verify legacy directory was removed.
	_, err = os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(err), "legacy screens/ dir should be removed")
}

// resetShellCache resets the sync.Once so ListAvailableShells recomputes.
func resetShellCache() {
	shellCache.once = sync.Once{}
	shellCache.shells = nil
	shellCache.defaultShell = ""
}

func TestListAvailableShells_ReturnsAtLeastOne(t *testing.T) {
	resetShellCache()
	shells, _ := ListAvailableShells()
	assert.NotEmpty(t, shells, "expected at least one shell to be found")
}

func TestListAvailableShells_DefaultShellSet(t *testing.T) {
	resetShellCache()
	_, defaultShell := ListAvailableShells()
	assert.NotEmpty(t, defaultShell, "expected default shell to be non-empty")
}

func TestListAvailableShells_DefaultShellFirst(t *testing.T) {
	resetShellCache()
	shells, defaultShell := ListAvailableShells()
	require.NotEmpty(t, shells, "expected at least one shell")
	assert.Equal(t, defaultShell, shells[0], "default shell should be the first entry")
}

func TestListAvailableShells_DefaultShellFirst_NonStandardPath(t *testing.T) {
	// Simulate $SHELL pointing to a path not found by LookPath (e.g.
	// /bin/zsh vs /opt/homebrew/bin/zsh).
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "")
	t.Setenv("SHELL", "/usr/local/fake/zsh")
	resetShellCache()
	shells, defaultShell := ListAvailableShells()
	assert.Equal(t, "/usr/local/fake/zsh", defaultShell)
	require.NotEmpty(t, shells)
	assert.Equal(t, "/usr/local/fake/zsh", shells[0], "non-standard default shell should be first")
}

func TestListAvailableShells_NoDuplicateDefaultShell(t *testing.T) {
	resetShellCache()
	shells, defaultShell := ListAvailableShells()
	count := 0
	for _, s := range shells {
		if s == defaultShell {
			count++
		}
	}
	assert.Equal(t, 1, count, "default shell should appear exactly once")
}

func TestListAvailableShells_Cached(t *testing.T) {
	resetShellCache()
	shells1, default1 := ListAvailableShells()
	shells2, default2 := ListAvailableShells()
	assert.Equal(t, shells1, shells2, "cached shells should be identical")
	assert.Equal(t, default1, default2, "cached default shell should be identical")
}

func TestDetectDefaultShell(t *testing.T) {
	shell := detectDefaultShell()
	assert.NotEmpty(t, shell, "detectDefaultShell should return a non-empty string")
	assert.True(t, strings.HasPrefix(shell, "/"), "detectDefaultShell should return an absolute path")
}

func TestResolveDefaultShell_PrefersLeapmuxEnv(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "/bin/test-leapmux-shell")
	t.Setenv("SHELL", "/bin/other-shell")
	shell := resolveDefaultShell()
	assert.Equal(t, "/bin/test-leapmux-shell", shell)
}

func TestResolveDefaultShell_LeapmuxEnvBareName(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "sh")
	t.Setenv("SHELL", "/bin/other-shell")
	shell := resolveDefaultShell()
	assert.NotEmpty(t, shell, "bare name should be resolved")
	assert.True(t, strings.HasPrefix(shell, "/"), "resolved path should be absolute")
	assert.True(t, strings.HasSuffix(shell, "/sh"), "resolved path should end with /sh")
}

func TestResolveDefaultShell_LeapmuxEnvInvalidBareName(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "nonexistent-shell-xyz")
	t.Setenv("SHELL", "/bin/fallback-shell")
	shell := resolveDefaultShell()
	assert.Equal(t, "/bin/fallback-shell", shell, "should fall back to $SHELL when LEAPMUX_DEFAULT_SHELL is unresolvable")
}

func TestResolveDefaultShell_UsesEnvWhenSet(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "")
	t.Setenv("SHELL", "/bin/test-shell")
	shell := resolveDefaultShell()
	assert.Equal(t, "/bin/test-shell", shell, "resolveDefaultShell should prefer $SHELL")
}

func TestResolveDefaultShell_FallsBackWhenEnvUnset(t *testing.T) {
	t.Setenv("LEAPMUX_DEFAULT_SHELL", "")
	t.Setenv("SHELL", "")
	shell := resolveDefaultShell()
	assert.NotEmpty(t, shell, "resolveDefaultShell should return a shell even without $SHELL")
	assert.True(t, strings.HasPrefix(shell, "/"), "resolveDefaultShell should return an absolute path")
}

func TestResolveShellEnv_Empty(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "")
	assert.Equal(t, "", resolveShellEnv("TEST_SHELL_ENV"))
}

func TestResolveShellEnv_AbsolutePath(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "/usr/bin/zsh")
	assert.Equal(t, "/usr/bin/zsh", resolveShellEnv("TEST_SHELL_ENV"))
}

func TestResolveShellEnv_BareNameResolved(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "sh")
	result := resolveShellEnv("TEST_SHELL_ENV")
	assert.NotEmpty(t, result)
	assert.True(t, filepath.IsAbs(result))
}

func TestResolveShellEnv_BareNameNotFound(t *testing.T) {
	t.Setenv("TEST_SHELL_ENV", "nonexistent-shell-xyz")
	assert.Equal(t, "", resolveShellEnv("TEST_SHELL_ENV"))
}
