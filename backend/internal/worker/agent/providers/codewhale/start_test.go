package codewhale

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestOpenThreadCreatesAThreadWithTheLaunchSettings(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodPost, routeThreads, http.StatusCreated, threadRecord{ID: testThreadID})
	a, _ := newTestAgent(t, rt)

	thread, err := a.openThread(agent.Options{WorkingDir: "/w", Options: optionmap.Map{
		agent.OptionIDModel:           "deepseek-pro",
		contracts.CodewhaleOptionMode: contracts.CodewhaleModePlan,
		agent.OptionIDPermissionMode:  contracts.CodewhalePostureFullAccess,
	}})
	require.NoError(t, err)
	assert.Equal(t, testThreadID, thread.ID)
	assert.Equal(t, map[string]any{
		"workspace": "/w", "model": "deepseek-pro", "mode": "plan", "permission_posture": "full_access",
		"allow_shell": true,
	}, rt.lastBody(t, http.MethodPost, routeThreads))

	// openThread leaves the account's default model, and a posture that LeapMux
	// does not offer, to the runtime. It always allows the shell: the posture
	// still asks before each command.
	_, err = a.openThread(agent.Options{WorkingDir: "/w", Options: optionmap.Map{
		agent.OptionIDModel:          agent.DefaultModelSentinel,
		agent.OptionIDPermissionMode: "yolo",
	}})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"workspace": "/w", "allow_shell": true}, rt.lastBody(t, http.MethodPost, routeThreads))
}

func TestOpenThreadAppliesOnlyTheChangedSettingsToAResumedThread(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	resumed := threadRecord{ID: testThreadID, Model: "deepseek-flash", Mode: contracts.CodewhaleModeAgent, PermissionPosture: contracts.CodewhalePostureAsk}
	rt.respondJSON(http.MethodPost, threadPath(testThreadID, threadRouteResume), http.StatusOK, resumed)
	a, _ := newTestAgent(t, rt)
	opts := agent.Options{ResumeSessionID: testThreadID, Options: optionmap.Map{
		agent.OptionIDModel:           "deepseek-flash",
		contracts.CodewhaleOptionMode: contracts.CodewhaleModeAgent,
		agent.OptionIDPermissionMode:  contracts.CodewhalePostureAsk,
	}}

	thread, err := a.openThread(opts)
	require.NoError(t, err)
	assert.Equal(t, resumed, thread)
	assert.Empty(t, rt.requestsTo(http.MethodPatch, threadRoute), "settings that match the thread send no update")
	assert.Empty(t, rt.requestsTo(http.MethodPost, routeThreads), "a resume creates no thread")

	updated := resumed
	updated.Mode = contracts.CodewhaleModePlan
	rt.respondJSON(http.MethodPatch, threadRoute, http.StatusOK, updated)
	opts.Options[contracts.CodewhaleOptionMode] = contracts.CodewhaleModePlan
	thread, err = a.openThread(opts)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "plan"}, rt.lastBody(t, http.MethodPatch, threadRoute), "only the changed axis rides the update")
	assert.Equal(t, updated, thread, "the thread states what the runtime settled on")
}

// A resumed thread runs on its own settings when the runtime refuses the
// launch's settings. The snapshot reports them, and the reader can change them
// again.
func TestOpenThreadKeepsAResumedThreadWhenTheUpdateFails(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	resumed := threadRecord{ID: testThreadID, Model: "deepseek-flash"}
	rt.respondJSON(http.MethodPost, threadPath(testThreadID, threadRouteResume), http.StatusOK, resumed)
	rt.respondStatus(http.MethodPatch, threadRoute, http.StatusBadRequest, "unknown model")
	a, _ := newTestAgent(t, rt)

	thread, err := a.openThread(agent.Options{ResumeSessionID: testThreadID, Options: optionmap.Map{agent.OptionIDModel: "a-model-nobody-has"}})
	require.NoError(t, err)
	assert.Equal(t, resumed, thread)
	assert.Len(t, rt.requestsTo(http.MethodPatch, threadRoute), 1)
}

func TestOpenThreadFailsWhenTheResumeFails(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPost, threadPath(testThreadID, threadRouteResume), http.StatusNotFound, "thread not found")
	a, _ := newTestAgent(t, rt)

	_, err := a.openThread(agent.Options{ResumeSessionID: testThreadID, Options: optionmap.Map{agent.OptionIDModel: "deepseek-pro"}})
	assert.ErrorContains(t, err, "thread not found")
	assert.Empty(t, rt.requestsTo(http.MethodPatch, threadRoute), "openThread applies nothing to a thread that did not resume")
}

func TestDiscardFreshStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fresh := codewhaleStore{dir: filepath.Join(root, "store-fresh")}
	require.NoError(t, os.MkdirAll(fresh.runtimeDir(), 0o700))
	discardFreshStore(true, fresh)
	_, err := os.Stat(fresh.dir)
	assert.ErrorIs(t, err, os.ErrNotExist, "the start removes a store that it made and never used")
	discardFreshStore(true, fresh)

	resumed := codewhaleStore{dir: filepath.Join(root, "store-resumed")}
	require.NoError(t, os.MkdirAll(resumed.runtimeDir(), 0o700))
	discardFreshStore(false, resumed)
	assert.DirExists(t, resumed.dir, "a resumed store holds a thread that a later resume needs")

	discardFreshStore(true, codewhaleStore{})
	assert.DirExists(t, root, "a store with no directory removes nothing")
}
