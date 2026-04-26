import { describe, expect, it } from 'vitest'
import { claudeTodoWriteFromInput } from './todo'

describe('claudeTodoWriteFromInput', () => {
  it('returns null for null/undefined input', () => {
    expect(claudeTodoWriteFromInput(null)).toBeNull()
    expect(claudeTodoWriteFromInput(undefined)).toBeNull()
  })

  it('returns null when todos is missing or not an array', () => {
    expect(claudeTodoWriteFromInput({})).toBeNull()
    expect(claudeTodoWriteFromInput({ todos: 'oops' as unknown as never[] })).toBeNull()
    expect(claudeTodoWriteFromInput({ other: 1 })).toBeNull()
  })

  it('extracts an empty todos list (empty state)', () => {
    expect(claudeTodoWriteFromInput({ todos: [] })).toEqual({
      toolName: 'TodoWrite',
      title: '0 tasks',
      todos: [],
    })
  })

  it('extracts statuses and pluralizes the title', () => {
    const source = claudeTodoWriteFromInput({
      todos: [
        { content: 'Do A', status: 'pending', activeForm: 'Doing A' },
        { content: 'Do B', status: 'in_progress', activeForm: 'Doing B' },
        { content: 'Do C', status: 'completed', activeForm: 'Doing C' },
      ],
    })
    expect(source).toEqual({
      toolName: 'TodoWrite',
      title: '3 tasks',
      todos: [
        { content: 'Do A', status: 'pending', activeForm: 'Doing A' },
        { content: 'Do B', status: 'in_progress', activeForm: 'Doing B' },
        { content: 'Do C', status: 'completed', activeForm: 'Doing C' },
      ],
    })
  })

  it('singularizes for one task', () => {
    const source = claudeTodoWriteFromInput({
      todos: [{ content: 'X', status: 'pending', activeForm: 'Xing' }],
    })
    expect(source?.title).toBe('1 task')
  })

  it('coerces missing fields to empty strings and unknown statuses to pending', () => {
    const source = claudeTodoWriteFromInput({
      todos: [{ status: 'unknown' }],
    })
    expect(source?.todos).toEqual([
      { content: '', status: 'pending', activeForm: '' },
    ])
  })
})
