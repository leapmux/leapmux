package dirac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestBypassRequiresAnOrdinaryNativeToolApproval(t *testing.T) {
	t.Parallel()
	hooks := (&Agent{}).configure(nil)
	require.NotNil(t, hooks.PermissionAllowsBypass)
	for _, tc := range []struct {
		name   string
		params string
		allow  bool
	}{
		{"API retry dialog", `{"toolCall":{"name":"API Request Failed","title":"API Request Failed","kind":"other","rawInput":{"body":"400 failure"}},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"another native dialog", `{"toolCall":{"name":"Continue","title":"Continue","kind":"other"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"changed dialog title", `{"toolCall":{"name":"API Request Failed","title":"Retry the request","kind":"other"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"manual task interaction", `{"toolCall":{"name":"resolve_task_interaction","title":"Task interaction"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"missing title", `{"toolCall":{"name":"execute_command"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"explicit tool identity with matching title", `{"toolCall":{"name":"custom_tool","title":"custom_tool","rawInput":{"tool":"custom_tool"}},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, true},
		{"mismatched tool identity", `{"toolCall":{"name":"custom_tool","title":"custom_tool","rawInput":{"tool":"another_tool"}},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"ordinary", `{"toolCall":{"name":"execute_command","title":"Execute command"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, true},
		{"another tool", `{"toolCall":{"name":"edit_file","title":"Edit file"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, true},
		{"new task", `{"toolCall":{"name":"new_task"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"transition choice", `{"toolCall":{"name":"execute_command","title":"Execute command"},"options":[{"optionId":"new_task","kind":"allow_once"}]}`, false},
		{"response dialog", `{"toolCall":{"name":"respond"},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"no name", `{"toolCall":{"rawInput":{"tool":"execute_command"}},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"empty name", `{"toolCall":{"name":""},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"wrong name type", `{"toolCall":{"name":false},"options":[{"optionId":"allow_once","kind":"allow_once"}]}`, false},
		{"no options", `{"toolCall":{"name":"execute_command"}}`, false},
		{"lasting approval only", `{"toolCall":{"name":"execute_command","title":"Execute command"},"options":[{"optionId":"allow_always","kind":"allow_always"}]}`, false},
		{"wrong option kind", `{"toolCall":{"name":"execute_command","title":"Execute command"},"options":[{"optionId":"allow_once","kind":"reject_once"}]}`, false},
		{"null", `null`, false},
		{"malformed", `[`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allow, hooks.PermissionAllowsBypass(&providerkit.ParsedLine{Params: []byte(tc.params)}))
		})
	}
}

func TestBypassClaimRequiresTheCurrentNativeYoloValue(t *testing.T) {
	t.Parallel()
	a, _ := acptest.NewAgentForRPC(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base })
	hooks := a.configure(nil)
	require.NotNil(t, hooks.WithBypassPermission)
	claims := 0
	for _, value := range []string{"", "false", "true", "false"} {
		a.OptionsForTest().SetValuesForTest(map[string]string{"yolo": value})
		hooks.WithBypassPermission(func() {
			claims++
			if a.Mu.TryLock() {
				a.Mu.Unlock()
				t.Error("the bypass claim must retain the native setting under its mutex")
			}
		})
	}
	assert.Equal(t, 1, claims)
}
