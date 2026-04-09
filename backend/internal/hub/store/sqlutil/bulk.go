package sqlutil

import (
	"strings"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// BulkGrantWorkspaceAccess applies the grant callback to each workspace access
// grant request in order, stopping at the first error.
func BulkGrantWorkspaceAccess(params []store.GrantWorkspaceAccessParams, grant func(store.GrantWorkspaceAccessParams) error) error {
	for _, p := range params {
		if err := grant(p); err != nil {
			return err
		}
	}
	return nil
}

// BuildWorkspaceTabBulkUpsertQuery builds the common INSERT ... VALUES ...
// prefix and argument list for workspace tab bulk upserts. Callers provide the
// dialect-specific VALUES tuple renderer and conflict clause.
func BuildWorkspaceTabBulkUpsertQuery(
	params []store.UpsertWorkspaceTabParams,
	writeValueTuple func(sb *strings.Builder, rowIndex int),
	conflictClause string,
) (string, []any) {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO workspace_tabs (workspace_id, worker_id, tab_type, tab_id, position, tile_id) VALUES `)

	args := make([]any, 0, len(params)*6)
	for i, p := range params {
		if i > 0 {
			sb.WriteString(", ")
		}
		writeValueTuple(&sb, i)
		args = append(args, p.WorkspaceID, p.WorkerID, p.TabType, p.TabID, p.Position, p.TileID)
	}

	sb.WriteString(conflictClause)
	return sb.String(), args
}
