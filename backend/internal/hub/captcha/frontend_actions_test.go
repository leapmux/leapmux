package captcha

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFrontendCaptchaActionsMatchProcedureActions pins the cross-end
// action contract: the backend verifies tokens under the actions in
// protectedProcedures, and the frontend's CaptchaField action union must
// offer exactly those strings (the three pages bind them as literals). A
// drift on either end denies every token for the affected procedure, and
// no other test compiles both ends together — the per-end tests each
// assert their own half against its own literal.
func TestFrontendCaptchaActionsMatchProcedureActions(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	fieldPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..",
		"frontend", "src", "components", "common", "CaptchaField.tsx")
	raw, err := os.ReadFile(fieldPath)
	require.NoError(t, err, "the frontend captcha field source must stay readable at %s", fieldPath)

	unionLine := regexp.MustCompile(`(?m)^\s*action:\s*.*$`).Find(raw)
	require.NotNil(t, unionLine, "CaptchaField.tsx must declare the action union")
	quoted := regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(string(unionLine), -1)
	require.NotEmpty(t, quoted, "the action union must list its members as quoted literals")
	frontend := make([]string, 0, len(quoted))
	for _, m := range quoted {
		frontend = append(frontend, m[1])
	}
	sort.Strings(frontend)

	backend := make([]string, 0, len(protectedProcedures))
	for _, proc := range protectedProcedures {
		backend = append(backend, proc.action)
	}
	sort.Strings(backend)

	assert.Equal(t, backend, frontend,
		"the captcha action literals must match across hub and frontend; a mismatch denies every token for the affected procedure")
}
