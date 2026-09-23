package providers

import (
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
)

// TestPiIsTheOnlyProviderOffTheTokenRule keeps the carve-out honest.
//
// Every other provider inherits ProviderDefaults's token rule by saying nothing, so
// a provider added later is covered by default and only one whose handle is a
// different shape has to override. This fails the day a second provider does,
// which is the day somebody should look at whether the split still reads.
func TestPiIsTheOnlyProviderOffTheTokenRule(t *testing.T) {
	t.Parallel()

	// A handle that the TOKEN rule refuses and that the path rule of Pi accepts.
	// The backslash is what the token rule refuses. The path is absolute on the
	// running host, because the path rule refuses a relative path.
	pathLike := filepath.Join(testutil.NativeAbsPath("/tmp/pi/sessions"), `a\b`+".jsonl")

	registry := Registry()
	for _, id := range registry.Providers() {
		_, err := registry.Plugin(id).ResolveResumeHandle(pathLike, "")
		if id == leapmuxv1.AgentProvider_AGENT_PROVIDER_PI {
			assert.NoError(t, err, "Pi takes a session file path that the token rule refuses")
			continue
		}
		assert.Errorf(t, err, "%v must keep the token rule, which refuses a backslash", id)
	}
}
