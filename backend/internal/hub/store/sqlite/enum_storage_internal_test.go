package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
)

// TestEnumColumnsRefuseValuesOutsideTheirOrdinals proves with real writes what
// TestSQLiteEnumColumnsStoreIntegersOnly and
// TestEnumColumnChecksMatchTheirProtoRanges prove from the migration text.
//
// SQLite stores any value in an INTEGER column, so a range CHECK alone admits
// 1.5, and the row then reads back as an ordinal no Go constant has. Each
// column refuses a fraction, the UNSPECIFIED zero, and the first ordinal past
// the last one it admits.
func TestEnumColumnsRefuseValuesOutsideTheirOrdinals(t *testing.T) {
	t.Parallel()

	st, db := newSessionTestStore(t)
	ctx := context.Background()
	user := storetest.SeedUser(t, st, "enum-storage-user")
	workspaceID := storetest.SeedWorkspace(t, st, user.ID, "enum-storage-ws")

	for _, c := range []struct {
		column string
		valid  int32
		// last is the highest ordinal the column admits.
		last   int32
		insert func(value any) error
	}{
		{
			column: "workspace_tab_owned.tab_type",
			valid:  int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
			last:   int32(leapmuxv1.TabType_TAB_TYPE_IMAGE),
			insert: func(value any) error {
				_, err := db.ExecContext(ctx,
					`INSERT INTO workspace_tab_owned (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position, rendered)
					 VALUES (?, ?, ?, ?, 'worker', 'tile', 'a', 0)`,
					user.ID, workspaceID, value, id.Generate())
				return err
			},
		},
		{
			column: "device_authorizations.state",
			valid:  int32(leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING),
			last:   int32(leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_DENIED),
			insert: func(value any) error {
				_, err := db.ExecContext(ctx,
					`INSERT INTO device_authorizations (device_code, user_code, client_id, state, expires_at)
					 VALUES (?, ?, ?, ?, ?)`,
					id.Generate(), id.Generate(), oauthapp.ControlCLIClientID, value,
					time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"))
				return err
			},
		},
		{
			column: "workspace_sections.section_type",
			// CUSTOM, the one type a user may hold more than once.
			valid: int32(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM),
			last:  int32(leapmuxv1.SectionType_SECTION_TYPE_BACKGROUND_TASKS),
			insert: func(value any) error {
				_, err := db.ExecContext(ctx,
					`INSERT INTO workspace_sections (id, user_id, name, position, section_type, sidebar)
					 VALUES (?, ?, 'section', 'a', ?, ?)`,
					id.Generate(), user.ID, value, int32(leapmuxv1.Sidebar_SIDEBAR_LEFT))
				return err
			},
		},
		{
			column: "workspace_sections.sidebar",
			valid:  int32(leapmuxv1.Sidebar_SIDEBAR_LEFT),
			last:   int32(leapmuxv1.Sidebar_SIDEBAR_RIGHT),
			insert: func(value any) error {
				_, err := db.ExecContext(ctx,
					`INSERT INTO workspace_sections (id, user_id, name, position, section_type, sidebar)
					 VALUES (?, ?, 'section', 'a', ?, ?)`,
					id.Generate(), user.ID, int32(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM), value)
				return err
			},
		},
	} {
		t.Run(c.column, func(t *testing.T) {
			require.NoError(t, c.insert(c.valid), "a declared ordinal must be storable")
			for name, value := range map[string]any{
				"a fraction":                    1.5,
				"the UNSPECIFIED zero":          0,
				"the ordinal past the last one": c.last + 1,
			} {
				err := c.insert(value)
				if assert.Error(t, err, "%s must refuse %s", c.column, name) {
					assert.Contains(t, err.Error(), "CHECK constraint failed", "%s must refuse %s with its CHECK", c.column, name)
				}
			}
		})
	}
}
