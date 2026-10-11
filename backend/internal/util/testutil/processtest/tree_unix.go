//go:build unix

// Package processtest supplies native process fixtures for lifetime tests.
package processtest

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/require"
)

const helperRole = "LEAPMUX_TEST_PROCESS_TREE_ROLE"
const helperAddress = "LEAPMUX_TEST_PROCESS_TREE_ADDRESS"

type receipt struct {
	Role     string `json:"role"`
	PID      int    `json:"pid"`
	ChildPID int    `json:"childPID,omitempty"`
	Type     string `json:"type"`
}

// RunHelper runs only inside the current test binary's dedicated helper test.
func RunHelper() {
	role := os.Getenv(helperRole)
	if role != "root" && role != "child" {
		return
	}
	childPID := 0
	if role == "root" {
		child := exec.Command(os.Args[0], Arguments()...)
		child.Env = append(os.Environ(), helperRole+"=child")
		child.Stdout, child.Stderr = io.Discard, io.Discard
		procutil.DetachFromTerminal(child)
		if child.Start() != nil {
			os.Exit(2)
		}
		childPID = child.Process.Pid
	}
	connection, err := net.Dial("tcp", os.Getenv(helperAddress))
	if err != nil {
		os.Exit(3)
	}
	if json.NewEncoder(connection).Encode(receipt{Role: role, PID: os.Getpid(), ChildPID: childPID, Type: "ready"}) != nil {
		os.Exit(4)
	}
	scanner := bufio.NewScanner(connection)
	for scanner.Scan() {
		switch scanner.Text() {
		case "ping":
			if json.NewEncoder(connection).Encode(receipt{Role: role, PID: os.Getpid(), Type: "pong"}) != nil {
				os.Exit(5)
			}
		case "release":
			os.Exit(0)
		default:
			os.Exit(6)
		}
	}
	_ = connection.Close()
	os.Exit(0)
}

func Arguments() []string { return []string{"-test.run=^TestHelperOwnedProcessTree$", "--"} }

// Tree keeps exact root and child identities plus their private control sockets.
type Tree struct {
	listener        net.Listener
	home            string
	Root            procutil.ProcessIdentity
	Child           procutil.ProcessIdentity
	rootConnection  net.Conn
	childConnection net.Conn
	rootReader      *json.Decoder
	childReader     *json.Decoder
}

func New(t *testing.T, home string) *Tree {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tree := &Tree{listener: listener, home: home}
	t.Cleanup(func() {
		_, _ = tree.Child.Kill()
		_, _ = tree.Root.Kill()
		if tree.childConnection != nil {
			_ = tree.childConnection.Close()
		}
		if tree.rootConnection != nil {
			_ = tree.rootConnection.Close()
		}
		_ = listener.Close()
	})
	return tree
}

func (tree *Tree) Environment() []string {
	return []string{"HOME=" + tree.home, helperRole + "=root", helperAddress + "=" + tree.listener.Addr().String()}
}

// Shell writes a launcher that ignores login flags and executes only this test helper.
func (tree *Tree) Shell(t *testing.T) string {
	t.Helper()
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	path := filepath.Join(t.TempDir(), "process-tree-shell")
	script := "#!/bin/sh\nexec " + quote(os.Args[0]) + " -test.run='^TestHelperOwnedProcessTree$' --\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// Ready binds each socket to the root's actual child and verifies its current ancestry.
func (tree *Tree) Ready(t *testing.T, rootPID int) {
	t.Helper()
	var rootExists bool
	tree.Root, rootExists = procutil.IdentifyProcess(rootPID)
	require.True(t, rootExists)
	var declaredChild int
	for range 2 {
		type accepted struct {
			connection net.Conn
			err        error
		}
		results := make(chan accepted, 1)
		go func() {
			connection, err := tree.listener.Accept()
			results <- accepted{connection: connection, err: err}
		}()
		var connection net.Conn
		select {
		case result := <-results:
			require.NoError(t, result.err)
			connection = result.connection
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("the process tree did not connect")
		}
		t.Cleanup(func() { _ = connection.Close() })
		require.NoError(t, connection.SetDeadline(time.Now().Add(30*time.Second)))
		reader := json.NewDecoder(connection)
		var message receipt
		require.NoError(t, reader.Decode(&message))
		require.Equal(t, "ready", message.Type)
		switch message.Role {
		case "root":
			require.Equal(t, rootPID, message.PID)
			var exists bool
			tree.Root, exists = procutil.IdentifyProcess(rootPID)
			require.True(t, exists)
			declaredChild = message.ChildPID
			tree.rootConnection, tree.rootReader = connection, reader
		case "child":
			var exists bool
			tree.Child, exists = procutil.IdentifyProcess(message.PID)
			require.True(t, exists)
			tree.childConnection, tree.childReader = connection, reader
		default:
			t.Fatalf("the process tree sent an unknown role: %q", message.Role)
		}
	}
	require.Equal(t, declaredChild, tree.Child.PID)
	parent, err := (&process.Process{Pid: int32(tree.Child.PID)}).PpidWithContext(t.Context())
	require.NoError(t, err)
	require.Equal(t, int32(rootPID), parent)
}

func (tree *Tree) RequirePong(t *testing.T) {
	t.Helper()
	for _, entry := range []struct {
		connection net.Conn
		reader     *json.Decoder
		role       string
		identity   procutil.ProcessIdentity
	}{
		{tree.rootConnection, tree.rootReader, "root", tree.Root},
		{tree.childConnection, tree.childReader, "child", tree.Child},
	} {
		require.NoError(t, entry.connection.SetDeadline(time.Now().Add(30*time.Second)))
		_, err := io.WriteString(entry.connection, "ping\n")
		require.NoError(t, err)
		var message receipt
		require.NoError(t, entry.reader.Decode(&message))
		require.Equal(t, receipt{Role: entry.role, PID: entry.identity.PID, Type: "pong"}, message)
		require.True(t, entry.identity.Runs())
	}
}

func (tree *Tree) RequireChildClosed(t *testing.T) {
	t.Helper()
	require.NoError(t, tree.childConnection.SetDeadline(time.Now().Add(30*time.Second)))
	_, _ = io.WriteString(tree.childConnection, "ping\n")
	var message receipt
	err := tree.childReader.Decode(&message)
	if err == nil {
		t.Fatalf("the exact detached child still answers after root cleanup: %+v", message)
	}
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET), "the child's control socket must close: %v", err)
}
