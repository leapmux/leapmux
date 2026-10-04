package zcode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const zcodeStoragePeerEnv = "LEAPMUX_ZCODE_STORAGE_PEER"

func TestZCodeStoragePeer(t *testing.T) {
	if os.Getenv(zcodeStoragePeerEnv) != "1" {
		return
	}
	scenario := os.Getenv("LEAPMUX_ZCODE_STORAGE_PEER_SCENARIO")
	path := os.Getenv("LEAPMUX_ZCODE_STORAGE_PEER_PATH")
	write := func(method string, params any) {
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"method": method, "params": params}); err != nil {
			os.Exit(20)
		}
	}
	awaitClose := func() {
		bufio.NewScanner(os.Stdin).Scan()
		os.Exit(0)
	}
	switch scenario {
	case "prepared-first":
		write(zcodeStoragePreparedMethod, map[string]any{})
		awaitClose()
	case "malformed":
		if _, err := fmt.Fprintln(os.Stdout, "{invalid native JSON"); err != nil {
			os.Exit(20)
		}
		awaitClose()
	case "excess-output":
		if _, err := fmt.Fprintln(os.Stdout, strings.Repeat("x", zcodeStorageQueryOutput+1)); err != nil {
			os.Exit(20)
		}
		awaitClose()
	case "wrong-path-type":
		write(zcodeStoragePathMethod, map[string]any{"path": false})
		awaitClose()
	case "absent-path":
		write(zcodeStoragePathMethod, map[string]any{})
		awaitClose()
	case "unknown-record":
		write("startup/nativeUnknown", map[string]any{})
		awaitClose()
	}
	write(zcodeStoragePathMethod, map[string]any{"path": path})
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(21)
	}
	var acknowledgement struct {
		Method string `json:"method"`
		Reuse  bool   `json:"reuse"`
	}
	if json.Unmarshal(scanner.Bytes(), &acknowledgement) != nil || acknowledgement.Method != zcodeStorageReadyMethod || !acknowledgement.Reuse {
		os.Exit(22)
	}
	if receipt := os.Getenv("LEAPMUX_ZCODE_STORAGE_PEER_RECEIPT"); receipt != "" {
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(23)
		}
		data, err := json.Marshal(map[string]any{"acknowledgement": acknowledgement, "args": os.Args, "workingDir": cwd,
			"home": os.Getenv("HOME"), "first": os.Getenv("LEAPMUX_ZCODE_STORAGE_TEST_FIRST"), "second": os.Getenv("LEAPMUX_ZCODE_STORAGE_TEST_SECOND")})
		if config, readErr := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".zcode", "cli", "config.json")); readErr == nil {
			var record map[string]json.RawMessage
			if json.Unmarshal(data, &record) != nil {
				os.Exit(29)
			}
			record["config"], err = json.Marshal(string(config))
			if err == nil {
				data, err = json.Marshal(record)
			}
		}
		if err != nil || os.WriteFile(receipt, data, 0o600) != nil {
			os.Exit(24)
		}
	}
	if scenario == "wait-for-cancel" {
		connection, err := net.Dial("tcp", os.Getenv("LEAPMUX_ZCODE_STORAGE_PEER_READY"))
		if err != nil {
			os.Exit(25)
		}
		control := bufio.NewScanner(connection)
		for control.Scan() {
			switch control.Text() {
			case "ping":
				if _, err := fmt.Fprintln(connection, "pong"); err != nil {
					os.Exit(26)
				}
			case "finish":
				write(zcodeStoragePreparedMethod, map[string]any{})
				if err := connection.Close(); err != nil {
					os.Exit(26)
				}
				os.Exit(0)
			default:
				os.Exit(27)
			}
		}
		os.Exit(27)
	}
	if scenario == "duplicate-path" {
		write(zcodeStoragePathMethod, map[string]any{"path": path})
	}
	if scenario != "missing-prepared" {
		write(zcodeStoragePreparedMethod, map[string]any{})
	}
	if scenario == "duplicate-prepared" {
		write(zcodeStoragePreparedMethod, map[string]any{})
	}
	if scenario == "exit-failure" {
		fmt.Fprintln(os.Stderr, "native controlled storage exit failed")
		os.Exit(28)
	}
	os.Exit(0)
}

