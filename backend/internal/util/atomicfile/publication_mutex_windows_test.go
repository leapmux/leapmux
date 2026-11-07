//go:build windows

package atomicfile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/procutil"
)

func TestHelperWindowsPublication(t *testing.T) {
	target := os.Getenv("LEAPMUX_TEST_PUBLICATION_TARGET")
	if target == "" {
		return
	}
	publishExclusive = func(from, to string) error {
		if err := renameNoReplace(from, to); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(os.Stdout, "published"); err != nil {
			return err
		}
		_, err := io.ReadFull(os.Stdin, make([]byte, 1))
		return err
	}
	if err := MkdirAllDurable(target); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestWindowsPublicationCoordinatesLiveAndKilledCreators(t *testing.T) {
	for _, kill := range []bool{false, true} {
		t.Run(fmt.Sprintf("killed=%t", kill), func(t *testing.T) {
			withOps(t, ops)
			root := filepath.Join(canonicalTempDir(t), "published")
			command := exec.CommandContext(testutil.DeadlineContext(t), os.Args[0], "-test.run=^TestHelperWindowsPublication$")
			command.Env = append(os.Environ(), "LEAPMUX_TEST_PUBLICATION_TARGET="+root)
			output, err := command.StdoutPipe()
			require.NoError(t, err)
			input, err := command.StdinPipe()
			require.NoError(t, err)
			var diagnostics testutil.LogBuffer
			command.Stderr = &diagnostics
			owner := procutil.PrepareProcess(command)
			require.NoError(t, owner.Start())
			t.Cleanup(func() { _ = owner.Terminate(); _ = owner.Wait() })
			line, err := bufio.NewReader(output).ReadString('\n')
			require.NoError(t, err, diagnostics.String())
			require.Equal(t, "published", strings.TrimSpace(line))
			before, err := os.ReadFile(recordPathAt(root))
			require.NoError(t, err)
			require.Contains(t, string(before), "phase: prepared")
			waiting := make(chan struct{})
			var once sync.Once
			previous := waitPublicationMutex
			waitPublicationMutex = func(handle windows.Handle, milliseconds uint32) (uint32, error) {
				once.Do(func() { close(waiting) })
				return previous(handle, milliseconds)
			}
			t.Cleanup(func() { waitPublicationMutex = previous })
			done := make(chan struct{})
			var contenderErr error
			deeper := filepath.Join(root, "worker")
			go func() { contenderErr = MkdirAllDurable(deeper); close(done) }()
			t.Cleanup(func() { _ = owner.Terminate(); <-done })
			select {
			case <-waiting:
			case <-done:
				t.Fatalf("the contender did not wait for the live creator: %v", contenderErr)
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the contender never reached the publication mutex")
			}
			select {
			case <-done:
				t.Fatalf("the contender returned before publication confirmation: %v", contenderErr)
			default:
			}
			if kill {
				require.NoError(t, owner.Terminate())
				require.Error(t, owner.Wait())
			} else {
				_, err = input.Write([]byte{1})
				require.NoError(t, err)
				require.NoError(t, owner.Wait(), diagnostics.String())
			}
			<-done
			if kill {
				require.ErrorIs(t, contenderErr, ErrDirectoryPublicationUncertain)
				after, err := os.ReadFile(recordPathAt(root))
				require.NoError(t, err)
				assert.Equal(t, before, after)
				assert.NoDirExists(t, deeper)
			} else {
				require.NoError(t, contenderErr)
				record, err := loadRecordAt(recordPathAt(root))
				require.NoError(t, err)
				assert.Equal(t, phaseCommitted, record.Phase)
				assert.DirExists(t, deeper)
			}
		})
	}
}

func TestWindowsPublicationMutexWaitOutcomes(t *testing.T) {
	for _, outcome := range []string{"success", "abandoned", "timeout", "failure", "unexpected"} {
		t.Run(outcome, func(t *testing.T) {
			withOps(t, ops)
			root := canonicalTempDir(t)
			previous := waitPublicationMutex
			t.Cleanup(func() { waitPublicationMutex = previous })
			waitPublicationMutex = func(handle windows.Handle, milliseconds uint32) (uint32, error) {
				assert.Equal(t, uint32(publicationWaitMilliseconds), milliseconds)
				switch outcome {
				case "timeout":
					return uint32(windows.WAIT_TIMEOUT), nil
				case "failure":
					return 0, assert.AnError
				case "unexpected":
					return 42, nil
				}
				state, err := previous(handle, milliseconds)
				if err == nil && state == windows.WAIT_OBJECT_0 && outcome == "abandoned" {
					return windows.WAIT_ABANDONED, nil
				}
				return state, err
			}
			called := false
			err := withPublicationMutex(root, func() error { called = true; return nil })
			switch outcome {
			case "success", "abandoned":
				require.NoError(t, err)
				assert.True(t, called)
			case "failure":
				require.ErrorIs(t, err, assert.AnError)
				assert.False(t, called)
			default:
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrDirectoryPublicationUncertain)
				assert.False(t, called)
			}
			waitPublicationMutex = previous
			require.NoError(t, withPublicationMutex(root, func() error { return nil }), "all owned handles and mutexes were released")
		})
	}
}

func TestWindowsPublicationMutexRejectsBroadAccess(t *testing.T) {
	account := currentAccountID()
	root := canonicalTempDir(t)
	name, err := windows.UTF16PtrFromString(publicationMutexName(account, root))
	require.NoError(t, err)
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:P(A;;GA;;;%s)(A;;GA;;;WD)", account, account))
	require.NoError(t, err)
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateMutexEx(&attributes, name, 0, windows.MUTEX_ALL_ACCESS)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, windows.CloseHandle(handle)) })
	err = withPublicationMutex(root, func() error { t.Fatal("a broad mutex must not admit publication"); return nil })
	require.ErrorContains(t, err, "not private")
}

func TestWindowsPublicationMutexIdentity(t *testing.T) {
	name := publicationMutexName("account", `C:\Data\LeapMux`)
	assert.True(t, strings.HasPrefix(name, `Global\`))
	assert.Equal(t, name, publicationMutexName("account", `c:\data\leapmux`))
	assert.NotEqual(t, name, publicationMutexName("other-account", `C:\Data\LeapMux`))
	assert.NotEqual(t, name, publicationMutexName("account", `C:\Data\Other`))
}

func TestWindowsPublicationMutexRejectsAnotherObjectType(t *testing.T) {
	root := canonicalTempDir(t)
	name, err := windows.UTF16PtrFromString(publicationMutexName(currentAccountID(), root))
	require.NoError(t, err)
	handle, err := windows.CreateEvent(nil, 0, 0, name)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, windows.CloseHandle(handle)) })
	err = withPublicationMutex(root, func() error { t.Fatal("another object type must not admit publication"); return nil })
	require.ErrorIs(t, err, windows.ERROR_INVALID_HANDLE)
}
