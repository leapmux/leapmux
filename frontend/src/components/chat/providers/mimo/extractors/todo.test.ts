import type { MiMoToolPart } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { mimoTaskRequest, mimoTaskResult, mimoTaskStatus } from './todo'

/** One finished `task` call. */
function taskPart(operation: unknown, fields: Partial<MiMoToolPart> = {}): MiMoToolPart {
  return {
    callId: 'call-1',
    tool: MIMO_TOOL.Task,
    status: 'completed',
    input: { operation },
    output: '',
    error: '',
    title: '',
    metadata: {},
    attachments: [],
    ...fields,
  }
}

function item(id: string, content: string, status: string) {
  return { id, rowKey: id, content, status, activeForm: '' }
}

describe('mimoTaskStatus', () => {
  it.each([
    ['in_progress', 'in_progress'],
    ['done', 'completed'],
    ['abandoned', 'deleted'],
    ['open', 'pending'],
    // A blocked item is still work that remains.
    ['blocked', 'pending'],
    ['', 'pending'],
  ])('reads %s as %s', (status, expected) => {
    expect(mimoTaskStatus(status)).toBe(expected)
  })
})

describe('mimoTaskRequest', () => {
  it('states the text a create adds', () => {
    expect(mimoTaskRequest({ operation: { action: 'create', summary: 'Ship it' } }).items.map(entry => entry.content)).toEqual(['Ship it'])
  })

  // MiMo mints the id of a new item when the call answers, so the request states the
  // item without one.
  it('states no id for the item a create adds, even when the operation gives one', () => {
    const [created] = mimoTaskRequest({ operation: { action: 'create', id: 'T9', summary: 'Ship it' } }).items
    expect(created).toMatchObject({ content: 'Ship it', status: 'pending', activeForm: '' })
    expect(created).not.toHaveProperty('id')
  })

  // A create carries no note: `event_summary` belongs to a status change.
  it('states no note for a create or a rename', () => {
    expect(mimoTaskRequest({ operation: { action: 'create', summary: 'Ship it', event_summary: 'why' } })).not.toHaveProperty('note')
    expect(mimoTaskRequest({ operation: { action: 'rename', id: 'T1', summary: 'x', event_summary: 'why' } })).not.toHaveProperty('note')
  })

  it('states the new text a rename gives', () => {
    expect(mimoTaskRequest({ operation: { action: 'rename', id: 'T1', summary: 'Ship it today' } })).toEqual({ items: [item('T1', 'Ship it today', 'pending')] })
  })

  it.each(['start', 'block', 'unblock', 'done', 'abandon'])('states the id that a %s acts on, and its note', (action) => {
    expect(mimoTaskRequest({ operation: { action, id: 'T2', event_summary: 'why' } })).toEqual({ items: [item('T2', 'T2', 'pending')], note: 'why' })
    expect(mimoTaskRequest({ operation: { action, id: 'T2' } })).toEqual({ items: [item('T2', 'T2', 'pending')] })
  })

  it.each([
    ['a change with no id', { action: 'done' }],
    ['a rename with no id', { action: 'rename', summary: 'x' }],
    ['a list', { action: 'list' }],
    ['a get', { action: 'get', id: 'T1' }],
    ['an action from a later release', { action: 'archive', id: 'T1' }],
  ])('states no item for %s', (_name, operation) => {
    expect(mimoTaskRequest({ operation }).items).toEqual([])
  })

  it('states no item for an operation that is not an object', () => {
    expect(mimoTaskRequest({ operation: 'create' })).toEqual({ items: [] })
    expect(mimoTaskRequest({})).toEqual({ items: [] })
  })
})

