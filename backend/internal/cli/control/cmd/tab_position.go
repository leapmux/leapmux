package cmd

import (
	"errors"
	"fmt"
	"slices"
	"strings"

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

// rankedItem pairs an entity ID with its LexoRank position for ordered
// placement calculations across tabs, sections, and workspaces.
type rankedItem struct {
	ID       string
	Position string
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
	return s.resolvePositionRef(spec)
}

// resolvePositionRef resolves a --before / --after target to a full tab ID and
// caches its Hub row on the session so later lookups skip a redundant LocateTab.
func (s *session) resolvePositionRef(spec positionSpec) (positionSpec, error) {
	if spec.refID != "" {
		ref, err := s.resolveOne(resolve.KindTab, spec.refID)
		if err != nil {
			return positionSpec{}, err
		}
		if ref.Tab != nil {
			s.cacheLocatedTab(ref.ID, ref.Tab)
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

// computeRankedPosition calculates a destination LexoRank among peers for an
// item being inserted or moved. It does not mutate peers, excludes movingID,
// rejects self-relative moves, and skips tied ranks when scanning for the
// nearest strictly smaller or strictly larger sibling.
func computeRankedPosition(peers []rankedItem, movingID string, spec positionSpec, noun string) (string, error) {
	filtered := make([]rankedItem, 0, len(peers))
	for _, peer := range peers {
		if movingID != "" && peer.ID == movingID {
			continue
		}
		filtered = append(filtered, peer)
	}
	slices.SortFunc(filtered, func(a, b rankedItem) int {
		if a.Position != b.Position {
			return strings.Compare(a.Position, b.Position)
		}
		return strings.Compare(a.ID, b.ID)
	})

	switch spec.kind {
	case positionBefore, positionAfter:
		if spec.refID == "" {
			return "", cli.Usagef("%s needs a %s", flagLabelForPositionKind(spec.kind), noun)
		}
		if movingID != "" && spec.refID == movingID {
			return "", cli.Usagef("cannot move %s relative to itself", noun)
		}
		idx := slices.IndexFunc(filtered, func(item rankedItem) bool {
			return item.ID == spec.refID
		})
		if idx < 0 {
			return "", cli.Newf(cli.CodeNotFound, "%s %s not found", noun, spec.refID)
		}
		refPos := filtered[idx].Position
		if spec.kind == positionBefore {
			var prevPos string
			for i := idx - 1; i >= 0; i-- {
				if filtered[i].Position < refPos {
					prevPos = filtered[i].Position
					break
				}
			}
			return lexorank.Mid(prevPos, refPos), nil
		}
		var nextPos string
		for i := idx + 1; i < len(filtered); i++ {
			if filtered[i].Position > refPos {
				nextPos = filtered[i].Position
				break
			}
		}
		if nextPos != "" {
			return lexorank.Mid(refPos, nextPos), nil
		}
		return lexorank.After(refPos), nil
	case positionFirst:
		if len(filtered) == 0 {
			return lexorank.First(), nil
		}
		return lexorank.Mid("", filtered[0].Position), nil
	case positionLast:
		if len(filtered) == 0 {
			return lexorank.First(), nil
		}
		return lexorank.After(filtered[len(filtered)-1].Position), nil
	default:
		return "", cli.Usagef("unknown position kind")
	}
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
		pos, err := computeRankedPosition(liveTabPeersOnTile(state, destTileID), movingTabID, spec, "tab")
		if err != nil {
			return "", "", err
		}
		return destTileID, pos, nil
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
			return "", "", cli.Usagef("%s refers to tab %s on tile %s, but destination tile is %s", flag, spec.refID, refTile, destTileID)
		}
		pos, err := computeRankedPosition(liveTabPeersOnTile(state, refTile), movingTabID, spec, "tab")
		if err != nil {
			return "", "", err
		}
		return refTile, pos, nil
	}
	return "", "", cli.Usagef("unknown position kind")
}

// liveTabPeersOnTile returns the live tabs anchored to tileID as ranked items.
func liveTabPeersOnTile(state *leapmuxv1.UserMaterialized, tileID string) []rankedItem {
	if tileID == "" {
		return nil
	}
	var out []rankedItem
	for _, t := range state.GetTabs() {
		if !crdt.HLCIsZero(t.GetTombstoneAt()) {
			continue
		}
		if t.GetTileId().GetValue() != tileID {
			continue
		}
		out = append(out, rankedItem{
			ID:       t.GetTabId(),
			Position: t.GetPosition().GetValue(),
		})
	}
	return out
}
