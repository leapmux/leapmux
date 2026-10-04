import { describe, expect, it } from 'vitest'
import { KIMI_EVENT } from '~/generated/contracts/kimi-protocol'
import { kimiFrame } from '~/test-support/kimiFixtures'
import { kimiResultDivider } from './resultDivider'

function end(fields: Record<string, unknown>) {
  return kimiResultDivider(kimiFrame(KIMI_EVENT.TurnEnded, { turnId: 0, ...fields }))
}

describe('kimiResultDivider', () => {
  it('words each end reason', () => {
    expect(end({ reason: 'completed', durationMs: 2100 })).toStrictEqual({ label: 'Turn ended (2.1s)' })
    expect(end({ reason: 'cancelled' })).toStrictEqual({ label: 'Turn interrupted' })
    expect(end({ reason: 'blocked' })).toStrictEqual({ label: 'Turn failed — blocked by a hook or a policy', isError: true })
    expect(end({ reason: 'failed', error: { code: 'provider.rate_limited', message: 'Slow down.' } }))
      .toStrictEqual({ label: 'Turn failed (provider.rate_limited) — Slow down.', isError: true })
  })

  it('words each end reason with no duration', () => {
    expect(end({ reason: 'completed' })).toStrictEqual({ label: 'Turn ended' })
    expect(end({ reason: 'cancelled', durationMs: 2100 })).toStrictEqual({ label: 'Turn interrupted (2.1s)' })
    expect(end({ reason: 'blocked', durationMs: 2100 })).toStrictEqual({ label: 'Turn failed (2.1s) — blocked by a hook or a policy', isError: true })
  })

  it('reads a failure that states no error', () => {
    expect(end({ reason: 'failed' })).toStrictEqual({ label: 'Turn failed', isError: true })
    expect(end({ reason: 'failed', error: { message: 'Broke.' } })).toStrictEqual({ label: 'Turn failed — Broke.', isError: true })
  })

  it('reads a reason this build does not know as a failure', () => {
    expect(end({ reason: 'paused' })).toStrictEqual({ label: 'Turn failed', isError: true })
    expect(end({})).toStrictEqual({ label: 'Turn failed', isError: true })
    expect(end({ reason: 'paused', error: { code: 'x.y', message: 'Odd.' } })).toStrictEqual({ label: 'Turn failed (x.y) — Odd.', isError: true })
  })

  it('reads no divider from another row', () => {
    expect(kimiResultDivider(kimiFrame(KIMI_EVENT.TurnStarted))).toBeNull()
    expect(kimiResultDivider(null)).toBeNull()
  })
})
