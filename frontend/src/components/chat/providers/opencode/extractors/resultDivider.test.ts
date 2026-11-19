import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { openCodeResultDivider } from './resultDivider'

describe('openCodeResultDivider', () => {
  it('reads live execution and recovered idle endings', () => {
    expect(openCodeResultDivider({ type: 'session.execution.succeeded', data: {} })?.label).toContain('ended')
    expect(openCodeResultDivider({ type: 'idle', outcome: 'succeeded' })?.label).toContain('ended')
    expect(openCodeResultDivider({
      type: 'session.execution.interrupted',
      data: { reason: 'shutdown' },
    })?.label).toContain('interrupted')
  })

  it('keeps native failure details and the Worker interruption override', () => {
    const native = {
      type: 'session.execution.failed',
      data: {
        error: { type: 'api', message: 'model unavailable' },
      },
    }
    expect(openCodeResultDivider(native)).toMatchObject({ isError: true })
    expect(openCodeResultDivider(native)?.label).toContain('model unavailable')
    expect(openCodeResultDivider(native, MessageCompletion.INTERRUPTED)?.label).toContain('interrupted')
  })

  it('rejects unrelated records and unknown projected outcomes', () => {
    expect(openCodeResultDivider(null)).toBeNull()
    expect(openCodeResultDivider({ type: 'compaction' })).toBeNull()
    expect(openCodeResultDivider({ type: 'idle', outcome: 'unknown' })).toBeNull()
  })
})
