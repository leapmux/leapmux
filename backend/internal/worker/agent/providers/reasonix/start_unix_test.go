//go:build unix

package reasonix

import (
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartReasonixResolvesResumedLaunchAliasesNatively(t *testing.T) {
	for _, scenario := range []string{"success", "queued", "refused", "invalid snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GO_REASONIX_TEST_RESUME_ALIAS", scenario)
			installFakeReasonixCLI(t, filepath.Join(t.TempDir(), "args.txt"))
			provider, err := Start(t.Context(), agent.Options{
				AgentID: "reasonix-alias", ResumeSessionID: "reasonix-resume", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
				Options: map[string]string{agent.OptionIDModel: "deepseek-flash"}, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
			}, agent.NewProviderServices(&agenttest.Sink{}))
			if scenario == "invalid snapshot" {
				if provider != nil {
					provider.Stop()
					_ = provider.Wait()
				}
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			a := provider.(*Agent)
			t.Cleanup(func() { a.Stop(); _ = a.Wait() })
			expected := "deepseek/deepseek-flash"
			if scenario == "refused" {
				expected = "deepseek/deepseek-pro"
			}
			assert.Equal(t, expected, a.CurrentModel())
		})
	}
}
