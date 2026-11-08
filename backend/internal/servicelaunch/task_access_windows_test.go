//go:build windows

package servicelaunch

import (
	"testing"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestReadTaskSecurityDescriptor(t *testing.T) {
	const descriptor = "D:P(A;;FA;;;SY)"
	for _, scenario := range []string{"success", "query error", "nil", "wrong type", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			text, err := readTaskSecurityDescriptorWith(nil, func(_ *ole.IDispatch, method string, args ...interface{}) (*ole.VARIANT, error) {
				require.Equal(t, "GetSecurityDescriptor", method)
				require.Equal(t, []interface{}{int32(4)}, args)
				switch scenario {
				case "query error":
					return nil, windows.ERROR_ACCESS_DENIED
				case "nil":
					return nil, nil
				case "wrong type":
					v := ole.NewVariant(ole.VT_I4, 1)
					return &v, nil
				}
				value := descriptor
				if scenario == "empty" {
					value = ""
				}
				allocated := ole.SysAllocStringLen(value)
				v := ole.NewVariant(ole.VT_BSTR, int64(uintptr(unsafe.Pointer(allocated))))
				return &v, nil
			})
			if scenario == "success" {
				require.NoError(t, err)
				assert.Equal(t, descriptor, text)
			} else {
				require.Error(t, err)
				assert.Empty(t, text)
				if scenario == "query error" {
					assert.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED)
				}
			}
		})
	}
}

func TestParseTaskAccess(t *testing.T) {
	const account = "S-1-5-21-1001"
	for _, descriptor := range []string{windowsTaskSecurityDescriptor(account), "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GA;;;" + account + ")"} {
		access, err := parseTaskAccess(descriptor)
		require.NoError(t, err)
		assert.True(t, access.matches(account))
	}
	for _, descriptor := range []string{
		windowsTaskSecurityDescriptor(account) + "(A;;FA;;;WD)",
		"D:P(A;;FA;;;SY)(A;;FA;;;BA)",
		"D:(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + account + ")",
		"D:P(A;ID;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + account + ")",
		"D:P(A;OICI;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + account + ")",
		"D:P(D;;FA;;;WD)(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + account + ")",
		"D:NO_ACCESS_CONTROL", "D:P",
	} {
		access, err := parseTaskAccess(descriptor)
		require.NoError(t, err, descriptor)
		assert.False(t, access.matches(account), descriptor)
	}
	_, err := parseTaskAccess("invalid descriptor")
	require.Error(t, err)
}
