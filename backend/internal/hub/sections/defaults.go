// Package sections owns the default sidebar sections every user starts with.
//
// It sits below the service layer on purpose. The sections are written in the
// SAME transaction as the user row and nothing backfills them afterwards
// (ListSections is a pure read), so every path that creates a user MUST seed
// them or that user gets an empty sidebar forever. Keeping the definition and
// the write here lets the production path and the test fixtures share one
// implementation -- the fixtures live in a package the service layer imports,
// so they cannot call back into it.
package sections

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// defaultSection is one starting section: its name, its type, and the sidebar
// it belongs to.
type defaultSection struct {
	name        string
	sectionType leapmuxv1.SectionType
	sidebar     leapmuxv1.Sidebar
}

// defaults lists the starting sections in the order they appear, per sidebar.
// Adding one is a line here rather than a seventh copy of the Create block in
// InitDefaults -- which is what keeps their UserID, error handling, and id
// generation identical by construction instead of by inspection.
// An ARRAY, not a slice, so len() below stays a constant expression.
var defaults = [...]defaultSection{
	{"In progress", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS, leapmuxv1.Sidebar_SIDEBAR_LEFT},
	{"Archived", leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED, leapmuxv1.Sidebar_SIDEBAR_LEFT},
	{"Workers", leapmuxv1.SectionType_SECTION_TYPE_WORKERS, leapmuxv1.Sidebar_SIDEBAR_LEFT},
	{"Files", leapmuxv1.SectionType_SECTION_TYPE_FILES, leapmuxv1.Sidebar_SIDEBAR_RIGHT},
	{"To-dos", leapmuxv1.SectionType_SECTION_TYPE_TODOS, leapmuxv1.Sidebar_SIDEBAR_RIGHT},
	{"Background tasks", leapmuxv1.SectionType_SECTION_TYPE_BACKGROUND_TASKS, leapmuxv1.Sidebar_SIDEBAR_RIGHT},
}

// Count is how many sections InitDefaults writes. Tests assert against it so a
// new default section does not silently break an expectation that spelled the
// number out.
const Count = len(defaults)

// InitDefaults writes the default sections for one user.
//
// Call it inside the transaction that writes the user row, so the set lands
// exactly once per user and no read path ever has to create it. It takes the
// store rather than a service, because seeding needs nothing else.
func InitDefaults(ctx context.Context, st store.Store, userID userid.UserID) error {
	// Each sidebar is ranked independently, starting at First() and chaining
	// After() down the list -- the same left/right split the literals encoded by
	// hand, now a consequence of the table's order rather than of separately
	// computed variables that had to be paired with the right rows.
	lastPos := map[leapmuxv1.Sidebar]string{}
	for _, section := range defaults {
		position := lexorank.First()
		if prev, ok := lastPos[section.sidebar]; ok {
			position = lexorank.After(prev)
		}
		lastPos[section.sidebar] = position

		if err := st.WorkspaceSections().Create(ctx, store.CreateWorkspaceSectionParams{
			ID:          id.Generate(),
			UserID:      userID,
			Name:        section.name,
			Position:    position,
			SectionType: section.sectionType,
			Sidebar:     section.sidebar,
		}); err != nil {
			return err
		}
	}
	return nil
}
