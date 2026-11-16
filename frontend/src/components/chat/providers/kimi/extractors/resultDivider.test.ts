import { describe, expect, it } from 'vitest'
import { kimiTranscriptTurnFrame } from '~/test-support/kimiFixtures'
import { kimiResultDivider } from './resultDivider'

function end(fields: Record<string, unknown>) {
  return kimiResultDivider(kimiTranscriptTurnFrame(fields))
}

describe('kimiResultDivider', () => {
  it.each([
    ['completed', undefined, 'Turn ended'],
    ['completed', 2100, 'Turn ended (2.1s)'],
    ['cancelled', undefined, 'Turn interrupted'],
    ['cancelled', 2100, 'Turn interrupted (2.1s)'],
  ])('words a native %s turn with duration %s', (state, durationMs, label) => {
    expect(end({ state, durationMs })).toStrictEqual({ label })
  })

  it('reads the native failure message without requiring a duration', () => {
    expect(end({ state: 'failed', error: 'The model refused the request.' })).toStrictEqual({ label: 'Turn failed — The model refused the request.', isError: true })
    expect(end({ state: 'failed', durationMs: 2100, error: 'Blocked by a hook or a policy.' })).toStrictEqual({ label: 'Turn failed (2.1s) — Blocked by a hook or a policy.', isError: true })
  })

  it.each([undefined, '', null])('reads a failed native turn without a nonempty error %s', (error) => {
    expect(end({ state: 'failed', error })).toStrictEqual({ label: 'Turn failed', isError: true })
  })

  it.each([
    'running',
    'queued',
    'paused',
    undefined,
  ])('reads no divider for a non-final or unknown state %s', (state) => {
    expect(end({ state })).toBeNull()
  })

  it('reads no divider from another row', () => {
    expect(kimiResultDivider({ content: 'hello' })).toBeNull()
    expect(kimiResultDivider(null)).toBeNull()
  })
})