func zcodeStoragePeerCommand(ctx context.Context, q agent.StoredSessionQuery, path, scenario, receipt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZCodeStoragePeer$", "--", "app-server", "--stdio", "--prepare-storage")
	cmd.Env = []string{zcodeStoragePeerEnv + "=1", "LEAPMUX_ZCODE_STORAGE_PEER_PATH=" + path,
		"LEAPMUX_ZCODE_STORAGE_PEER_SCENARIO=" + scenario, "LEAPMUX_ZCODE_STORAGE_PEER_RECEIPT=" + receipt,
		"HOME=" + q.Home(), "USERPROFILE=" + q.Home()}
	if q.WorkingDir != "" {
		if info, err := os.Stat(q.WorkingDir); err == nil && info.IsDir() {
			cmd.Dir = q.WorkingDir
		}
	}
	return cmd
}

func newZCodeTestStorageQuery(path, scenario, receipt string) zcodeNativeStorageQuery {
	return zcodeNativeStorageQuery{command: func(ctx context.Context, q agent.StoredSessionQuery) (zcodeStorageCommand, error) {
		return zcodeStorageCommand{command: zcodeStoragePeerCommand(ctx, q, path, scenario, receipt)}, nil
	}}
}

type zcodeTestHomeStorageQuery struct{}

func (zcodeTestHomeStorageQuery) DatabasePath(ctx context.Context, q agent.StoredSessionQuery) (string, error) {
	return newZCodeTestStorageQuery(filepath.Join(q.Home(), ".zcode", "cli", "db", "db.sqlite"), "", "").DatabasePath(ctx, q)
}

func TestZCodeStorageQueryRequiresTheNativePathAndPreparedSequence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "authoritative-native.db")
	cases := []struct {
		name     string
		scenario string
		path     string
	}{
		{name: "valid native sequence", path: path},
		{name: "prepared before path", scenario: "prepared-first", path: path},
		{name: "repeated path", scenario: "duplicate-path", path: path},
		{name: "repeated acknowledgement", scenario: "duplicate-prepared", path: path},
		{name: "missing acknowledgement", scenario: "missing-prepared", path: path},
		{name: "invalid JSON", scenario: "malformed", path: path},
		{name: "wrong path type", scenario: "wrong-path-type", path: path},
		{name: "absent path", scenario: "absent-path", path: path},
		{name: "relative path", path: "relative.db"},
		{name: "empty path"},
		{name: "unclean path", path: filepath.Dir(path) + string(filepath.Separator) + "." + string(filepath.Separator) + "native.db"},
		{name: "native exit failure", scenario: "exit-failure", path: path},
		{name: "excess output", scenario: "excess-output", path: path},
		{name: "unknown record", scenario: "unknown-record", path: path},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			query := newZCodeTestStorageQuery(tc.path, tc.scenario, "")
			got, err := query.DatabasePath(t.Context(), agent.StoredSessionQuery{HomeDir: t.TempDir()})
			if tc.name == "valid native sequence" {
				require.NoError(t, err)
				assert.Equal(t, path, got)
				_, err = os.Stat(path)
				assert.ErrorIs(t, err, os.ErrNotExist, "reuse does not create or open a database")
			} else {
				require.Error(t, err)
				assert.Empty(t, got, "a partial native receipt supplies no path authority")
			}
		})
	}
}

func TestZCodeStorageQueryPreservesItsRealLaunchArgumentsAndEnvironment(t *testing.T) {
	t.Parallel()
	home, directory := t.TempDir(), t.TempDir()
	path := filepath.Join(home, "native-query.db")
	receipt := filepath.Join(home, "query-receipt.json")
	spec := launch.Spec{Program: os.Args[0], PrefixArgs: []string{"-test.run=^TestZCodeStoragePeer$", "--"}}
	q := agent.StoredSessionQuery{HomeDir: home, WorkingDir: directory, Shell: terminal.ResolveDefaultShell(), EnvEntries: []string{
		"PATH=" + os.Getenv("PATH"), "ZDOTDIR=" + home, "HOME=" + home, "USERPROFILE=" + home, zcodeStoragePeerEnv + "=1", "LEAPMUX_ZCODE_STORAGE_PEER_PATH=" + path,
		"LEAPMUX_ZCODE_STORAGE_PEER_RECEIPT=" + receipt, "LEAPMUX_ZCODE_STORAGE_TEST_FIRST=older", "LEAPMUX_ZCODE_STORAGE_TEST_SECOND=second",
		"LEAPMUX_ZCODE_STORAGE_TEST_FIRST=newer",
	}}
	got, err := newZCodeStorageQuery(&spec).DatabasePath(t.Context(), q)
	require.NoError(t, err)
	assert.Equal(t, path, got)
	data, err := os.ReadFile(receipt)
	require.NoError(t, err)
	var recorded struct {
		Args            []string `json:"args"`
		WorkingDir      string   `json:"workingDir"`
		Home            string   `json:"home"`
		First           string   `json:"first"`
		Second          string   `json:"second"`
		Acknowledgement struct {
			Method string `json:"Method"`
			Reuse  bool   `json:"Reuse"`
		} `json:"acknowledgement"`
	}
	require.NoError(t, json.Unmarshal(data, &recorded))
	assert.Equal(t, []string{"app-server", "--stdio", "--prepare-storage"}, recorded.Args[len(recorded.Args)-3:])
	wantDirectory, err := filepath.EvalSymlinks(directory)
	require.NoError(t, err)
	assert.Equal(t, wantDirectory, recorded.WorkingDir)
	assert.Equal(t, home, recorded.Home)
	assert.Equal(t, "newer", recorded.First)
	assert.Equal(t, "second", recorded.Second)
	assert.Equal(t, zcodeStorageReadyMethod, recorded.Acknowledgement.Method)
	assert.True(t, recorded.Acknowledgement.Reuse)
}

