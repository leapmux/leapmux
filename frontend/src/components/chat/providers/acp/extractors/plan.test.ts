import { describe, expect, it } from 'vitest'
import { acpPlanTodos } from './plan'

describe('acpPlanTodos', () => {
  // Null means "this frame carries no plan at all", which the caller cannot draw.
  // An EMPTY array is a cleared list, and the shared checklist header states that in
  // its own words -- so the two must not collapse into one answer.
  it('returns null for a frame that carries no entries array', () => {
    expect(acpPlanTodos(null)).toBeNull()
    expect(acpPlanTodos(undefined)).toBeNull()
    expect(acpPlanTodos('not a list')).toBeNull()
  })

  it('returns an empty list for a cleared plan', () => {
    expect(acpPlanTodos([])).toEqual([])
  })

  it('maps pending/completed/in_progress and defaults to pending', () => {
    expect(acpPlanTodos([
      { content: 'one', status: 'pending' },
      { content: 'two', status: 'completed' },
      { content: 'three', status: 'in_progress' },
      { content: 'four' },
      { content: 'five', status: 'unknown' },
    ])).toEqual([
      { rowKey: '0:one', content: 'one', status: 'pending', activeForm: '' },
      { rowKey: '1:two', content: 'two', status: 'completed', activeForm: '' },
      { rowKey: '2:three', content: 'three', status: 'in_progress', activeForm: '' },
      { rowKey: '3:four', content: 'four', status: 'pending', activeForm: '' },
      { rowKey: '4:five', content: 'five', status: 'pending', activeForm: '' },
    ])
  })

  it('coerces missing content to empty string', () => {
    expect(acpPlanTodos([{ status: 'completed' }])).toEqual([
      { rowKey: '0:', content: '', status: 'completed', activeForm: '' },
    ])
  })

  // An `Array.isArray` says nothing about the elements. An entry that is no object
  // states neither content nor status, and a blank checklist row states nothing to the
  // reader -- so it is dropped, and the surviving entries keep their place in the RAW
  // plan so that `rowKey` stays the same across a re-send.
  it('drops an entry that states no fields and keeps the raw index of the rest', () => {
    expect(acpPlanTodos([null, 'one', { content: 'two' }, 42, ['three'], { content: 'four', status: 'completed' }])).toEqual([
      { rowKey: '2:two', content: 'two', status: 'pending', activeForm: '' },
      { rowKey: '5:four', content: 'four', status: 'completed', activeForm: '' },
    ])
  })

  // A non-string content is no content, and the row would otherwise draw the number.
  it('reads a content that is not a string as none', () => {
    expect(acpPlanTodos([{ content: 42, status: 'in_progress' }])).toEqual([
      { rowKey: '0:', content: '', status: 'in_progress', activeForm: '' },
    ])
  })
})
