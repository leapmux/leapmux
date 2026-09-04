package service

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// nextSectionPosition places a new custom section directly above "Archived".
//
// The rule used to be reconstructed from two scanned anchors -- the last CUSTOM
// position and the Archived position -- and handed to lexorank.Mid as a pair.
// That holds only while the user leaves the seeded order alone. MoveSection
// writes any position a client asks for and every section is draggable, so the
// pair could arrive DESCENDING, which lexorank.between does not take: it
// answered a rank below Archived, or a rank equal to a live section.
//
// These cases pin the ORDER-derived rule instead. Each one states the sidebar
// it describes, because the scan must ignore the other sidebar entirely.

// One section of a sidebar, spelled short so a case reads as a list.
func sec(sidebar leapmuxv1.Sidebar, sectionType leapmuxv1.SectionType, position string) store.WorkspaceSection {
	return store.WorkspaceSection{SectionType: sectionType, Sidebar: sidebar, Position: position}
}

const (
	left  = leapmuxv1.Sidebar_SIDEBAR_LEFT
	right = leapmuxv1.Sidebar_SIDEBAR_RIGHT

	inProgress = leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_IN_PROGRESS
	archived   = leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED
	custom     = leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM
	workers    = leapmuxv1.SectionType_SECTION_TYPE_WORKERS
)

// The seeded layout, in the (sidebar, position) order ListByUserID returns.
func seededSections() []store.WorkspaceSection {
	return []store.WorkspaceSection{
		sec(left, inProgress, "n"),
		sec(left, archived, "nn"),
		sec(left, workers, "nnn"),
	}
}

func TestNextSectionPosition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		sections []store.WorkspaceSection
		sidebar  leapmuxv1.Sidebar
		// The row the result must sort AFTER, and the row it must sort BEFORE.
		// An empty string means "no bound on that side".
		after  string
		before string
	}{
		{
			name:     "lands between In progress and Archived on a seeded sidebar",
			sections: seededSections(),
			sidebar:  left,
			after:    "n",
			before:   "nn",
		},
		{
			name: "lands after an existing custom section and still above Archived",
			sections: []store.WorkspaceSection{
				sec(left, inProgress, "n"),
				sec(left, custom, "ng"),
				sec(left, archived, "nn"),
			},
			sidebar: left,
			after:   "ng",
			before:  "nn",
		},
		{
			// The defect the ordered rule exists for: the pair the old scan
			// built was descending here, so lexorank answered a rank BELOW
			// Archived and every later create repeated it.
			name: "stays above Archived when a custom section was dragged below it",
			sections: []store.WorkspaceSection{
				sec(left, inProgress, "n"),
				sec(left, archived, "nn"),
				sec(left, custom, "nng"),
			},
			sidebar: left,
			after:   "n",
			before:  "nn",
		},
		{
			// The mirror defect, which needed no byte wrap: with no custom
			// section the old code called Mid("", archivedPos), and `before`
			// knows only Archived -- not the row that now precedes it. It
			// answered "ng", byte-identical to the dragged Workers row.
			name: "does not collide with a built-in dragged above Archived",
			sections: []store.WorkspaceSection{
				sec(left, inProgress, "n"),
				sec(left, workers, "ng"),
				sec(left, archived, "nn"),
			},
			sidebar: left,
			after:   "ng",
			before:  "nn",
		},
		{
			// The right sidebar carries no Archived anchor, so the section
			// appends past the last row there.
			name: "appends past the last section of a sidebar with no Archived",
			sections: []store.WorkspaceSection{
				sec(left, inProgress, "n"),
				sec(left, archived, "nn"),
				sec(right, workers, "n"),
			},
			sidebar: right,
			after:   "n",
			before:  "",
		},
		{
			name:     "heads a sidebar whose Archived row sorts first",
			sections: []store.WorkspaceSection{sec(left, archived, "n")},
			sidebar:  left,
			after:    "",
			before:   "n",
		},
		{
			name:     "answers a usable rank for an empty sidebar",
			sections: nil,
			sidebar:  right,
			after:    "",
			before:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := nextSectionPosition(tc.sections, tc.sidebar)
			require.NotEmpty(t, got, "a section with no position cannot be ordered at all")

			if tc.after != "" {
				assert.Greater(t, got, tc.after, "must sort after %q", tc.after)
			}
			if tc.before != "" {
				assert.Less(t, got, tc.before, "must sort before %q", tc.before)
			}
			// A rank equal to a live row is the collision the ordered rule
			// removes, so no section of this sidebar may share it.
			for _, s := range tc.sections {
				if s.Sidebar == tc.sidebar {
					assert.NotEqual(t, s.Position, got, "collides with an existing section")
				}
			}
		})
	}
}

// Two creates in a row must not land on the same rank, whichever sidebar they
// go to -- a collision is what the old First() fallback produced.
//
// The new row is inserted in (sidebar, position) order rather than appended,
// because that is the order ListByUserID guarantees and the scan reads.
func TestNextSectionPositionDoesNotRepeatItself(t *testing.T) {
	t.Parallel()

	for _, sidebar := range []leapmuxv1.Sidebar{left, right} {
		sections := seededSections()
		first := nextSectionPosition(sections, sidebar)
		sections = insertOrdered(sections, sec(sidebar, custom, first))
		second := nextSectionPosition(sections, sidebar)

		assert.NotEqual(t, first, second, "sidebar %v", sidebar)

		sections = insertOrdered(sections, sec(sidebar, custom, second))
		assert.NotEqual(t, second, nextSectionPosition(sections, sidebar), "sidebar %v", sidebar)
	}
}

// Put `row` where ListByUserID's `ORDER BY sidebar, position` would put it.
func insertOrdered(sections []store.WorkspaceSection, row store.WorkspaceSection) []store.WorkspaceSection {
	out := append([]store.WorkspaceSection{}, sections...)
	out = append(out, row)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sidebar != out[j].Sidebar {
			return out[i].Sidebar < out[j].Sidebar
		}
		return out[i].Position < out[j].Position
	})
	return out
}

// The scan reads one sidebar and ignores the other, so a section on the far
// side can neither anchor nor displace this one.
func TestNextSectionPositionIgnoresTheOtherSidebar(t *testing.T) {
	t.Parallel()

	withRightNoise := append(seededSections(),
		sec(right, custom, "a"),
		sec(right, archived, "z"),
	)
	assert.Equal(t,
		nextSectionPosition(seededSections(), left),
		nextSectionPosition(withRightNoise, left))
}
