package zcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// registryRefreshReason is the reason every provider registry refresh states.
// The app-server records it on the reload; this one marks the refresh that
// precedes a session's model catalog read.
const registryRefreshReason = "session_resume"

// zcodeCatalogRefreshOwner coalesces the registry-plus-catalog refresh passes
// that account changes and workspace modelCatalog patches request.
//
// One pass runs at a time. A request arriving during a pass requests exactly one
// following pass, whatever the number of arrivals, and the waiters that one pass
// took on share its result. A waiter attaches to the next pass the owner
// schedules, so a pass's reads start at or after every request it serves: an
// account generation's catalog read must observe that generation's snapshot.
type zcodeCatalogRefreshOwner struct {
	mu sync.Mutex
	// running is true while a pass runs or is about to run.
	running bool
	// pending requests one following pass.
	pending bool
	// waiters holds the requests waiting for the next scheduled pass.
	waiters []chan error
}

// refreshZCodeProviderRegistry asks the app-server to reload its provider
// registry from disk, so a personal configuration change reaches the session's
// model catalog. The acknowledgement is required: only a build that does not
// implement the method has no such capability, and that absence is ignored.
func (a *Agent) refreshZCodeProviderRegistry(timeout time.Duration) error {
	raw, err := a.sendZCodeRequest(MethodProviderRefreshRegistry, map[string]any{"reason": registryRefreshReason}, timeout)
	if err != nil {
		if zcodeIsMethodNotFound(err) {
			return nil
		}
		return err
	}
	var response struct {
		Refreshed bool `json:"refreshed"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode ZCode provider registry refresh acknowledgement: %w", err)
	}
	if !response.Refreshed {
		return fmt.Errorf("ZCode did not confirm the provider registry refresh")
	}
	return nil
}

// scheduleZCodeCatalogRefresh requests one registry-plus-catalog pass without
// waiting for its result. The pass runs on a goroutine of its own, so the read
// loop that a workspace patch arrives on stays free.
func (a *Agent) scheduleZCodeCatalogRefresh() {
	a.requestZCodeCatalogRefresh(nil)
}

// awaitZCodeCatalogRefresh requests one registry-plus-catalog pass and waits
// for the result of the pass that this request attaches to.
func (a *Agent) awaitZCodeCatalogRefresh() error {
	waiter := make(chan error, 1)
	a.requestZCodeCatalogRefresh(waiter)
	return <-waiter
}

// requestZCodeCatalogRefresh attaches a request to the next scheduled pass and
// schedules one when the owner is idle. A nil waiter schedules without waiting.
func (a *Agent) requestZCodeCatalogRefresh(waiter chan error) {
	a.catalogRefresh.mu.Lock()
	if waiter != nil {
		a.catalogRefresh.waiters = append(a.catalogRefresh.waiters, waiter)
	}
	if a.catalogRefresh.running {
		// A trigger arriving during a pass requests one following pass.
		a.catalogRefresh.pending = true
		a.catalogRefresh.mu.Unlock()
		return
	}
	batch := a.catalogRefresh.waiters
	a.catalogRefresh.waiters = nil
	a.catalogRefresh.running = true
	a.catalogRefresh.pending = false
	a.catalogRefresh.mu.Unlock()
	go a.runZCodeCatalogRefreshPasses(batch)
}

// runZCodeCatalogRefreshPasses runs passes until none is requested, delivering
// each pass's result to the waiters that pass took on.
func (a *Agent) runZCodeCatalogRefreshPasses(batch []chan error) {
	for {
		err := a.runZCodeCatalogRefreshPass()
		a.catalogRefresh.mu.Lock()
		for _, waiter := range batch {
			waiter <- err
		}
		if !a.catalogRefresh.pending {
			a.catalogRefresh.running = false
			a.catalogRefresh.mu.Unlock()
			return
		}
		batch = a.catalogRefresh.waiters
		a.catalogRefresh.waiters = nil
		a.catalogRefresh.pending = false
		a.catalogRefresh.mu.Unlock()
	}
}

// runZCodeCatalogRefreshPass refreshes the provider registry and then reads the
// unrestricted model catalog. The registry refresh comes first: a catalog read
// against a registry that predates the change would fold models the user
// already removed. A registry failure stops the pass there.
func (a *Agent) runZCodeCatalogRefreshPass() error {
	if err := a.refreshZCodeProviderRegistry(a.APITimeout()); err != nil {
		return err
	}
	return a.refreshZCodeModelCatalog()
}

// refreshZCodeModelCatalog reads the unrestricted model list of the active
// session. Native session/read and setter replies contain only the current model.
func (a *Agent) refreshZCodeModelCatalog() error {
	a.sessionLifecycleMu.Lock()
	changed, err := a.readZCodeModelCatalog()
	a.sessionLifecycleMu.Unlock()
	if changed {
		a.sink.PersistSettingsRefresh(nil)
	}
	return err
}

func (a *Agent) readZCodeModelCatalog() (bool, error) {
	a.Mu.Lock()
	sessionID, stopped := a.sessionID, a.StoppedLocked()
	a.Mu.Unlock()
	if sessionID == "" || stopped {
		return false, errNoZCodeSession
	}
	// Native resume returns the existing runtime when this ID is active. The
	// lifecycle lock prevents adopting and retiring a replacement during this read.
	raw, err := a.sendZCodeRequest(MethodSessionResume, a.buildZCodeResumeParams(sessionID), a.APITimeout())
	if err != nil {
		return false, err
	}
	snap, ok := a.parseStateSnapshot(raw)
	if !ok || snap.Session.SessionID != sessionID || snap.Settings == nil || snap.Settings.Model == nil || snap.Settings.Model.Available == nil {
		return false, fmt.Errorf("ZCode returned no complete model catalog for session %q", sessionID)
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.sessionID != sessionID || a.StoppedLocked() {
		return false, errNoZCodeSession
	}
	// The response's axes and event sequence can already be stale. Fold only
	// membership and metadata, using the model currently observed by the agent.
	providerID, modelID, _ := splitZCodeModelID(a.model)
	a.replaceZCodeLiveModelsLocked(snap.Settings.Model.Available, zcodeModelRef{ProviderID: providerID, ModelID: modelID})
	return true, nil
}

// repairUnavailableZCodeModel runs the one repair stage of a start: a current
// model that the complete nonempty live catalog no longer holds is replaced by
// the first live model, with that model's reasoning default, through the bounded
// reasoning-level ladder.
//
// The stage runs outside sessionLifecycleMu (the setter takes it after its RPC)
// and at most once per agent. An explicitly empty catalog or a failed repair
// makes no further setter call and marks the model unresolved.
func (a *Agent) repairUnavailableZCodeModel(timeout time.Duration) {
	a.Mu.Lock()
	if a.modelRepairAttempted {
		a.Mu.Unlock()
		return
	}
	a.modelRepairAttempted = true
	if !a.liveCatalogComplete {
		// No complete native catalog states absence, so there is nothing to repair.
		a.Mu.Unlock()
		return
	}
	if len(a.liveModelOrder) == 0 {
		// The catalog is explicitly empty: it manufactures no model from stale
		// legacy files.
		a.Mu.Unlock()
		a.markSettingUnresolved(agent.OptionIDModel)
		return
	}
	if _, ok := a.resolveZCodeModelIDLocked(a.model); ok {
		// The persisted selection is still live.
		a.Mu.Unlock()
		return
	}
	first := a.liveModelOrder[0]
	a.Mu.Unlock()
	if _, err := a.applyZCodeModel(first, "", timeout); err != nil {
		slog.Warn("zcode unavailable-model repair failed", "agent_id", a.AgentID(), "model", first, "error", err)
		a.markSettingUnresolved(agent.OptionIDModel)
	}
}
