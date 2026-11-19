import { describe, expect, it } from 'vitest'
import { diracTaskletIdFromRecords } from './taskletIdentity'

describe('diracTaskletIdFromRecords', () => {
  const record = {
    type: 'leapmux.dirac.tool',
    session_id: 'child',
    parent_session_id: 'parent',
    tasklet_id: 'Witten',
  }

  it('keeps the exact native identity across repeated owned records', () => {
    expect(diracTaskletIdFromRecords([
      null,
      { content: 'The assignment' },
      record,
      { ...record, type: 'leapmux.dirac.message' },
    ], 'parent', 'child')).toBe('Witten')
  })

  it.each([
    { records: [], error: 'exactly one tasklet' },
    {
      records: [null],
      error: 'exactly one tasklet',
    },
    {
      records: [
        { ...record, session_id: 'another-child' },
      ],
      error: 'another conversation',
    },
    {
      records: [
        { ...record, parent_session_id: 'another-parent' },
      ],
      error: 'another conversation',
    },
    {
      records: [
        { ...record, tasklet_id: '' },
      ],
      error: 'another conversation',
    },
    {
      records: [
        { ...record, tasklet_id: ' ' },
      ],
      error: 'another conversation',
    },
    {
      records: [
        record,
        { ...record, tasklet_id: 'Bea' },
      ],
      error: 'exactly one tasklet',
    },
  ])('refuses absent, foreign or ambiguous records: %j', ({ records, error }) => {
    expect(() => diracTaskletIdFromRecords(records, 'parent', 'child')).toThrow(error)
  })

  it.each([
    ['', 'child'],
    ['parent', ''],
    ['same', 'same'],
  ])('refuses an invalid parent/child scope: %j', (parent, child) => {
    expect(() => diracTaskletIdFromRecords([record], parent, child)).toThrow('distinct parent and child sessions')
  })
})
