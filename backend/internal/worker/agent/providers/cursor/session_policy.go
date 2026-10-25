package cursor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// readCursorBypassPolicy reads the permission state that survives a native restart.
// Cursor's ACP runtime sets isRunEverything with --force but never clears it.
func readCursorBypassPolicy(ctx context.Context, query agent.StoredSessionQuery, sessionID string) (bool, error) {
	path := cursorACPStorePath(query, sessionID)
	if path == "" {
		return false, errors.New("cannot locate the Cursor session permission state")
	}
	db, err := sessionstore.OpenDB(ctx, path)
	if err != nil {
		return false, fmt.Errorf("read Cursor session permission state: %w", err)
	}
	defer func() { _ = db.Close() }()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, cursorMetaKey).Scan(&raw); err != nil {
		return false, fmt.Errorf("read Cursor session permission metadata: %w", err)
	}
	data := []byte(raw)
	if decoded, err := hex.DecodeString(strings.TrimSpace(raw)); err == nil {
		data = decoded
	}
	var metadata *struct {
		AgentID         string          `json:"agentId"`
		IsRunEverything json.RawMessage `json:"isRunEverything"`
	}
	if json.Unmarshal(data, &metadata) != nil || metadata == nil || metadata.AgentID != sessionID {
		return false, errors.New("invalid Cursor session permission metadata or session identity")
	}
	// Missing flags predate enabling Run Everything. Explicit malformed values
	// cannot establish the native policy, so they must never be reported as Off.
	switch strings.TrimSpace(string(metadata.IsRunEverything)) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, errors.New("invalid isRunEverything value in Cursor session permission metadata")
	}
}
