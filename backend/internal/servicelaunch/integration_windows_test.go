//go:build windows

package servicelaunch

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/leapmux/leapmux/internal/servicelaunch/servicefixture"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/lifecycle"
)

func nativeIntegrationManager(_ *testing.T, fixture servicefixture.Account, binary string) Manager {
	provider := func(context.Context, string) ([]byte, error) {
		if fixture.CredentialReference == nil {
			return nil, fmt.Errorf("the Windows fixture requires an OS credential reference")
		}
		return passwordFromCredentialStore(*fixture.CredentialReference, fixture.AccountID)
	}
	elevate := func(ctx context.Context, request nativeOperationRequest) error {
		return runElevatedOperationWithHelper(ctx, binary, request)
	}
	service := newWindowsServiceManager()
	service.credential = provider
	service.elevate = elevate
	task := newWindowsTaskManager(provider)
	task.elevate = elevate
	return &windowsAdapter{task: task, service: service}
}

func assertForeignNativeControlDenied(t *testing.T, url string) {
	t.Helper()
	name := strings.TrimPrefix(url, "npipe:")
	if !strings.HasPrefix(name, `\\.\pipe\`) {
		name = `\\.\pipe\` + name
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ownerConnection, err := winio.DialPipeContext(ctx, name)
	require.NoError(t, err)
	file, ok := ownerConnection.(interface{ Fd() uintptr })
	require.True(t, ok)
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	acl, _, err := descriptor.DACL()
	require.NoError(t, err)
	require.NotNil(t, acl)
	require.Equal(t, uint16(1), acl.AceCount, "the control pipe must grant no other account or group")
	var ace *windows.ACCESS_ALLOWED_ACE
	require.NoError(t, windows.GetAce(acl, 0, &ace))
	require.Equal(t, byte(windows.ACCESS_ALLOWED_ACE_TYPE), ace.Header.AceType)
	require.Equal(t, lifecycle.CurrentAccountID(), (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String())
	require.NoError(t, ownerConnection.Close())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	impersonate := windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateAnonymousToken")
	success, _, nativeErr := impersonate.Call(uintptr(windows.CurrentThread()))
	require.NotZero(t, success, "anonymous impersonation: %v", nativeErr)
	defer func() { require.NoError(t, windows.RevertToSelf()) }()
	connection, err := winio.DialPipeContext(ctx, name)
	if connection != nil {
		_ = connection.Close()
	}
	require.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED, "another account must not open the private control pipe")
}
