package reasonix

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
)

func TestPermissionBypassRequiresNativeToolMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		allow        bool
	}{
		{"ordinary", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"bash"}}}}`, true},
		{"question", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"ask"}}}}`, false},
		{"plan", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"exit_plan_mode"}}}}`, false},
		{"another tool", `{"toolCall":{"_meta":{"reasonix.io":{"tool":"ask_again"}}}}`, true},
		{"foreign metadata", `{"toolCall":{"_meta":{"other":{"tool":"bash"}}}}`, false},
		{"empty tool", `{"toolCall":{"_meta":{"reasonix.io":{"tool":""}}}}`, false},
		{"missing", `{}`, false},
		{"wrong tool type", `{"toolCall":{"_meta":{"reasonix.io":{"tool":false}}}}`, false},
		{"malformed", `[`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.allow, permissionAllowsBypass(&providerkit.ParsedLine{Params: []byte(tc.params)}))
		})
	}
}
