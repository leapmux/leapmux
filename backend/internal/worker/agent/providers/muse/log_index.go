package muse

import (
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
)

type nativeItemIdentity struct {
	sessionID string
	turnID    string
	callID    string
	stream    nativeStream
	first     nativePosition
}

func nativeIdentity(item itemParams) (nativeItemIdentity, bool) {
	if item.Item.TurnID == nil || item.SessionID == "" || item.Item.CallID == "" || item.SourceRange.First.ID == "" || item.SourceRange.First.Sequence <= 0 || item.SourceRange.Stream.Kind != contracts.MuseStreamKindSession || item.SourceRange.Stream.ID != item.SessionID {
		return nativeItemIdentity{}, false
	}
	return nativeItemIdentity{sessionID: item.SessionID, turnID: *item.Item.TurnID, callID: item.Item.CallID, stream: item.SourceRange.Stream, first: item.SourceRange.First}, true
}

type nativeItemIndex struct {
	identities map[nativeItemIdentity]map[string]*itemState
	origins    map[nativeOriginIdentity]map[string]*itemState
}

type nativeOriginIdentity struct {
	stream   nativeStream
	position nativePosition
}

func (index *nativeItemIndex) rebuild(items map[string]*itemState) {
	index.identities = make(map[nativeItemIdentity]map[string]*itemState)
	index.origins = make(map[nativeOriginIdentity]map[string]*itemState)
	for _, item := range items {
		index.replace(itemParams{}, item)
	}
}

func putNativeMember[K comparable, V any](groups map[K]map[string]V, key K, id string, value V) map[K]map[string]V {
	if groups == nil {
		groups = make(map[K]map[string]V)
	}
	if groups[key] == nil {
		groups[key] = make(map[string]V)
	}
	groups[key][id] = value
	return groups
}

func removeNativeMember[K comparable, V any](groups map[K]map[string]V, key K, id string) {
	group := groups[key]
	delete(group, id)
	if len(group) == 0 {
		delete(groups, key)
	}
}

func (index *nativeItemIndex) replace(previous itemParams, item *itemState) {
	if key, valid := nativeIdentity(previous); valid {
		removeNativeMember(index.identities, key, previous.Item.ID)
		removeNativeMember(index.origins, nativeOriginIdentity{stream: key.stream, position: key.first}, previous.Item.ID)
	}
	if item == nil || item.params.Item.ID == "" {
		return
	}
	if key, valid := nativeIdentity(item.params); valid {
		index.identities = putNativeMember(index.identities, key, item.params.Item.ID, item)
		index.origins = putNativeMember(index.origins, nativeOriginIdentity{stream: key.stream, position: key.first}, item.params.Item.ID, item)
	}
}

func (state *sessionState) ensureNativeItemIndex() *nativeItemIndex {
	if state.itemIndex == nil {
		state.itemIndex = &nativeItemIndex{}
		state.itemIndex.rebuild(state.items)
	}
	return state.itemIndex
}

func (index *nativeItemIndex) candidates(params itemParams) []itemParams {
	key, valid := nativeIdentity(params)
	if !valid {
		return nil
	}
	group := index.identities[key]
	items := make([]itemParams, 0, len(group))
	for _, item := range group {
		items = append(items, item.params)
	}
	return items
}

type nativeBatchIdentity struct {
	stream nativeStream
	turnID string
	batch  string
}

type nativeBatchIndex struct {
	results map[nativeBatchIdentity]map[string]struct{}
	origins map[nativeBatchIdentity]map[string]nativePosition
}

func (index *nativeBatchIndex) rebuild(records map[string]storedRecord) {
	index.results = make(map[nativeBatchIdentity]map[string]struct{})
	index.origins = make(map[nativeBatchIdentity]map[string]nativePosition)
	for _, record := range records {
		index.add(record.record)
	}
}

func (index *nativeBatchIndex) add(record nativeRecord) {
	event := record.Payload.Event
	key := nativeBatchIdentity{stream: record.Stream, turnID: record.Payload.RunID}
	switch event.Kind {
	case contracts.MuseLogEventAssistantToolCallsCommitted:
		key.batch = event.MessageID
		index.origins = putNativeMember(index.origins, key, record.ID, nativePosition{ID: record.ID, Sequence: record.Sequence})
	case contracts.MuseLogEventToolResultBatchCommitted:
		key.batch = event.BatchID
		index.results = putNativeMember(index.results, key, record.ID, struct{}{})
	}
}

func (log *nativeLog) ensureNativeBatchIndex() *nativeBatchIndex {
	if log.batchIndex == nil {
		log.batchIndex = &nativeBatchIndex{}
		log.batchIndex.rebuild(log.records)
	}
	return log.batchIndex
}

// cloneItemParams retains every mutable native field independently of the live item.
func cloneItemParams(params itemParams) itemParams {
	if params.Item.TurnID != nil {
		turn := *params.Item.TurnID
		params.Item.TurnID = &turn
	}
	if params.Item.OutputReference != nil {
		reference := *params.Item.OutputReference
		params.Item.OutputReference = &reference
	}
	if params.Item.PatchReference != nil {
		reference := *params.Item.PatchReference
		params.Item.PatchReference = &reference
	}
	params.Item.ModelVisibleContent = slices.Clone(params.Item.ModelVisibleContent)
	params.Item.Children = slices.Clone(params.Item.Children)
	return params
}
