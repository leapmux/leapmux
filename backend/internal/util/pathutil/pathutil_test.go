package pathutil

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSamePath_Identical(t *testing.T) {
	assert.True(t, SamePath("/home/user", "/home/user"))
}

func TestSamePath_CleanNormalization(t *testing.T) {
	assert.True(t, SamePath("/home/user/", "/home/user"))
	assert.True(t, SamePath("/home//user", "/home/user"))
	assert.True(t, SamePath("/home/./user", "/home/user"))
}

func TestSamePath_Different(t *testing.T) {
	assert.False(t, SamePath("/home/alice", "/home/bob"))
}

func TestSamePath_CaseSensitivity(t *testing.T) {
	// Case-insensitive on Windows, case-sensitive on POSIX.
	got := SamePath("/Home/User", "/home/user")
	if runtime.GOOS == "windows" {
		assert.True(t, got, "Windows should compare paths case-insensitively")
	} else {
		assert.False(t, got, "POSIX should compare paths case-sensitively")
	}
}
