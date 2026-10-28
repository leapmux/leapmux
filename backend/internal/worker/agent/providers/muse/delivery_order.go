package muse

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// unfinishedChildTextBeforeInput finds a proved earlier revision without a
// retained row. Its eventual final revision must establish its position before
// a later input can publish. The caller holds stateMu.
func unfinishedChildTextBeforeInput(id string, state *sessionState, marker childInputMarker) (string, *nativeSourceRange) {
	for _, item := range state.items {
		params := item.params
		if item.retainedRevision >= params.Item.Revision || !provedUnfinishedChildText(id, item) || params.SourceRange.Last.Sequence > marker.position.Sequence {
			continue
		}
		resolved := false
		for _, frame := range state.projection.frames {
			line := providerkit.ParseLine(frame.raw)
			if line.Method != contracts.MuseMethodItemCompleted && line.Method != contracts.MuseMethodItemUpdated {
				continue
			}
			var final itemParams
			if json.Unmarshal(line.Params, &final) != nil || final.SessionID != id || final.Item.ID != params.Item.ID || final.Item.Kind != params.Item.Kind || final.Item.Revision <= params.Item.Revision || final.Item.Status == contracts.MuseItemStatusInProgress {
				continue
			}
			source, err := childProjectionAnchor(frame.raw)
			if err != nil || source == nil || source.Last.Sequence < marker.position.Sequence {
				continue
			}
			complete, err := state.log.hasNativePrefixThrough(source.Last)
			if err != nil || !complete {
				return "The native child text position remains unresolved.", source
			}
			if state.log.sequences[source.First.Sequence] != source.First.ID {
				continue
			}
			resolved = true
			break
		}
		if !resolved {
			return "An earlier native child text item remains unfinished.", nil
		}
	}
	return "", nil
}

func provedUnfinishedChildText(id string, item *itemState) bool {
	params := item.params
	return len(item.raw) > 0 && params.Item.Status == contracts.MuseItemStatusInProgress && (params.Item.Kind == contracts.MuseItemKindAgentMessage || params.Item.Kind == contracts.MuseItemKindReasoning) && params.SourceRange.Stream == (nativeStream{Kind: contracts.MuseStreamKindSession, ID: id}) && params.SourceRange.First.ID != "" && params.SourceRange.First.Sequence > 0 && params.SourceRange.Last.ID != "" && params.SourceRange.Last.Sequence >= params.SourceRange.First.Sequence
}
