package cursor

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func TestCursorRegisteredSecondaryFallback(t *testing.T) {
	t.Parallel()
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(cursorStaticOptionGroups, acp.ModeChannelPermissionMode), fallbackCursorCLIModes(),
		Registration().OptionGroups, cursorStaticOptionGroups)
}