func TestZCodeStorageQueryCancelsAnUnfinishedNativePeer(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	guard, stopGuard := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopGuard()
	tcpListener, valid := listener.(*net.TCPListener)
	require.True(t, valid)
	require.NoError(t, tcpListener.SetDeadline(time.Now().Add(30*time.Second)))
	ctx, cancel := context.WithCancel(guard)
	defer cancel()
	query := newZCodeTestStorageQuery(filepath.Join(t.TempDir(), "native.db"), "wait-for-cancel", "")
	makeCommand := query.command
	query.command = func(ctx context.Context, q agent.StoredSessionQuery) (zcodeStorageCommand, error) {
		command, err := makeCommand(ctx, q)
		if err == nil {
			command.command.Env = append(command.command.Env, "LEAPMUX_ZCODE_STORAGE_PEER_READY="+listener.Addr().String())
		}
		return command, err
	}
	result := make(chan error, 1)
	home := t.TempDir()
	go func() { _, err := query.DatabasePath(ctx, agent.StoredSessionQuery{HomeDir: home}); result <- err }()
	connection, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	cancel()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-guard.Done():
		t.Fatal("The canceled native storage query did not finish.")
	}
}

func TestZCodeStorageQueryCancellationPreservesAnotherNativePeer(t *testing.T) {
	t.Parallel()
	guard, stopGuard := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopGuard()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	tcpListener, valid := listener.(*net.TCPListener)
	require.True(t, valid)
	require.NoError(t, tcpListener.SetDeadline(time.Now().Add(30*time.Second)))
	type result struct {
		path string
		err  error
	}
	start := func(ctx context.Context, path string) (<-chan result, net.Conn) {
		home := t.TempDir()
		query := newZCodeTestStorageQuery(path, "wait-for-cancel", "")
		makeCommand := query.command
		query.command = func(ctx context.Context, q agent.StoredSessionQuery) (zcodeStorageCommand, error) {
			command, err := makeCommand(ctx, q)
			if err == nil {
				command.command.Env = append(command.command.Env, "LEAPMUX_ZCODE_STORAGE_PEER_READY="+listener.Addr().String())
			}
			return command, err
		}
		completed := make(chan result, 1)
		go func() {
			path, err := query.DatabasePath(ctx, agent.StoredSessionQuery{HomeDir: home})
			completed <- result{path: path, err: err}
		}()
		connection, err := listener.Accept()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })
		require.NoError(t, connection.SetDeadline(time.Now().Add(30*time.Second)))
		return completed, connection
	}
	firstContext, cancelFirst := context.WithCancel(guard)
	defer cancelFirst()
	first, _ := start(firstContext, filepath.Join(t.TempDir(), "first.db"))
	secondPath := filepath.Join(t.TempDir(), "second.db")
	second, secondConnection := start(guard, secondPath)
	cancelFirst()
	select {
	case outcome := <-first:
		require.Error(t, outcome.err)
		assert.Empty(t, outcome.path)
	case <-guard.Done():
		t.Fatal("The first canceled native storage query did not finish.")
	}
	_, err = fmt.Fprintln(secondConnection, "ping")
	require.NoError(t, err)
	answer, err := bufio.NewReader(secondConnection).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "pong\n", answer, "canceling the first owned query must preserve the second peer")
	_, err = fmt.Fprintln(secondConnection, "finish")
	require.NoError(t, err)
	select {
	case outcome := <-second:
		require.NoError(t, outcome.err)
		assert.Equal(t, secondPath, outcome.path)
	case <-guard.Done():
		t.Fatal("The second native storage query did not finish its acknowledged preparation.")
	}
}

