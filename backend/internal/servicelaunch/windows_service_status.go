package servicelaunch

import "fmt"

const (
	windowsServiceNeverStarted  uint32 = 1077
	windowsServiceSpecificError uint32 = 1066
)

func windowsServiceExitFailure(code, serviceCode uint32) string {
	if code == 0 || code == windowsServiceNeverStarted {
		return ""
	}
	if code == windowsServiceSpecificError {
		return fmt.Sprintf("the service exited with Windows code %d and service code %d", code, serviceCode)
	}
	return fmt.Sprintf("the service exited with Windows code %d", code)
}
