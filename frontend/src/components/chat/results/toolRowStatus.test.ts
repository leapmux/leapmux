import { describe, expect, it } from 'vitest'
import { TOOL_ROW_STATUSES, toolRowStatus, toolStatusFor } from './toolRowStatus'

describe('toolRowStatus', () => {
  it('keeps every status the row header reads', () => {
    for (const status of TOOL_ROW_STATUSES)
      expect(toolRowStatus(status)).toBe(status)
  })

  // 'canceled' is the American spelling, and the wire word carries two l's. The
  // header tests the value against the wire word alone, so the typo drew nothing
  // and reported nothing. A closed union makes it a compile error at the producer.
  it('drops a word the row header cannot read', () => {
    expect(toolRowStatus('canceled')).toBe('')
    expect(toolRowStatus('running')).toBe('')
    expect(toolRowStatus(undefined)).toBe('')
  })
})

describe('toolStatusFor', () => {
  it('lets an interruption win over a finished row', () => {
    expect(toolStatusFor('interrupted', false, true)).toBe('cancelled')
  })

  it('reports a failure from the row and from the outcome', () => {
    expect(toolStatusFor(null, true, true)).toBe('failed')
    expect(toolStatusFor('failed', false, true)).toBe('failed')
  })

  it('separates a finished row from a running one', () => {
    expect(toolStatusFor(null, false, true)).toBe('completed')
    expect(toolStatusFor(null, false, false)).toBe('in_progress')
  })
})
