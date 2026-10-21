import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { piResultDivider } from './resultDivider'

describe('piResultDivider', () => {
  it('reads a cancelled retry divider as interrupted', () => {
    expect(piResultDivider({ type: 'agent_end', willRetry: true, duration_ms: 2000 }, MessageCompletion.INTERRUPTED))
      .toEqual({ label: 'Turn interrupted (2.0s)' })
  })

  it('does not label a stored divider as a retry attempt', () => {
    expect(piResultDivider({ type: 'agent_end', willRetry: true, messages: [] }))
      .toEqual({ label: 'Turn ended' })
  })
})
