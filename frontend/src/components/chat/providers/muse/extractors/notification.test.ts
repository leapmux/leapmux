import { describe, expect, it } from 'vitest'
import { museCompactionBoundary, museNotificationEntry } from './notification'

function nativeTodoUpdate(items: unknown): Record<string, unknown> {
  return { method: 'session/todoListChanged', params: { sessionId: 'native-session', items } }
}

describe('museNotificationEntry', () => {
  it.each(['pending', 'inProgress', 'completed', 'cancelled'])('keeps a recognized native %s list update on the sidebar', (status) => {
    const frame = nativeTodoUpdate([{ text: 'Native task', status }])
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([])
    expect(frame).toEqual(original)
  })

  it('keeps an actual empty native list update hidden', () => {
    expect(museNotificationEntry(nativeTodoUpdate([]))).toEqual([])
  })

  it.each(['futureStatus', 'in_progress', 'completed ', ' Native status 文 '])('shows the exact unknown native status without a guessed task state: %j', (status) => {
    const frame = nativeTodoUpdate([{ text: 'Native task', status }])
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text: `Unknown Muse to-do status: ${status}` }])
    expect(frame).toEqual(original)
  })

  it('reports each unknown status without exposing a partial native list', () => {
    expect(museNotificationEntry(nativeTodoUpdate([
      { text: 'Recognized task', status: 'pending' },
      { text: 'Unknown task', status: 'futureOne' },
      { text: 'Another unknown task', status: 'futureTwo' },
    ]))).toEqual([
      { kind: 'text', text: 'Unknown Muse to-do status: futureOne' },
      { kind: 'text', text: 'Unknown Muse to-do status: futureTwo' },
    ])
  })

  it.each(singleArgumentCases([null, 0, '', {}, [null], [{}], [{ text: 'Native task', status: null }]]))('keeps malformed native list values out of status notifications: %j', (items) => {
    expect(museNotificationEntry(nativeTodoUpdate(items))).toEqual([])
  })
})

function nativeQuestionSettlement(outcome: unknown): Record<string, unknown> {
  return { method: 'userInput/settled', params: { sessionId: 'native-session', userInputId: 'native-input', outcome } }
}

describe('museNotificationEntry native outcomes', () => {
  it.each([
    ['answered', 'Muse question answered.'],
    ['cancelled', 'Muse question cancelled.'],
    ['interrupted', 'Muse question interrupted.'],
    ['clarified', 'Muse question clarified.'],
    ['timedOut', 'Muse question timed out.'],
    ['aborted', 'Muse question aborted.'],
  ])('reads native question outcome %s', (outcome, text) => {
    const frame = nativeQuestionSettlement(outcome)
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text }])
    expect(frame).toEqual(original)
  })

  it.each(['futureOutcome', ' Native outcome 文 '])('preserves a future native settlement word: %j', (outcome) => {
    const frame = nativeQuestionSettlement(outcome)
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text: `Muse question settled: ${outcome}` }])
    expect(frame).toEqual(original)
  })

  it.each(singleArgumentCases([undefined, null, false, 0, [], {}, '', ' \t\n']))('refuses an unreadable native settlement outcome: %j', (outcome) => {
    const frame = nativeQuestionSettlement(outcome)
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([])
    expect(frame).toEqual(original)
  })

  it.each(['sessionId', 'userInputId'])('requires a readable native %s before displaying settlement', (field) => {
    for (const value of [undefined, null, false, 0, [], {}, '', ' \t\n']) {
      const frame = { method: 'userInput/settled', params: { sessionId: 'native-session', userInputId: 'native-input', outcome: 'answered', [field]: value } }
      const original = structuredClone(frame)
      expect(museNotificationEntry(frame)).toEqual([])
      expect(frame).toEqual(original)
    }
  })

  it.each([
    ['noop', 'Muse compaction did not change the context.'],
    ['failed', 'Muse compaction failed.'],
    ['cancelled', 'Muse compaction cancelled.'],
  ])('reads native compaction outcome %s when no reason exists', (outcome, text) => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome } } }
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text }])
    expect(frame).toEqual(original)
  })

  it.each(['noop', 'failed', 'cancelled', 'futureOutcome'])('preserves the supplied native compaction reason for %s', (outcome) => {
    const reason = ' Native exact reason\n文 '
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome, reason } } }
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text: reason }])
    expect(frame).toEqual(original)
  })

  it('preserves the existing compacted trigger', () => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome: 'compacted', trigger: 'native-trigger', tokensBefore: 23, tokensAfter: 7 } } }
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'native-trigger' } }])
    expect(frame).toEqual(original)
  })

  it('preserves an unknown native compaction outcome as native text', () => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome: 'futureOutcome' } } }
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text: 'Muse compaction: futureOutcome' }])
    expect(frame).toEqual(original)
  })
})

describe('museNotificationEntry unreadable values', () => {
  it('keeps a native to-do status that contains whitespace only out of notifications', () => {
    const frame = nativeTodoUpdate([{ text: 'Native task', status: ' \t\n' }])
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([])
    expect(frame).toEqual(original)
  })

  it.each(singleArgumentCases([undefined, null, false, 0, [], {}, '', ' \t\n']))('refuses an unreadable native compaction outcome: %j', (outcome) => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome } } }
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([])
    expect(frame).toEqual(original)
  })
})

describe('museCompactionBoundary', () => {
  it.each([Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])('omits a token count that is not a safe nonnegative integer: %s', (count) => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome: 'compacted', trigger: 'manual', tokensBefore: count, tokensAfter: count } } }
    const original = structuredClone(frame)
    expect(museCompactionBoundary({ rawText: '', topLevel: frame, parentObject: frame, wrapper: null })).toEqual({ trigger: 'manual' })
    expect(frame).toEqual(original)
  })

  it.each([0, Number.MAX_SAFE_INTEGER])('preserves the safe native token count %s', (count) => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome: 'compacted', trigger: 'manual', tokensBefore: count, tokensAfter: count } } }
    const original = structuredClone(frame)
    expect(museCompactionBoundary({ rawText: '', topLevel: frame, parentObject: frame, wrapper: null })).toEqual({ trigger: 'manual', pre: count, post: count })
    expect(frame).toEqual(original)
  })
})

describe('museNotificationEntry native reason precedence', () => {
  it.each(singleArgumentCases([undefined, null, false, 0, [], {}, '', ' \t\n']))('keeps a readable native reason without a readable outcome: %j', (outcome) => {
    const reason = ' Native exact reason\n文 '
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome, reason } } }
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text: reason }])
    expect(frame).toEqual(original)
  })

  it.each([
    ['noop', 'Muse compaction did not change the context.'],
    ['failed', 'Muse compaction failed.'],
    ['cancelled', 'Muse compaction cancelled.'],
  ])('uses the known %s outcome when the native reason contains whitespace only', (outcome, text) => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome, reason: ' \t\n' } } }
    const original = structuredClone(frame)
    expect(museNotificationEntry(frame)).toEqual([{ kind: 'text', text }])
    expect(frame).toEqual(original)
  })
})

function singleArgumentCases(values: readonly unknown[]): [unknown][] {
  return values.map<[unknown]>(value => [value])
}
