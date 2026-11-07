//go:build windows

package servicelaunch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/lifecycle"
)

const nativeHelperPipePrefix = `\\.\pipe\leapmux-native-`
const nativeHelperFlag = "--native-service-helper"

type nativeOperation string

const (
	nativeInstallService  nativeOperation = "install-service"
	nativeRemoveService   nativeOperation = "remove-service"
	nativeGrantBatchRight nativeOperation = "grant-batch-right"
	nativeInstallTask     nativeOperation = "install-task"
	nativeRemoveTask      nativeOperation = "remove-task"
)

type nativeOperationRequest struct {
	Operation nativeOperation `json:"operation"`
	Spec      LaunchSpec      `json:"spec"`
	Password  []byte          `json:"password"`
}

type nativeOperationReply struct {
	Code  uint32 `json:"code"`
	Error string `json:"error"`
}

type windowsOperationError struct {
	code    uint32
	message string
}

func (e *windowsOperationError) Error() string      { return e.message }
func (e *windowsOperationError) ErrorCode() uintptr { return uintptr(e.code) }
func (e *windowsOperationError) Unwrap() error {
	if e.code == 0 {
		return nil
	}
	return syscall.Errno(e.code)
}

// runElevatedOperation passes one request through an account/admin-restricted
// pipe. The process command line carries only the pipe name and parent process
// identity, including its PID and creation time.
func runElevatedOperation(ctx context.Context, request nativeOperationRequest) (result error) {
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	return runElevatedOperationWithHelper(ctx, helper, request)
}

func runElevatedOperationWithHelper(ctx context.Context, helper string, request nativeOperationRequest) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNativeOperation(request, lifecycle.CurrentAccountID()); err != nil {
		return err
	}
	// Native definitions consume the private specification at launch. The
	// privileged helper needs no provider environment or runtime overrides.
	request.Spec.RuntimeArguments = nil
	request.Spec.RuntimeEnvironment = nil
	request.Spec.EnvironmentFile = nil
	if windows.GetCurrentProcessToken().IsElevated() {
		return executeNativeOperation(ctx, request)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	pipe := nativeHelperPipePrefix + hex.EncodeToString(nonce[:])
	sid := lifecycle.CurrentAccountID()
	listener, err := winio.ListenPipe(pipe, &winio.PipeConfig{
		SecurityDescriptor: "O:" + sid + "G:" + sid + "D:P(D;;GA;;;NU)(A;;GA;;;" + sid + ")(A;;GA;;;BA)(A;;GA;;;SY)",
		InputBufferSize:    65536, OutputBufferSize: 65536,
	})
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, listener.Close()) }()
	process, err := launchElevatedHelper(helper, pipe, os.Getpid())
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(process)) }()
	// Authorization finishes before this deadline begins.
	operationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		result = errors.Join(result, waitNativeHelper(cleanupCtx, process))
	}()
	expected, err := windows.GetProcessId(process)
	if err != nil {
		return err
	}
	stopAccept := context.AfterFunc(operationCtx, func() { _ = listener.Close() })
	defer stopAccept()
	var connection net.Conn
	for {
		connection, err = listener.Accept()
		if err != nil {
			return errors.Join(err, operationCtx.Err())
		}
		peer, err := pipePeerProcess(connection, true)
		if err == nil && peer == expected {
			break
		}
		_ = connection.Close()
		if err != nil {
			return err
		}
	}
	defer func() { result = errors.Join(result, connection.Close()) }()
	stopIO := context.AfterFunc(operationCtx, func() { _ = connection.Close() })
	defer stopIO()
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return errors.Join(err, operationCtx.Err())
	}
	var reply nativeOperationReply
	if err := json.NewDecoder(io.LimitReader(connection, 65536)).Decode(&reply); err != nil {
		return errors.Join(err, operationCtx.Err())
	}
	if reply.Error != "" {
		return &windowsOperationError{code: reply.Code, message: reply.Error}
	}
	return nil
}

func pipePeerProcess(connection net.Conn, client bool) (uint32, error) {
	file, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return 0, fmt.Errorf("the native pipe does not expose its authenticated handle")
	}
	var process uint32
	var err error
	if client {
		err = windows.GetNamedPipeClientProcessId(windows.Handle(file.Fd()), &process)
	} else {
		err = windows.GetNamedPipeServerProcessId(windows.Handle(file.Fd()), &process)
	}
	if err != nil {
		return 0, err
	}
	if process == 0 {
		return 0, fmt.Errorf("the native pipe peer has no process identity")
	}
	return process, nil
}

