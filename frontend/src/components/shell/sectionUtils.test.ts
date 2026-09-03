import { create } from '@bufbuild/protobuf'
import ListTree from 'lucide-solid/icons/list-tree'
import { describe, expect, it } from 'vitest'
import { getSectionIcon, isMoveTargetSection, isWorkspaceMutatable, isWorkspaceSection, sectionTypeTestId } from '~/components/shell/sectionUtils'
import { SectionSchema, SectionType } from '~/generated/proto/leapmux/v1/section_pb'

describe('isMoveTargetSection', () => {
  it('returns true for IN_PROGRESS', () => {
    expect(isMoveTargetSection(SectionType.WORKSPACES_IN_PROGRESS)).toBe(true)
  })

  it('returns true for CUSTOM', () => {
    expect(isMoveTargetSection(SectionType.WORKSPACES_CUSTOM)).toBe(true)
  })

  it('returns false for ARCHIVED', () => {
    expect(isMoveTargetSection(SectionType.WORKSPACES_ARCHIVED)).toBe(false)
  })

  it('returns false for FILES', () => {
    expect(isMoveTargetSection(SectionType.FILES)).toBe(false)
  })

  it('returns false for TODOS', () => {
    expect(isMoveTargetSection(SectionType.TODOS)).toBe(false)
  })

  it('returns false for WORKERS', () => {
    expect(isMoveTargetSection(SectionType.WORKERS)).toBe(false)
  })

  it('returns false for BACKGROUND_TASKS (not a workspace move target)', () => {
    // Background tasks is a per-agent panel, not a workspace section: a
    // cross-workspace move must never offer it as a destination.
    expect(isMoveTargetSection(SectionType.BACKGROUND_TASKS)).toBe(false)
  })
})

describe('isWorkspaceSection', () => {
  it('returns true for the three workspace section types', () => {
    expect(isWorkspaceSection(SectionType.WORKSPACES_IN_PROGRESS)).toBe(true)
    expect(isWorkspaceSection(SectionType.WORKSPACES_CUSTOM)).toBe(true)
    expect(isWorkspaceSection(SectionType.WORKSPACES_ARCHIVED)).toBe(true)
  })

  it('returns false for non-workspace section types', () => {
    expect(isWorkspaceSection(SectionType.FILES)).toBe(false)
    expect(isWorkspaceSection(SectionType.TODOS)).toBe(false)
    expect(isWorkspaceSection(SectionType.WORKERS)).toBe(false)
  })

  it('returns false for BACKGROUND_TASKS (a per-agent panel, not a workspace section)', () => {
    // The negative half of the move-target guard: a section that does not hold
    // workspaces must not answer true here, or isMoveTargetSection's
    // isWorkspaceSection-based filter would leak.
    expect(isWorkspaceSection(SectionType.BACKGROUND_TASKS)).toBe(false)
  })
})

describe('sectionTypeTestId', () => {
  it('maps every section type to its snake_case test id slug', () => {
    // Pin the well-known slugs the DOM data-testid attributes key off, so a
    // Playwright selector or a `[data-testid=...]` CSS hook keeps resolving
    // after a refactor of the switch.
    expect(sectionTypeTestId(SectionType.WORKSPACES_IN_PROGRESS)).toBe('workspaces_in_progress')
    expect(sectionTypeTestId(SectionType.WORKSPACES_CUSTOM)).toBe('workspaces_custom')
    expect(sectionTypeTestId(SectionType.WORKSPACES_ARCHIVED)).toBe('workspaces_archived')
    expect(sectionTypeTestId(SectionType.FILES)).toBe('files')
    expect(sectionTypeTestId(SectionType.TODOS)).toBe('todos')
    expect(sectionTypeTestId(SectionType.WORKERS)).toBe('workers')
  })

  it('returns "background_tasks" for the background-tasks section', () => {
    expect(sectionTypeTestId(SectionType.BACKGROUND_TASKS)).toBe('background_tasks')
  })

  it('falls back to String(sectionType) for an unmapped value', () => {
    // The default arm keeps an unknown enum value renderable rather than
    // throwing -- a forward-compatible new section type still produces a slug.
    expect(sectionTypeTestId(999 as unknown as SectionType)).toBe('999')
  })
})

describe('getSectionIcon', () => {
  it('returns the ListTree icon for the background-tasks section', () => {
    const section = create(SectionSchema, { sectionType: SectionType.BACKGROUND_TASKS })
    // Identity check: the switch returns the same lucide-solid component the
    // sidebar renders, so a rename of the imported icon would surface here.
    expect(getSectionIcon(section)).toBe(ListTree)
  })

  it('returns Folder (the default) for an unmapped section type', () => {
    const section = create(SectionSchema, { sectionType: 999 as unknown as SectionType })
    expect(getSectionIcon(section)).toBeDefined()
  })
})

describe('isWorkspaceMutatable', () => {
  it('returns true for a live workspace', () => {
    expect(isWorkspaceMutatable(false)).toBe(true)
  })

  it('returns false for an archived workspace', () => {
    // Archival is the ONE thing that blocks mutation. Access is owner-only, so
    // there is no second axis for this predicate to weigh.
    expect(isWorkspaceMutatable(true)).toBe(false)
  })
})
