package cmd

import (
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/lexorank"
)

// positionKind enumerates the four placement modes tab open / tab move
// accept. The zero value (positionLast) matches the documented default
// so callers that never touch the flags get "append at end" behaviour.
type positionKind uint8

const (
	positionLast positionKind = iota
	positionFirst
	positionBefore
	positionAfter
)

// positionSpec describes where a new (or moved) tab should land
// relative to its sibling tabs on a tile. Exactly one of the four
// flag forms is captured per spec; `refID` is the tab id supplied to
// --before / --after and is empty for --first / --last.
type positionSpec struct {
	kind  positionKind
	refID string
}

// addPositionFlags registers the four placement flags that every
// tab-placement command shares: --first, --last, --before <tab> and
// --after <tab>. positionSpec reads them back.
func addPositionFlags(fs *pflag.FlagSet, verb string) {
	fs.Bool("first", false, fmt.Sprintf("%s first on the destination tile", verb))
	fs.Bool("last", false, fmt.Sprintf("%s last on the destination tile (default)", verb))
	fs.String("before", "", fmt.Sprintf("%s right before this tab", verb))
	fs.String("after", "", fmt.Sprintf("%s right after this tab", verb))
}

// positionSpec reads the placement flags of the invocation. A tab that
// --before or --after gives accepts every target form, so it resolves to a
// full id here.
func (s *session) positionSpec() (positionSpec, error) {
	before, after := stringFlag(s.inv, "before"), stringFlag(s.inv, "after")
	spec, err := parsePositionSpec(boolFlag(s.inv, "first"), boolFlag(s.inv, "last"), before, after)
	if err != nil {
		return positionSpec{}, cli.Usagef("%s", err.Error())
	}
	if spec.refID != "" {
		ref, err := s.resolveOne(resolve.KindTab, spec.refID)
		if err != nil {
			return positionSpec{}, err
		}
		spec.refID = ref.ID
	}
	return spec, nil
}

// parsePositionSpec validates the four placement flags are mutually
// exclusive and returns the resulting spec. None set → positionLast,
// matching the documented default.
func parsePositionSpec(first, last bool, beforeRef, afterRef string) (positionSpec, error) {
	set := 0
	if first {
		set++
	}
	if last {
		set++
	}
	if beforeRef != "" {
		set++
	}
	if afterRef != "" {
		set++
	}
	if set > 1 {
		return positionSpec{}, errors.New("--first, --last, --before, and --after are mutually exclusive")
	}
	switch {
	case first:
		return positionSpec{kind: positionFirst}, nil
	case beforeRef != "":
		return positionSpec{kind: positionBefore, refID: beforeRef}, nil
	case afterRef != "":
		return positionSpec{kind: positionAfter, refID: afterRef}, nil
	default:
		return positionSpec{kind: positionLast}, nil
	}
}

// flagLabelForPositionKind returns the user-facing CLI flag name for a
// positionKind so error messages can reference the actual flag the
// caller typed.
func flagLabelForPositionKind(k positionKind) string {
	switch k {
	case positionFirst:
		return "--first"
	case positionLast:
		return "--last"
	case positionBefore:
		return "--before"
	case positionAfter:
		return "--after"
	}
	return ""
}

