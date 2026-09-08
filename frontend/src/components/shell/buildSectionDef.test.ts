import type { SectionDefContext } from './buildSectionDef'
import { create } from '@bufbuild/protobuf'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { SectionSchema, SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { buildSectionDef, SECTION_DEFAULT_SIZES } from './buildSectionDef'

/** A context with only the fields the section under test reads. */
function context(over: Partial<SectionDefContext> = {}): SectionDefContext {
  return {
    activeTodos: [],
    activeGoal: { progress: {}, actions: ['set'] },
    showGoalsAndTodos: true,
    showBackgroundTasks: true,
    activeBackgroundTasks: [],
    activeBackgroundTasksFailed: false,
    ...over,
  } as unknown as SectionDefContext
}

function sectionOf(sectionType: SectionType, name: string) {
  return create(SectionSchema, { id: `s-${sectionType}`, name, sectionType })
}

describe('section default sizes', () => {
  /**
   * The definition each sidebar actually reads, not the table's own literals.
   * Asserting the constant against the numbers it declares three lines away
   * exercises nothing: deleting every `defaultSize:` line still passed.
   */
  it('puts the declared size of each section type on its definition', () => {
    expect(buildSectionDef(sectionOf(SectionType.TODOS, 'Goals & To-dos'), context()).defaultSize)
      .toBe(SECTION_DEFAULT_SIZES[SectionType.TODOS])
    expect(buildSectionDef(sectionOf(SectionType.BACKGROUND_TASKS, 'Background tasks'), context()).defaultSize)
      .toBe(SECTION_DEFAULT_SIZES[SectionType.BACKGROUND_TASKS])
  })

  it('uses the requested 0.55:0.20:0.25 right-sidebar ratio', () => {
    expect([
      SECTION_DEFAULT_SIZES[SectionType.FILES],
      SECTION_DEFAULT_SIZES[SectionType.TODOS],
      SECTION_DEFAULT_SIZES[SectionType.BACKGROUND_TASKS],
    ]).toEqual([0.55, 0.2, 0.25])
  })

  // A workspace section declares nothing, and distributeSectionSizes gives it a
  // share of what the declared ones leave. A number here would over-subscribe
  // the sidebar the moment a second workspace section appeared.
  it('declares no size for a workspace section', () => {
    expect(SECTION_DEFAULT_SIZES[SectionType.WORKSPACES_CUSTOM]).toBeUndefined()
  })

  it('renders no 0/0 rail badge for a goal-only section', () => {
    const definition = buildSectionDef(sectionOf(SectionType.TODOS, 'Goals & To-dos'), context())
    const { container } = render(() => definition.railBadge?.())
    expect(container.textContent).toBe('')
  })
})
