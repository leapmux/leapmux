import { describe, expect, it } from 'vitest'
import { isMoveTargetSection, isWorkspaceMutatable } from '~/components/shell/sectionUtils'
import { SectionType } from '~/generated/leapmux/v1/section_pb'

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

  it('returns false for SHARED', () => {
    expect(isMoveTargetSection(SectionType.WORKSPACES_SHARED)).toBe(false)
  })

  it('returns false for FILES', () => {
    expect(isMoveTargetSection(SectionType.FILES)).toBe(false)
  })

  it('returns false for TODOS', () => {
    expect(isMoveTargetSection(SectionType.TODOS)).toBe(false)
  })
})

describe('isWorkspaceMutatable', () => {
  const userId = 'user-1'

  it('returns true for owner on non-archived workspace', () => {
    expect(isWorkspaceMutatable({ createdBy: userId }, userId, false)).toBe(true)
  })

  it('returns false for non-owner', () => {
    expect(isWorkspaceMutatable({ createdBy: 'other-user' }, userId, false)).toBe(false)
  })

  it('returns false for archived workspace', () => {
    expect(isWorkspaceMutatable({ createdBy: userId }, userId, true)).toBe(false)
  })

  it('returns false when workspace is undefined', () => {
    expect(isWorkspaceMutatable(undefined, userId, false)).toBe(false)
  })

  it('returns false for non-owner on archived workspace', () => {
    expect(isWorkspaceMutatable({ createdBy: 'other-user' }, userId, true)).toBe(false)
  })
})
