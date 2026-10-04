import { describe, expect, it } from 'vitest'
import { ohMyPiTodoSource } from './todo'

describe('ohMyPiTodoSource', () => {
  it('reads the snapshot omp states after an operation', () => {
    // omp 18.2.11's own details (probe s2).
    const source = ohMyPiTodoSource({ op: 'init' }, {
      op: 'init',
      phases: [{ name: 'Build', tasks: [{ content: 'Write code', status: 'in_progress' }, { content: 'Test it', status: 'pending' }] }],
      storage: 'session',
    })
    expect(source).toEqual({
      op: 'init',
      items: [
        { rowKey: '0:Write code', content: 'Write code', status: 'in_progress', activeForm: 'Write code', description: 'Build' },
        { rowKey: '1:Test it', content: 'Test it', status: 'pending', activeForm: '', description: 'Build' },
      ],
    })
  })

  it('maps every omp status and states a blocker', () => {
    const source = ohMyPiTodoSource({ op: 'view' }, {
      phases: [
        { name: 'Plan', tasks: [{ content: 'Read', status: 'completed' }, { content: 'Old', status: 'abandoned' }] },
        { name: '', tasks: [{ content: 'Deploy', status: 'blocked', blocker: 'no key' }, { content: 'Odd', status: 'hibernating' }] },
      ],
    })
    expect(source?.items.map(item => [item.content, item.status, item.description])).toEqual([
      ['Read', 'completed', 'Plan'],
      ['Old', 'deleted', 'Plan'],
      ['Deploy', 'pending', 'Blocked: no key'],
      ['Odd', 'pending', undefined],
    ])
  })

  it('skips a blank task and a malformed phase', () => {
    const source = ohMyPiTodoSource({}, { phases: [{ name: 'A', tasks: [{ content: '  ' }, 'x', { content: 'Keep' }] }, 'bad', { name: 'B' }] })
    expect(source?.items.map(item => item.content)).toEqual(['Keep'])
    expect(source?.op).toBe('')
  })

  it('keys two tasks with the same text apart', () => {
    // omp matches a task by its text, and two phases can hold the same words.
    const source = ohMyPiTodoSource({ op: 'view' }, { phases: [{ name: 'A', tasks: [{ content: 'Test it', status: 'completed' }] }, { name: 'B', tasks: [{ content: 'Test it', status: 'pending' }] }] })
    const keys = source?.items.map(item => item.rowKey) ?? []
    expect(keys).toHaveLength(2)
    expect(new Set(keys).size).toBe(2)
  })

  it('trims the text of a task and of its blocker, and states no blocker that is blank', () => {
    const source = ohMyPiTodoSource({ op: 'view' }, { phases: [{ name: 'P', tasks: [{ content: '  Deploy  ', status: 'blocked', blocker: '   ' }] }] })
    expect(source?.items).toEqual([{ rowKey: '0:Deploy', content: 'Deploy', status: 'pending', activeForm: '', description: 'P' }])
  })

  it('reads a phase whose tasks are not a list as no tasks, and a phases field that is not a list as no snapshot', () => {
    expect(ohMyPiTodoSource({}, { phases: [{ name: 'A', tasks: 'Write code' }] })).toEqual({ op: '', items: [] })
    expect(ohMyPiTodoSource({}, { phases: { name: 'A' } })).toBeNull()
  })

  it('reads an empty list as a snapshot that clears the panel', () => {
    expect(ohMyPiTodoSource({ op: 'clear' }, { phases: [] })).toEqual({ op: 'clear', items: [] })
  })

  it('answers null for a result that states no phases', () => {
    expect(ohMyPiTodoSource({}, {})).toBeNull()
    expect(ohMyPiTodoSource({}, undefined)).toBeNull()
  })
})
