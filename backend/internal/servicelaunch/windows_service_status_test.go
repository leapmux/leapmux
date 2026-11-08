package servicelaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWindowsServiceExitFailure(t *testing.T) {
	for _, test := range []struct {
		name           string
		code, specific uint32
		expected       string
	}{
		{"success", 0, 0, ""},
		{"unused service code after success", 0, 42, ""},
		{"never started", windowsServiceNeverStarted, 0, ""},
		{"never started with unused service code", windowsServiceNeverStarted, 42, ""},
		{"Windows failure", 5, 0, "the service exited with Windows code 5"},
		{"Windows failure ignores unused service code", 5, 42, "the service exited with Windows code 5"},
		{"service-specific failure", windowsServiceSpecificError, 42, "the service exited with Windows code 1066 and service code 42"},
		{"service-specific zero", windowsServiceSpecificError, 0, "the service exited with Windows code 1066 and service code 0"},
		{"maximum native code", ^uint32(0), 42, "the service exited with Windows code 4294967295"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, windowsServiceExitFailure(test.code, test.specific))
		})
	}
}
