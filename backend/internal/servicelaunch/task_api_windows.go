//go:build windows

package servicelaunch

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

func withTaskFolder(ctx context.Context, use func(*ole.IDispatch) error) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initialized := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
	if initialized == nil || credentialErrorCode(initialized) == 1 {
		defer ole.CoUninitialize()
	} else if credentialErrorCode(initialized) != 0x80010106 {
		return initialized
	}
	object, err := oleutil.CreateObject("Schedule.Service")
	if err != nil {
		return err
	}
	defer object.Release()
	scheduler, err := object.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer scheduler.Release()
	connected, err := oleutil.CallMethod(scheduler, "Connect")
	if connected != nil {
		defer func() { result = errors.Join(result, connected.Clear()) }()
	}
	if err != nil {
		return err
	}
	folderValue, err := oleutil.CallMethod(scheduler, "GetFolder", `\`)
	if folderValue != nil {
		defer func() { result = errors.Join(result, folderValue.Clear()) }()
	}
	if err != nil {
		return err
	}
	folder := folderValue.ToIDispatch()
	if folder == nil {
		return fmt.Errorf("Windows returned no task folder")
	}
	if err := use(folder); err != nil {
		return err
	}
	return ctx.Err()
}

func withRegisteredTask(folder *ole.IDispatch, spec LaunchSpec, use func(*ole.IDispatch) error) (result error) {
	value, err := oleutil.CallMethod(folder, "GetTask", taskName(spec))
	if value != nil {
		defer func() { result = errors.Join(result, value.Clear()) }()
	}
	if err != nil {
		if credentialErrorCode(err) == uint32(windows.ERROR_FILE_NOT_FOUND) {
			return fs.ErrNotExist
		}
		return err
	}
	task := value.ToIDispatch()
	if task == nil {
		return fmt.Errorf("Windows returned no registered task")
	}
	return use(task)
}

// taskScalar copies only scalar properties before releasing their VARIANT.
// COM objects must retain their owning VARIANT throughout the native call.
func taskScalar(object *ole.IDispatch, name string) (value any, result error) {
	property, err := oleutil.GetProperty(object, name)
	if property != nil {
		defer func() { result = errors.Join(result, property.Clear()) }()
	}
	if err != nil {
		return nil, err
	}
	switch property.VT {
	case ole.VT_BSTR, ole.VT_BOOL, ole.VT_I4, ole.VT_UI4, ole.VT_I2, ole.VT_UI2:
		return property.Value(), nil
	default:
		return nil, fmt.Errorf("Windows returned an invalid task property %s", name)
	}
}

func taskNumber(object *ole.IDispatch, name string) (int64, error) {
	value, err := taskScalar(object, name)
	if err != nil {
		return 0, err
	}
	switch number := value.(type) {
	case int32:
		return int64(number), nil
	case uint32:
		return int64(number), nil
	case int16:
		return int64(number), nil
	case uint16:
		return int64(number), nil
	default:
		return 0, fmt.Errorf("Windows returned an invalid numeric task property %s", name)
	}
}

func taskAccountSID(account string) (string, error) {
	if strings.HasPrefix(account, "S-") {
		sid, err := windows.StringToSid(account)
		if err != nil {
			return "", err
		}
		return sid.String(), nil
	}
	return resolveWindowsAccount(account)
}

func readTaskSnapshot(task *ole.IDispatch) (snapshot windowsTaskSnapshot, result error) {
	raw, err := taskScalar(task, "Xml")
	if err != nil {
		return snapshot, err
	}
	definition, ok := raw.(string)
	if !ok {
		return snapshot, fmt.Errorf("Windows returned no task XML")
	}
	if err := xml.Unmarshal([]byte(definition), &snapshot.Definition); err != nil {
		return snapshot, fmt.Errorf("Windows returned invalid task XML")
	}
	enabled, err := taskScalar(task, "Enabled")
	if err != nil {
		return snapshot, err
	}
	snapshot.Enabled, ok = enabled.(bool)
	if !ok {
		return snapshot, fmt.Errorf("Windows returned an invalid task enabled state")
	}
	state, err := taskNumber(task, "State")
	if err != nil {
		return snapshot, err
	}
	if state < 0 || state > 4 {
		return snapshot, fmt.Errorf("Windows returned an unknown task state")
	}
	snapshot.State = int32(state)
	lastResult, err := taskNumber(task, "LastTaskResult")
	if err != nil {
		return snapshot, err
	}
	if lastResult < -1<<31 || lastResult > 1<<32-1 {
		return snapshot, fmt.Errorf("Windows returned an invalid task result")
	}
	snapshot.LastResult = uint32(lastResult)
	instances, err := oleutil.CallMethod(task, "GetInstances", 0)
	if instances != nil {
		defer func() { result = errors.Join(result, instances.Clear()) }()
	}
	if err != nil {
		return snapshot, err
	}
	collection := instances.ToIDispatch()
	if collection == nil {
		return snapshot, fmt.Errorf("Windows returned no task instance collection")
	}
	count, err := taskNumber(collection, "Count")
	if err != nil {
		return snapshot, err
	}
	if count < 0 || count > 1<<20 {
		return snapshot, fmt.Errorf("Windows returned an invalid task instance count")
	}
	snapshot.Instances = int32(count)
	if len(snapshot.Definition.Principals.Items) == 1 {
		snapshot.Identity.PrincipalSID, err = taskAccountSID(snapshot.Definition.Principals.Items[0].UserID)
		if err != nil {
			return snapshot, err
		}
	}
	if len(snapshot.Definition.Triggers.Items) == 1 && snapshot.Definition.Triggers.Items[0].XMLName.Local == "LogonTrigger" {
		snapshot.Identity.TriggerSID, err = taskAccountSID(snapshot.Definition.Triggers.Items[0].UserID)
		if err != nil {
			return snapshot, err
		}
	}
	if len(snapshot.Definition.Actions.Items) == 1 && snapshot.Definition.Actions.Items[0].XMLName.Local == "Exec" {
		arguments, err := windows.DecomposeCommandLine(`"leapmux-task.exe" ` + snapshot.Definition.Actions.Items[0].Arguments)
		if err != nil {
			return snapshot, err
		}
		if len(arguments) == 0 {
			return snapshot, fmt.Errorf("Windows returned no task arguments")
		}
		snapshot.Identity.Arguments = arguments[1:]
	}
	snapshot.Access, err = readTaskAccess(task)
	return snapshot, err
}

func inspectWindowsTask(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	var status NativeStatus
	err := withTaskFolder(ctx, func(folder *ole.IDispatch) error {
		return withRegisteredTask(folder, spec, func(task *ole.IDispatch) error {
			snapshot, err := readTaskSnapshot(task)
			if err != nil {
				return err
			}
			status = NativeStatus{Installed: true, DefinitionMatches: snapshot.matches(spec), Running: snapshot.active(), State: "stopped"}
			if !snapshot.Enabled {
				status.State = "disabled"
			}
			if snapshot.State == 2 {
				status.State = "start-pending"
			}
			if snapshot.Instances > 0 || snapshot.State == 4 {
				status.State = "running"
			}
			if !snapshot.active() && snapshot.LastResult != 0 && snapshot.LastResult != 0x41303 && snapshot.LastResult != 0x41301 && snapshot.LastResult != 0x41306 {
				status.State = "failed"
				status.Failure = fmt.Sprintf("the scheduled task exited with Windows result 0x%08x", snapshot.LastResult)
			}
			return nil
		})
	})
	if errors.Is(err, fs.ErrNotExist) {
		return NativeStatus{State: "not installed"}, nil
	}
	return status, err
}

func upsertWindowsTask(ctx context.Context, spec LaunchSpec, password []byte) error {
	if err := verifyWindowsAccount(spec); err != nil {
		return err
	}
	if err := validateWindowsLogon(spec, password, 4); err != nil {
		return err
	}
	definition, err := taskDefinitionXML(spec, windows.ComposeCommandLine(spec.RunArguments()))
	if err != nil {
		return err
	}
	return withTaskFolder(ctx, func(folder *ole.IDispatch) error {
		err := withRegisteredTask(folder, spec, func(task *ole.IDispatch) error {
			snapshot, err := readTaskSnapshot(task)
			if err != nil {
				return err
			}
			if snapshot.Identity.PrincipalSID != spec.AccountID {
				return fmt.Errorf("the existing scheduled task belongs to another account")
			}
			if snapshot.active() && !snapshot.matches(spec) {
				return fmt.Errorf("stop the scheduled task before changing its configuration")
			}
			if snapshot.State == 0 {
				return fmt.Errorf("the scheduled task's current state is unknown")
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		security := windowsTaskSecurityDescriptor(spec.AccountID)
		// Password is a COM argument in this process, never part of the task
		// XML, a child command line, a saved specification, or diagnostic text.
		// The explicit descriptor supplies the principal's access. Prevent Task
		// Scheduler from adding another context-principal entry.
		value, err := oleutil.CallMethod(folder, "RegisterTask", taskName(spec), string(definition), windowsTaskRegistrationFlags, spec.AccountName, string(password), 1, security)
		if value != nil {
			err = errors.Join(err, value.Clear())
		}
		return err
	})
}

func startWindowsTask(ctx context.Context, spec LaunchSpec) error {
	return withTaskFolder(ctx, func(folder *ole.IDispatch) error {
		return withRegisteredTask(folder, spec, func(task *ole.IDispatch) error {
			snapshot, err := readTaskSnapshot(task)
			if err != nil {
				return err
			}
			return startTaskSnapshot(snapshot, spec, func() error {
				value, err := oleutil.CallMethod(task, "Run", nil)
				if value != nil {
					err = errors.Join(err, value.Clear())
				}
				return err
			})
		})
	})
}

func removeWindowsTask(ctx context.Context, spec LaunchSpec) error {
	err := withTaskFolder(ctx, func(folder *ole.IDispatch) error {
		err := withRegisteredTask(folder, spec, func(task *ole.IDispatch) error {
			snapshot, err := readTaskSnapshot(task)
			if err != nil {
				return err
			}
			if snapshot.Identity.PrincipalSID != spec.AccountID {
				return fmt.Errorf("the scheduled task belongs to another account")
			}
			if snapshot.active() || snapshot.State == 0 {
				return fmt.Errorf("stop the scheduled task before removing its startup definition")
			}
			return nil
		})
		if err != nil {
			return err
		}
		value, err := oleutil.CallMethod(folder, "DeleteTask", taskName(spec), 0)
		if value != nil {
			err = errors.Join(err, value.Clear())
		}
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
