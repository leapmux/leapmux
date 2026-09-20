import { describe, expect, it } from 'vitest'
import { claudeTodoItems } from './todo'

describe('claudeTodoItems', () => {
  it('returns null for null/undefined input', () => {
    expect(claudeTodoItems(null)).toBeNull()
    expect(claudeTodoItems(undefined)).toBeNull()
  })

  it('returns null when todos is missing or not an array', () => {
    expect(claudeTodoItems({})).toBeNull()
    expect(claudeTodoItems({ todos: 'oops' as unknown as never[] })).toBeNull()
    expect(claudeTodoItems({ other: 1 })).toBeNull()
  })

  // An empty list is a list the agent CLEARED, which the row words for itself.
  // Null is the different answer "this payload holds no list at all".
  it('extracts an empty todos list (empty state)', () => {
    expect(claudeTodoItems({ todos: [] })).toEqual([])
  })

  it('extracts each status and keys every row', () => {
    expect(claudeTodoItems({
      todos: [
        { content: 'Do A', status: 'pending', activeForm: 'Doing A' },
        { content: 'Do B', status: 'in_progress', activeForm: 'Doing B' },
        { content: 'Do C', status: 'completed', activeForm: 'Doing C' },
      ],
    })).toEqual([
      { rowKey: '0:Do A', content: 'Do A', status: 'pending', activeForm: 'Doing A' },
      { rowKey: '1:Do B', content: 'Do B', status: 'in_progress', activeForm: 'Doing B' },
      { rowKey: '2:Do C', content: 'Do C', status: 'completed', activeForm: 'Doing C' },
    ])
  })

  it('coerces missing fields to empty strings and unknown statuses to pending', () => {
    expect(claudeTodoItems({ todos: [{ status: 'unknown' }] })).toEqual([
      { rowKey: '0:', content: '', status: 'pending', activeForm: '' },
    ])
  })
})
