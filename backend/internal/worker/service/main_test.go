package service

import (
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// The terminal tests start login shells, which read the login files under
// HOME. See testutil.RunWithEmptyHome.
func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithEmptyHome(m))
}