// resolvePositionSpec converts a spec into a (tileID, lexorank) pair
// against the bootstrapped state.
//
// destTileID is the caller-preferred destination tile (e.g. via
// --tile, or "" when the spec is allowed to
// derive it from a ref tab). For --first / --last destTileID is
// required; for --before / --after the tile is taken from the ref
// tab's record and, if destTileID is also set, the two must agree.
//
// movingTabID is the tab being moved (empty for `tab open`). When set,
// it is excluded from the sibling-position scan so the moving tab
// doesn't influence its own destination rank, and it is rejected as a
// --before / --after target (a tab cannot reposition relative to
// itself in a single op).
func resolvePositionSpec(
	state *leapmuxv1.UserMaterialized,
	destTileID, movingTabID string,
	spec positionSpec,
) (resolvedTileID, position string, err error) {
	switch spec.kind {
	case positionFirst, positionLast:
		if destTileID == "" {
			return "", "", cli.Usagef("%s needs a destination tile", flagLabelForPositionKind(spec.kind))
		}
		tabs := liveTabsOnTile(state, destTileID, movingTabID)
		if len(tabs) == 0 {
			return destTileID, lexorank.First(), nil
		}
		if spec.kind == positionFirst {
			return destTileID, lexorank.Mid("", tabs[0].GetPosition().GetValue()), nil
		}
		return destTileID, lexorank.After(tabs[len(tabs)-1].GetPosition().GetValue()), nil
	case positionBefore, positionAfter:
		flag := flagLabelForPositionKind(spec.kind)
		if spec.refID == "" {
			return "", "", cli.Usagef("%s needs a tab", flag)
		}
		if spec.refID == movingTabID {
			return "", "", cli.Usagef("%s points at the tab being moved", flag)
		}
		ref, ok := state.GetTabs()[spec.refID]
		if !ok || ref == nil {
			return "", "", cli.Newf(cli.CodeNotFound, "no such tab: %s", spec.refID)
		}
		if !crdt.HLCIsZero(ref.GetTombstoneAt()) {
			return "", "", cli.Newf(cli.CodeNotFound, "tab is tombstoned: %s", spec.refID)
		}
		refTile := ref.GetTileId().GetValue()
		if refTile == "" {
			return "", "", cli.Newf(cli.CodeNotFound, "tab %s has no tile placement", spec.refID)
		}
		if destTileID != "" && destTileID != refTile {
			return "", "", cli.Usagef("%s references tab %s on tile %s, but destination tile is %s", flag, spec.refID, refTile, destTileID)
		}
		refPos := ref.GetPosition().GetValue()
		tabs := liveTabsOnTile(state, refTile, movingTabID)
		if spec.kind == positionBefore {
			return refTile, computeBefore(tabs, refPos), nil
		}
		return refTile, computeAfter(tabs, refPos), nil
	}
	return "", "", cli.Usagef("unknown position kind")
}

// liveTabsOnTile returns the live tabs anchored to tileID, sorted by
// LexoRank position. excludeTabID is dropped from the result so that
// `tab move`'s position computation isn't influenced by the moving
// tab's pre-move record; pass "" when no exclusion is needed.
func liveTabsOnTile(state *leapmuxv1.UserMaterialized, tileID, excludeTabID string) []*leapmuxv1.TabRecord {
	if tileID == "" {
		return nil
	}
	var out []*leapmuxv1.TabRecord
	for _, t := range state.GetTabs() {
		if !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if t.GetTileId().GetValue() != tileID {
			continue
		}
		if t.GetTabId() == excludeTabID {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetPosition().GetValue() < out[j].GetPosition().GetValue()
	})
	return out
}

// computeBefore returns a LexoRank strictly less than refPos but
// greater than the previous live tab's position on the same tile.
// `tabs` must be the tile's live tabs sorted by position.
func computeBefore(tabs []*leapmuxv1.TabRecord, refPos string) string {
	var prevPos string
	for _, t := range tabs {
		p := t.GetPosition().GetValue()
		if p >= refPos {
			break
		}
		prevPos = p
	}
	return lexorank.Mid(prevPos, refPos)
}

// computeAfter returns a LexoRank strictly greater than refPos but
// less than the next live tab's position on the same tile. `tabs`
// must be the tile's live tabs sorted by position.
func computeAfter(tabs []*leapmuxv1.TabRecord, refPos string) string {
	var nextPos string
	for _, t := range tabs {
		p := t.GetPosition().GetValue()
		if p > refPos {
			nextPos = p
			break
		}
	}
	return lexorank.Mid(refPos, nextPos)
}
