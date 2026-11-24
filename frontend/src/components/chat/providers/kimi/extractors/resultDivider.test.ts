import { describe, expect, it } from 'vitest'
import { kimiConversationTurnRecord, kimiTranscriptTurnFrame } from '~/test-support/kimiFixtures'
import { kimiResultDivider } from './resultDivider'

function end(fields: Record<string, unknown>) {
  return kimiResultDivider(kimiConversationTurnRecord(fields))
}

describe('kimiResultDivider', () => {
  it.each([
    ['completed', undefined, 'Turn ended'],
    ['completed', 2100, 'Turn ended (2.1s)'],
    ['interrupted', undefined, 'Turn interrupted'],
    ['interrupted', 2100, 'Turn interrupted (2.1s)'],
  ])('words a native %s turn with duration %s', (outcome, durationMs, label) => {
    expect(end({ status: 'ended', outcome, duration_ms: durationMs })).toStrictEqual({ label })
  })

  it('reads the native failure message without requiring a duration', () => {
    expect(end({
      status: 'ended',
      outcome: 'failed',
      error: { code: 'native_failure', message: 'The model refused the request.' },
    })).toStrictEqual({ label: 'Turn failed — The model refused the request.', isError: true })
    expect(end({
      status: 'ended',
      outcome: 'failed',
      duration_ms: 2100,
      error: { code: 'native_failure', message: 'Blocked by a hook or a policy.' },
    })).toStrictEqual({ label: 'Turn failed (2.1s) — Blocked by a hook or a policy.', isError: true })
  })

  it.each([undefined, '', '  '])('reads a failed native turn without a nonempty error %s', (reason) => {
    const error = reason === undefined
      ? undefined
      : { code: 'native_failure', message: reason }
    expect(end({ status: 'ended', outcome: 'failed', error })).toStrictEqual({ label: 'Turn failed', isError: true })
  })

  it.each([
    'running',
    'queued',
    'paused',
    undefined,
  ])('reads no divider for a non-final or unknown status %s', (status) => {
    expect(end({ status })).toBeNull()
  })

  it('ignores obsolete transcript completion frames', () => {
    expect(kimiResultDivider(kimiTranscriptTurnFrame({ state: 'completed' }))).toBeNull()
  })

  it('reads no divider from another row', () => {
    expect(kimiResultDivider({ content: 'hello' })).toBeNull()
    expect(kimiResultDivider(null)).toBeNull()
  })
})
