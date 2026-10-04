package codewhale

import (
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// effortIDs lists a model's effort ids in order.
func effortIDs(model *agent.ModelInfo) []string {
	ids := make([]string, 0, len(model.SupportedEfforts))
	for _, effort := range model.SupportedEfforts {
		ids = append(ids, effort.Id)
	}
	return ids
}

func TestCodewhaleModelInfo(t *testing.T) {
	t.Parallel()
	listed := codewhaleModelInfo(providerModel{ID: "deepseek-flash", ReasoningEffort: capabilitySupported, ReasoningEffortLevels: []string{"low", " high ", "", "low", agent.EffortAuto}}, "deepseek-flash")
	assert.Equal(t, "deepseek-flash", listed.Id)
	assert.True(t, listed.IsDefault)
	assert.Equal(t, agent.EffortAuto, listed.DefaultEffort)
	assert.Equal(t, []string{agent.EffortAuto, "high", "low"}, effortIDs(listed), "auto first, then the model's own levels, highest first, each once")

	unlisted := codewhaleModelInfo(providerModel{ID: "custom", ReasoningEffort: "unknown"}, "")
	assert.False(t, unlisted.IsDefault)
	assert.Len(t, unlisted.SupportedEfforts, len(codewhaleEffortVocabulary)+1, "a model that states no levels takes the runtime's vocabulary")

	none := codewhaleModelInfo(providerModel{ID: "no-effort", ReasoningEffort: capabilityUnsupported}, "")
	assert.Empty(t, none.SupportedEfforts, "a model without the capability gets no effort axis")
	assert.Empty(t, none.DefaultEffort)
}

func TestApplyModelCatalogKeepsTheCurrentModel(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	a.settings.model = "mock-model"
	a.settings.defaultModel = "mock-model"
	a.applyModelCatalog([]providerModel{{ID: "deepseek-flash"}, {ID: " "}})

	require.Len(t, a.models, 2, "an entry with no id is dropped, and the running model is kept")
	assert.Equal(t, "deepseek-flash", a.models[0].Id)
	assert.False(t, a.models[0].IsDefault)
	assert.Equal(t, "mock-model", a.models[1].Id)
	assert.True(t, a.models[1].IsDefault)
	assert.False(t, a.models[1].Hidden, "the running model stays selectable")
}

func TestCurrentImageInput(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	a.catalog = []providerModel{{ID: "a", ImageInput: capabilitySupported}, {ID: "b", ImageInput: capabilityUnsupported}, {ID: "c", ImageInput: "unknown"}}
	for model, want := range map[string]imageInputSupport{"a": imageInputSupported, "b": imageInputUnsupported, "c": imageInputUnknown, "absent": imageInputUnknown} {
		a.settings.model = model
		assert.Equal(t, want, a.currentImageInputLocked(), model)
	}
}

func TestRefreshModelCatalogReadsEveryPage(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := routeProviders + "/custom" + providerRouteModels
	rt.handle(http.MethodGet, route, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			writeFakeJSON(w, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "m1"}}, "nextCursor": "page-2"})
			return
		}
		writeFakeJSON(w, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "m2"}}})
	})
	a, sink := newTestAgent(t, rt)
	a.settings.provider, a.settings.providerID, a.settings.model = "custom", "my-route", "m2"

	a.refreshModelCatalog()

	require.Len(t, a.models, 2)
	assert.Equal(t, "m1", a.models[0].Id)
	assert.Equal(t, "m2", a.models[1].Id)
	requests := rt.requestsTo(http.MethodGet, route)
	require.Len(t, requests, 2)
	first, err := url.ParseQuery(requests[0].RawQuery)
	require.NoError(t, err)
	assert.Equal(t, "250", first.Get("limit"))
	assert.Equal(t, "my-route", first.Get("model_provider_id"), "a named custom route narrows its own catalog")
	second, err := url.ParseQuery(requests[1].RawQuery)
	require.NoError(t, err)
	assert.Equal(t, "page-2", second.Get("cursor"))
	assert.Equal(t, 1, sink.SettingsRefreshCount())
}

