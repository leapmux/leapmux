import type { SectionDefContext } from './buildSectionDef'
import { create } from '@bufbuild/protobuf'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { SectionSchema, SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { buildSectionDef, RIGHT_SIDEBAR_DEFAULT_SIZES } from './buildSectionDef'

describe('right sidebar default sizes', () => {
  it('uses the requested 0.6:0.15:0.25 ratio', () => {
    expect([
      RIGHT_SIDEBAR_DEFAULT_SIZES[SectionType.FILES],
      RIGHT_SIDEBAR_DEFAULT_SIZES[SectionType.TODOS],
      RIGHT_SIDEBAR_DEFAULT_SIZES[SectionType.BACKGROUND_TASKS],
    ]).toEqual([0.6, 0.15, 0.25])
  })

  it('renders no 0/0 rail badge for a goal-only section', () => {
    const section = create(SectionSchema, {
      id: 'todos',
      name: 'Goals & To-dos',
      sectionType: SectionType.TODOS,
    })
    const definition = buildSectionDef(section, {
      activeTodos: [],
      activeGoal: { progress: {}, actions: ['set'] },
      showGoalsAndTodos: true,
    } as unknown as SectionDefContext)
    const { container } = render(() => definition.railBadge?.())
    expect(container.textContent).toBe('')
  })
})
