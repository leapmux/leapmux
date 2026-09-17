import { describe, expect, it } from 'vitest'
import { TOOL_ROW_STATUSES, toolRowStatus, toolRowStatusOutcome, toolStatusFor } from './toolRowStatus'

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

describe('toolRowStatusOutcome', () => {
  it('gives one outcome word for each status that ends a call badly', () => {
    expect(toolRowStatusOutcome('failed')).toBe('failed')
    expect(toolRowStatusOutcome('cancelled')).toBe('interrupted')
    expect(toolRowStatusOutcome('declined')).toBe('declined')
  })

  // A row whose call is still open, or finished cleanly, draws no outcome header:
  // the body states the result, and a header above it would state it twice.
  it('states no outcome for a status the row does not have to announce', () => {
    expect(toolRowStatusOutcome('')).toBeNull()
    expect(toolRowStatusOutcome('pending')).toBeNull()
    expect(toolRowStatusOutcome('in_progress')).toBeNull()
    expect(toolRowStatusOutcome('completed')).toBeNull()
  })

  // The union and the table must stay in step: a status added to one and not the
  // other is a row whose header silently never draws.
  it('answers for every status the union declares', () => {
    for (const status of TOOL_ROW_STATUSES)
      expect(() => toolRowStatusOutcome(status)).not.toThrow()
  })
})