func TestZCodeStorageQueryKeepsTheFinalizedProcessEnvironment(t *testing.T) {
	t.Parallel()
	actualHome, queryHome, locatorHome := t.TempDir(), t.TempDir(), t.TempDir()
	directory := t.TempDir()
	path := filepath.Join(actualHome, "native-finalized.db")
	receipt := filepath.Join(actualHome, "query-receipt.json")
	spec := launch.Spec{
		Program: os.Args[0], PrefixArgs: []string{"-test.run=^TestZCodeStoragePeer$", "--"},
		Env: []string{"HOME=" + locatorHome, "USERPROFILE=" + locatorHome, "LEAPMUX_ZCODE_STORAGE_TEST_FIRST=locator-value"},
	}
	q := agent.StoredSessionQuery{
		HomeDir: queryHome, WorkingDir: directory, Shell: terminal.ResolveDefaultShell(),
		EnvEntries: []string{
			"PATH=" + os.Getenv("PATH"), "ZDOTDIR=" + actualHome, "HOME=" + actualHome, "USERPROFILE=" + actualHome,
			zcodeStoragePeerEnv + "=1", "LEAPMUX_ZCODE_STORAGE_PEER_PATH=" + path,
			"LEAPMUX_ZCODE_STORAGE_PEER_RECEIPT=" + receipt, "LEAPMUX_ZCODE_STORAGE_TEST_FIRST=finalized-value",
		},
	}
	_, err := newZCodeStorageQuery(&spec).DatabasePath(t.Context(), q)
	require.NoError(t, err)
	data, err := os.ReadFile(receipt)
	require.NoError(t, err)
	var observed struct {
		Home  string `json:"home"`
		First string `json:"first"`
	}
	require.NoError(t, json.Unmarshal(data, &observed))
	assert.Equal(t, actualHome, observed.Home, "the storage query must use the main process's finalized HOME")
	assert.Equal(t, "finalized-value", observed.First, "the locator must not overwrite finalized ExtraEnv values")
}

func TestZCodeStorageQueryUsesTheProvidedRuntimeLocator(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(home, "controlled-native.db")
	locator := launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: os.Args[0], PrefixArgs: []string{"-test.run=^TestZCodeStoragePeer$", "--"}}, launch.Found
	})
	q := agent.StoredSessionQuery{HomeDir: home, WorkingDir: t.TempDir(), RuntimeLocator: &locator, Shell: terminal.ResolveDefaultShell(),
		EnvEntries: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home,
			zcodeStoragePeerEnv + "=1", "LEAPMUX_ZCODE_STORAGE_PEER_PATH=" + path}}
	got, err := newZCodeStorageQuery(nil).DatabasePath(t.Context(), q)
	require.NoError(t, err)
	assert.Equal(t, path, got)
	q.RuntimeLocator = new(launch.Locator)
	got, err = newZCodeStorageQuery(nil).DatabasePath(t.Context(), q)
	require.ErrorContains(t, err, "invalid runtime locator")
	assert.Empty(t, got)
}

func TestZCodeStorageQueryRejectsAbsentInputsAndStartupFailure(t *testing.T) {
	t.Parallel()
	var absentContext context.Context
	_, err := newZCodeTestStorageQuery("", "", "").DatabasePath(absentContext, agent.StoredSessionQuery{})
	require.Error(t, err)
	_, err = (zcodeNativeStorageQuery{}).DatabasePath(t.Context(), agent.StoredSessionQuery{})
	require.Error(t, err)
	query := zcodeNativeStorageQuery{command: func(context.Context, agent.StoredSessionQuery) (zcodeStorageCommand, error) {
		return zcodeStorageCommand{}, errors.New("controlled native command failure")
	}}
	_, err = query.DatabasePath(t.Context(), agent.StoredSessionQuery{})
	require.EqualError(t, err, "controlled native command failure")
	query.command = func(ctx context.Context, _ agent.StoredSessionQuery) (zcodeStorageCommand, error) {
		return zcodeStorageCommand{command: exec.CommandContext(ctx, filepath.Join(t.TempDir(), "absent-native-command"))}, nil
	}
	_, err = query.DatabasePath(t.Context(), agent.StoredSessionQuery{})
	require.Error(t, err)
	assert.False(t, validZCodeStoragePath("/native/invalid\x00file"))
}