func validateNativeOperation(request nativeOperationRequest, account string) error {
	spec := request.Spec
	spec.EnvironmentFile = nil // The helper does not read provider secrets.
	if err := ValidateLaunchSpec(spec); err != nil {
		return err
	}
	canonical, err := lifecycle.CanonicalDir(spec.DataDirectory)
	if err != nil {
		return err
	}
	if account != spec.AccountID || canonical != spec.DataDirectory || spec.InstanceID != lifecycle.NewInstanceID(account, spec.Mode, canonical).String() {
		return fmt.Errorf("the native request does not match its originating account and data directory")
	}
	if err := verifyWindowsAccount(spec); err != nil {
		return err
	}
	switch request.Operation {
	case nativeInstallService, nativeRemoveService:
		if spec.Startup != lifecycle.StartupBoot {
			return fmt.Errorf("a Windows service requires boot startup")
		}
	case nativeGrantBatchRight, nativeInstallTask, nativeRemoveTask:
		if spec.Startup != lifecycle.StartupLogin {
			return fmt.Errorf("the batch logon right requires login startup")
		}
	default:
		return fmt.Errorf("the native operation is not supported")
	}
	installation := request.Operation == nativeInstallService || request.Operation == nativeInstallTask
	if installation && len(request.Password) == 0 {
		return fmt.Errorf("service setup requires the selected account password")
	}
	if !installation && len(request.Password) != 0 {
		return fmt.Errorf("this native operation does not accept a password")
	}
	return nil
}

func executeNativeOperation(ctx context.Context, request nativeOperationRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch request.Operation {
	case nativeInstallService:
		if err := grantWindowsLogonRight(request.Spec, "SeServiceLogonRight"); err != nil {
			return err
		}
		if err := validateWindowsLogon(request.Spec, request.Password, 5); err != nil {
			return err
		}
		return upsertWindowsService(ctx, request.Spec, request.Password)
	case nativeRemoveService:
		return removeWindowsService(ctx, request.Spec)
	case nativeGrantBatchRight:
		return grantWindowsLogonRight(request.Spec, "SeBatchLogonRight")
	case nativeInstallTask:
		if err := grantWindowsLogonRight(request.Spec, "SeBatchLogonRight"); err != nil {
			return err
		}
		return upsertWindowsTask(ctx, request.Spec, request.Password)
	case nativeRemoveTask:
		return removeWindowsTask(ctx, request.Spec)
	default:
		return fmt.Errorf("the native operation is not supported")
	}
}