describe('mimoTaskResult', () => {
  const result = (operation: Record<string, unknown>, fields: Partial<MiMoToolPart>) => {
    const part = taskPart(operation, fields)
    return mimoTaskResult(part, mimoTaskRequest(part.input))
  }

  describe('a list', () => {
    it('reads each item line and skips a line of another shape', () => {
      expect(result({ action: 'list' }, { output: 'Tasks:\nT1 in_progress — Ship it\nT1.2 done — Test it\n\nT3 abandoned — Old idea' })).toEqual({
        items: [item('T1', 'Ship it', 'in_progress'), item('T1.2', 'Test it', 'completed'), item('T3', 'Old idea', 'deleted')],
      })
    })

    it('reads the empty list', () => {
      expect(result({ action: 'list' }, { output: 'No tasks.' })).toEqual({ items: [], emptyText: 'No tasks.' })
      expect(result({ action: 'list' }, { output: '\nNo tasks.\n' })).toEqual({ items: [], emptyText: 'No tasks.' })
    })

    it('reads nothing from an empty answer', () => {
      expect(result({ action: 'list' }, { output: '' })).toBeNull()
    })

    // The line format is `<id> <status> — <text>`. An id that does not open with T,
    // or a hyphen in place of the dash, is a line of another shape.
    it('skips a line whose id or separator breaks the format', () => {
      expect(result({ action: 'list' }, { output: 'X1 open — Nope\nT2 open - Nope\nT3 open — Yes' })).toEqual({ items: [item('T3', 'Yes', 'pending')] })
    })

    it('reads nothing from a list with no item line', () => {
      expect(result({ action: 'list' }, { output: 'something else' })).toBeNull()
    })
  })

  describe('a get', () => {
    it('reads the item', () => {
      expect(result({ action: 'get', id: 'T1' }, { output: JSON.stringify({ id: 'T1', summary: 'Ship it', status: 'done' }) })).toEqual({ items: [item('T1', 'Ship it', 'completed')] })
    })

    it.each([
      ['text that is not JSON', 'No task T1.'],
      ['a record with no id', JSON.stringify({ summary: 'Ship it' })],
      ['a record that is a list', JSON.stringify([{ id: 'T1' }])],
    ])('reads nothing from %s', (_name, output) => {
      expect(result({ action: 'get', id: 'T1' }, { output })).toBeNull()
    })
  })

  describe('a change', () => {
    it('reads the status a rename left, with the new text', () => {
      expect(result({ action: 'rename', id: 'T1', summary: 'Ship it today' }, { metadata: { id: 'T1', status: 'in_progress' } })).toEqual({
        items: [item('T1', 'Ship it today', 'in_progress')],
      })
    })

    it('reads an abandoned item as deleted, with the note', () => {
      expect(result({ action: 'abandon', id: 'T2', event_summary: 'no longer needed' }, { metadata: { id: 'T2', status: 'abandoned' } })).toEqual({
        items: [item('T2', 'T2', 'deleted')],
        note: 'no longer needed',
      })
    })

    it('reads the id that the answer states over the one the request gave', () => {
      expect(result({ action: 'create', summary: 'Ship it' }, { metadata: { id: 'T4', status: 'open' } })).toEqual({ items: [item('T4', 'Ship it', 'pending')] })
    })

    // A create with no text states no words, so the id MiMo minted stands in for them.
    it('states the id as the text of a create that gave none', () => {
      expect(result({ action: 'create' }, { metadata: { id: 'T5', status: 'open' } })).toEqual({ items: [item('T5', 'T5', 'pending')] })
    })

    it('reads a status word of a later release as work that remains', () => {
      expect(result({ action: 'done', id: 'T1' }, { metadata: { id: 'T1', status: 'paused' } })).toEqual({ items: [item('T1', 'T1', 'pending')] })
    })

    it.each([
      ['no id', { status: 'done' }],
      ['no status', { id: 'T1' }],
    ])('reads nothing from an answer with %s', (_name, metadata) => {
      expect(result({ action: 'done', id: 'T1' }, { metadata })).toBeNull()
    })

    it('reads nothing when the request states no item', () => {
      expect(result({ action: 'done' }, { metadata: { id: 'T1', status: 'done' } })).toBeNull()
    })
  })
})
