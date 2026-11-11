// Package tabcreation validates the public intent of a Worker-owned tab creation.
package tabcreation

import (
	"crypto/sha256"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/proto"
)

const MaxTabIDBytes = 128

// Validate checks the shared request boundary. Canonical layout and presentation
// validation remains on the Hub's manager when it commits the creation.
func Validate(creation *leapmuxv1.TabCreation, kind leapmuxv1.TabType) error {
	if kind < leapmuxv1.TabType_TAB_TYPE_AGENT || kind > leapmuxv1.TabType_TAB_TYPE_IMAGE {
		return errors.New("the tab type cannot be created")
	}
	if creation == nil || creation.GetTabId() == "" || len(creation.GetTabId()) > MaxTabIDBytes {
		return errors.New("a valid fresh tab ID is required")
	}
	for _, character := range []byte(creation.GetTabId()) {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return errors.New("the tab ID must use ASCII letters, digits, underscores, or hyphens")
		}
	}
	placement := creation.GetPlacement()
	if placement == nil || placement.GetWorkspaceId() == "" {
		return errors.New("a destination workspace is required")
	}
	switch target := placement.GetDestination().(type) {
	case *leapmuxv1.TabPlacement_TileId:
		if target.TileId == "" {
			return errors.New("a destination tile is required")
		}
	case *leapmuxv1.TabPlacement_FirstLeaf:
		if target.FirstLeaf == nil {
			return errors.New("the first-leaf destination is missing")
		}
	default:
		return errors.New("an explicit tab destination is required")
	}
	switch placement.GetInsertion() {
	case leapmuxv1.TabInsertion_TAB_INSERTION_FIRST, leapmuxv1.TabInsertion_TAB_INSERTION_LAST:
		if placement.GetRelativeTabId() != "" {
			return errors.New("first and last insertion cannot specify a reference tab")
		}
	case leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE, leapmuxv1.TabInsertion_TAB_INSERTION_AFTER:
		if placement.GetRelativeTabId() == "" {
			return errors.New("before and after insertion require a reference tab")
		}
	default:
		return errors.New("a valid tab insertion is required")
	}
	return nil
}

// Fingerprint gives immutable protobuf requests one deterministic identity.
func Fingerprint(message proto.Message) ([]byte, error) {
	if message == nil || !message.ProtoReflect().IsValid() {
		return nil, errors.New("a creation request is required")
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode creation request: %w", err)
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}
