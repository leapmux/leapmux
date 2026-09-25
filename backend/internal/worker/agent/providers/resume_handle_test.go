package providers

import (
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
)

// TestOnlySessionFileProvidersLeaveTheTokenRule keeps the carve-out honest.
//
// Every other provider inherits ProviderDefaults's token rule by saying nothing, so
// a provider added later is covered by default and only one whose handle is a
// different shape has to override. Pi and Oh My Pi are the two whose CLI resumes
// a session FILE as well as an id, through providerkit.ResolveSessionFileOrIDHandle.
// This fails the day a third provider leaves the token rule, which is the day
// somebody should look at whether the split still reads.
func TestOnlySessionFileProvidersLeaveTheTokenRule(t *testing.T) {
	t.Parallel()

	// A handle that the TOKEN rule refuses and that the path rule accepts. The
	// backslash is what the token rule refuses. The path is absolute on the
	// running host, because the path rule refuses a relative path.
	pathLike := filepath.Join(testutil.NativeAbsPath("/tmp/pi/sessions"), `a\b`+".jsonl")

	sessionFileProviders := map[leapmuxv1.AgentProvider]bool{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:       true,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OH_MY_PI: true,
	}
	registry := Registry()
	for _, id := range registry.Providers() {
		_, err := registry.Plugin(id).ResolveResumeHandle(pathLike, "")
		if sessionFileProviders[id] {
			assert.NoErrorf(t, err, "%v takes a session file path that the token rule refuses", id)
			continue
		}
		assert.Errorf(t, err, "%v must keep the token rule, which refuses a backslash", id)
	}
}
