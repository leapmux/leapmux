import { describe, expect, it } from 'vitest'
import { ANTIGRAVITY_FRAME } from '~/generated/contracts/antigravity-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { antigravityResultDivider } from './resultDivider'

describe('antigravityResultDivider', () => {
  const turnEnd = { name: ANTIGRAVITY_FRAME.TurnEnd }

  it('labels completed, interrupted and failed turns', () => {
    expect(antigravityResultDivider(turnEnd, MessageCompletion.COMPLETE)).toEqual({ label: 'Turn ended' })
    expect(antigravityResultDivider(turnEnd, MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted' })
    expect(antigravityResultDivider(turnEnd, MessageCompletion.ERROR)).toEqual({ label: 'Turn failed', isError: true })
  })

  it('ignores an unrelated native frame', () => {
    expect(antigravityResultDivider({ name: ANTIGRAVITY_FRAME.Step }, MessageCompletion.COMPLETE)).toBeNull()
    expect(antigravityResultDivider(null, MessageCompletion.COMPLETE)).toBeNull()
  })
})