// RunNativeHelper handles only the private elevation entry. Ordinary command
// dispatch and agent helpers keep their existing entrypoints.
func RunNativeHelper(args []string) (bool, error) {
	if len(args) == 0 || args[0] != nativeHelperFlag {
		return false, nil
	}
	if len(args) != 6 || args[2] != "--parent-pid" || args[4] != "--parent-start" || !strings.HasPrefix(args[1], nativeHelperPipePrefix) {
		return true, fmt.Errorf("invalid native helper invocation")
	}
	suffix := strings.TrimPrefix(args[1], nativeHelperPipePrefix)
	if decoded, err := hex.DecodeString(suffix); err != nil || len(decoded) != 16 {
		return true, fmt.Errorf("invalid native helper pipe")
	}
	parent, err := strconv.ParseUint(args[3], 10, 32)
	if err != nil || parent == 0 {
		return true, fmt.Errorf("invalid native helper parent")
	}
	parentStart, err := strconv.ParseUint(args[5], 10, 64)
	if err != nil || parentStart == 0 {
		return true, fmt.Errorf("invalid native helper parent creation time")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	connection, err := winio.DialPipeContext(ctx, args[1])
	if err != nil {
		return true, err
	}
	defer func() { _ = connection.Close() }()
	peer, err := pipePeerProcess(connection, false)
	if err != nil || peer != uint32(parent) {
		return true, fmt.Errorf("the native pipe server does not match the invoking process")
	}
	account, err := nativeParentAccount(peer, parentStart)
	if err != nil {
		return true, err
	}
	stopIO := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopIO()
	decoder := json.NewDecoder(io.LimitReader(connection, 16<<20))
	decoder.DisallowUnknownFields()
	var request nativeOperationRequest
	if err := decoder.Decode(&request); err != nil {
		return true, fmt.Errorf("the native helper received an invalid request")
	}
	defer clear(request.Password)
	if err := validateNativeOperation(request, account); err != nil {
		return true, err
	}
	if (request.Operation == nativeInstallService || request.Operation == nativeInstallTask) && !sameWindowsExecutable(request.Spec.Executable) {
		return true, fmt.Errorf("the native installation request must run this LeapMux executable")
	}
	// A configuration helper must not outlive its parent while a native call
	// blocks. Closing the private pipe ends this helper process; it never ends
	// an application service. A partially completed install remains repairable.
	var complete atomic.Bool
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		_, _ = io.Copy(io.Discard, connection)
		if !complete.Load() {
			os.Exit(1)
		}
	}()
	stopDeadline := context.AfterFunc(ctx, func() {
		if !complete.Load() {
			os.Exit(1)
		}
	})
	defer stopDeadline()
	operationErr := executeNativeOperation(ctx, request)
	complete.Store(true)
	reply := nativeOperationReply{}
	if operationErr != nil {
		reply.Code = credentialErrorCode(operationErr)
		if reply.Code == 0 {
			reply.Error = fmt.Sprintf("Windows could not complete %s: %v", request.Operation, operationErr)
		} else {
			reply.Error = fmt.Sprintf("Windows could not complete %s (code %d): %v", request.Operation, reply.Code, syscall.Errno(reply.Code))
		}
		switch reply.Code {
		case 1326, 1330, 1907, 1331, 1385:
			reply.Error += ": " + credentialErrorClass(operationErr)
		}
	}
	writeErr := json.NewEncoder(connection).Encode(reply)
	closeErr := connection.Close()
	<-watcherDone
	return true, errors.Join(writeErr, closeErr)
}

func nativeParentAccount(processID uint32, expectedStart uint64) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	created, err := windowsProcessCreation(process)
	if err != nil {
		return "", err
	}
	if created != expectedStart {
		return "", fmt.Errorf("the invoking process identity changed")
	}
	// The native Manager can be hosted by the explicit integration runner.
	// Its account SID is the authority; the helper still verifies the image
	// it installs, and permits only that account's instance namespace.
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Window     windows.Handle
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	Instance   windows.Handle
	IDList     uintptr
	Class      *uint16
	ClassKey   windows.Handle
	HotKey     uint32
	Icon       windows.Handle
	Process    windows.Handle
}

func launchElevatedHelper(executable, pipe string, parent int) (windows.Handle, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initialized := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	if initialized == nil || credentialErrorCode(initialized) == 1 {
		defer ole.CoUninitialize()
	} else if credentialErrorCode(initialized) != 0x80010106 {
		return 0, initialized
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, err
	}
	created, err := windowsProcessCreation(windows.CurrentProcess())
	if err != nil {
		return 0, err
	}
	arguments := []string{nativeHelperFlag, pipe, "--parent-pid", strconv.Itoa(parent), "--parent-start", strconv.FormatUint(created, 10)}
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = syscall.EscapeArg(argument)
	}
	parameters, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{Mask: 0x40 | 0x100, Verb: verb, File: file, Parameters: parameters}
	info.Size = uint32(unsafe.Sizeof(info))
	execute := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")
	success, _, nativeErr := execute.Call(uintptr(unsafe.Pointer(&info)))
	if success == 0 {
		return 0, fmt.Errorf("administrator authorization failed: %w", windowsCallFailure(nativeErr))
	}
	if info.Process == 0 {
		return 0, fmt.Errorf("Windows returned no elevated helper process")
	}
	return info.Process, nil
}

func windowsProcessCreation(process windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

func waitNativeHelper(ctx context.Context, process windows.Handle) error {
	for {
		state, err := windows.WaitForSingleObject(process, 100)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			var code uint32
			if err := windows.GetExitCodeProcess(process, &code); err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("the native helper exited with code %d", code)
			}
			return nil
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			return fmt.Errorf("Windows returned an invalid helper process state")
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("the native helper did not finish: %w", err)
		}
	}
}
