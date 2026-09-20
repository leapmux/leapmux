import { describe, expect, it } from 'vitest'
import { FINISHED_TOOL_STATUSES, isFinishedToolCallStatus, statusForOutcome, TOOL_CALL_STATUSES, toolCallStatus, toolCallStatusOutcome, UNFINISHED_TOOL_STATUSES } from './toolCallStatus'

describe('toolCallStatus', () => {
  it('keeps every status the row header reads', () => {
    for (const status of TOOL_CALL_STATUSES)
      expect(toolCallStatus(status)).toBe(status)
  })

  // 'canceled' is the American spelling, and the wire word carries two l's. The
  // header tests the value against the wire word alone, so the typo drew nothing
  // and reported nothing. A closed union makes it a compile error at the producer.
  it('drops a word the row header cannot read', () => {
    expect(toolCallStatus('canceled')).toBe('unstated')
    expect(toolCallStatus('running')).toBe('unstated')
    expect(toolCallStatus(undefined)).toBe('unstated')
  })
})

describe('the finished/unfinished split', () => {
  it('names every finished status and no unfinished one', () => {
    for (const status of FINISHED_TOOL_STATUSES)
      expect(isFinishedToolCallStatus(status), status).toBe(true)
    for (const status of UNFINISHED_TOOL_STATUSES)
      expect(isFinishedToolCallStatus(status), status).toBe(false)
    // The two halves are the whole union, stated once, with nothing shared.
    expect([...UNFINISHED_TOOL_STATUSES, ...FINISHED_TOOL_STATUSES].sort()).toEqual([...TOOL_CALL_STATUSES].sort())
  })
})

describe('statusForOutcome', () => {
  it('maps every outcome word that ends a call to its status', () => {
    expect(statusForOutcome('failed')).toBe('failed')
    expect(statusForOutcome('interrupted')).toBe('cancelled')
    expect(statusForOutcome('declined')).toBe('declined')
  })
})

describe('toolCallStatusOutcome', () => {
  it('gives one outcome word for each status that ends a call badly', () => {
    expect(toolCallStatusOutcome('failed')).toBe('failed')
    expect(toolCallStatusOutcome('cancelled')).toBe('interrupted')
    expect(toolCallStatusOutcome('declined')).toBe('declined')
    expect(toolCallStatusOutcome('incomplete')).toBe('incomplete')
  })

  // A row whose call is still open, or finished cleanly, draws no outcome header:
  // the body states the result, and a header above it would state it twice.
  it('states no outcome for a status the row does not have to announce', () => {
    expect(toolCallStatusOutcome('unstated')).toBeNull()
    expect(toolCallStatusOutcome('pending')).toBeNull()
    expect(toolCallStatusOutcome('in_progress')).toBeNull()
    expect(toolCallStatusOutcome('completed')).toBeNull()
  })

  // The union and the table must stay in step: a status added to one and not the
  // other is a row whose header silently never draws.
  it('answers for every status the union declares', () => {
    for (const status of TOOL_CALL_STATUSES)
      expect(() => toolCallStatusOutcome(status)).not.toThrow()
  })
})