func TestRefreshModelCatalogKeepsTheCatalogOnAFailure(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodGet, routeProviders+"/deepseek"+providerRouteModels, http.StatusInternalServerError, "boom")
	a, sink := newTestAgent(t, rt)
	a.settings.provider = "deepseek"
	a.models = []*agent.ModelInfo{{Id: "kept"}}

	a.refreshModelCatalog()
	assert.Equal(t, "kept", a.models[0].Id)
	assert.Zero(t, sink.SettingsRefreshCount())

	// A thread with no provider has no catalog to read.
	a.settings.provider = ""
	a.refreshModelCatalog()
	assert.Len(t, rt.requestsTo(http.MethodGet, routeProviders+"/deepseek"+providerRouteModels), 1)
}

func TestListProviderModelsOmitsARouteThatNamesItsKind(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := routeProviders + "/deepseek" + providerRouteModels
	rt.respondJSON(http.MethodGet, route, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "m"}}})
	a, _ := newTestAgent(t, rt)
	_, err := a.listProviderModels("deepseek", "deepseek")
	require.NoError(t, err)
	query, err := url.ParseQuery(rt.requestsTo(http.MethodGet, route)[0].RawQuery)
	require.NoError(t, err)
	assert.Empty(t, query.Get("model_provider_id"))
}

// A runtime that always states another page cannot hold the walk for ever: it
// stops at the page cap and keeps what it read.
func TestListProviderModelsStopsAtThePageCap(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := routeProviders + "/deepseek" + providerRouteModels
	var served atomic.Int32
	rt.handle(http.MethodGet, route, func(w http.ResponseWriter, _ *http.Request) {
		page := served.Add(1)
		writeFakeJSON(w, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "m" + strconv.Itoa(int(page))}}, "nextCursor": "page-" + strconv.Itoa(int(page)+1)})
	})
	a, _ := newTestAgent(t, rt)

	models, err := a.listProviderModels("deepseek", "")
	require.NoError(t, err)
	assert.Len(t, models, providerModelsMaxPages)
	assert.Len(t, rt.requestsTo(http.MethodGet, route), providerModelsMaxPages)
	assert.Equal(t, "m1", models[0].ID)
	assert.Equal(t, "m"+strconv.Itoa(providerModelsMaxPages), models[len(models)-1].ID)
}

// A page that fails ends the walk with its error, and the walk does not report
// the partial catalog as if it were whole.
func TestListProviderModelsFailsOnAFailedLaterPage(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := routeProviders + "/deepseek" + providerRouteModels
	rt.handle(http.MethodGet, route, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			writeFakeJSON(w, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "m1"}}, "nextCursor": "page-2"})
			return
		}
		writeFakeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": "boom", "status": 500}})
	})
	a, sink := newTestAgent(t, rt)
	models, err := a.listProviderModels("deepseek", "")
	assert.ErrorContains(t, err, "boom")
	assert.Nil(t, models)

	a.settings.provider = "deepseek"
	a.models = []*agent.ModelInfo{{Id: "kept"}}
	a.refreshModelCatalog()
	require.Len(t, a.models, 1)
	assert.Equal(t, "kept", a.models[0].Id, "a partial catalog never replaces the one the agent has")
	assert.Zero(t, sink.SettingsRefreshCount())
}

func TestApplyModelCatalogEdges(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	a.applyModelCatalog([]providerModel{{ID: "m1"}})
	require.Len(t, a.models, 1, "a thread with no model adds no entry of its own")
	assert.Equal(t, "m1", a.models[0].Id)

	a.settings.model = "custom"
	a.applyModelCatalog(nil)
	require.Len(t, a.models, 1, "an empty catalog still offers the running model")
	assert.Equal(t, "custom", a.models[0].Id)
	assert.Nil(t, a.catalog)
	assert.Equal(t, imageInputUnknown, a.currentImageInputLocked(), "a model the catalog does not list leaves images to the runtime")
}
