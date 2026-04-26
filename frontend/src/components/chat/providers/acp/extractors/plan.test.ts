import { describe, expect, it } from 'vitest'
import { acpPlanFromEntries } from './plan'

describe('acpPlanFromEntries', () => {
  it('returns null for null/undefined entries', () => {
    expect(acpPlanFromEntries(null)).toBeNull()
    expect(acpPlanFromEntries(undefined)).toBeNull()
  })

  it('returns an empty source when entries is empty', () => {
    expect(acpPlanFromEntries([])).toEqual({
      toolName: 'Plan',
      title: 'Plan',
      todos: [],
    })
  })

  it('maps pending/completed/in_progress and defaults to pending', () => {
    const source = acpPlanFromEntries([
      { content: 'one', status: 'pending' },
      { content: 'two', status: 'completed' },
      { content: 'three', status: 'in_progress' },
      { content: 'four' },
      { content: 'five', status: 'unknown' },
    ])
    expect(source?.todos).toEqual([
      { content: 'one', status: 'pending', activeForm: '' },
      { content: 'two', status: 'completed', activeForm: '' },
      { content: 'three', status: 'in_progress', activeForm: '' },
      { content: 'four', status: 'pending', activeForm: '' },
      { content: 'five', status: 'pending', activeForm: '' },
    ])
  })

  it('coerces missing content to empty string', () => {
    const source = acpPlanFromEntries([{ status: 'completed' } as never])
    expect(source?.todos).toEqual([
      { content: '', status: 'completed', activeForm: '' },
    ])
  })
})
