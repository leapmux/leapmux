package captcha

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCaptchaActionsMatchProcedureActions pins the cross-end action
// contract: contracts/captcha.json is the single source of truth, the
// backend's protectedProcedures map consumes its generated constants, and
// the frontend's CaptchaField action union is generated from the same file.
// This test pins the remaining seam -- the map's action set must be exactly
// the contract's -- so a stale contract entry, or a literal that bypassed
// the generated constants, fails here instead of denying tokens at runtime.
// A drift on either end denies every token for the affected procedure, and
// no other test compiles both ends together.
func TestCaptchaActionsMatchProcedureActions(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	contractPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..",
		"contracts", "captcha.json")
	raw, err := os.ReadFile(contractPath)
	require.NoError(t, err, "the captcha action contract must stay readable at %s", contractPath)

	var contract struct {
		Actions map[string]string `json:"actions"`
	}
	require.NoError(t, json.Unmarshal(raw, &contract))
	require.NotEmpty(t, contract.Actions, "contracts/captcha.json must list its actions")

	contractActions := make([]string, 0, len(contract.Actions))
	for _, token := range contract.Actions {
		contractActions = append(contractActions, token)
	}
	sort.Strings(contractActions)

	backend := make([]string, 0, len(protectedProcedures))
	for _, proc := range protectedProcedures {
		backend = append(backend, proc.action)
	}
	sort.Strings(backend)

	assert.Equal(t, contractActions, backend,
		"the captcha action set must match between contracts/captcha.json and protectedProcedures; a mismatch denies every token for the affected procedure")
}
