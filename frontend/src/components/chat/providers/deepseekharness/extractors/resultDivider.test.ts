import { describe, expect, it } from 'vitest'
import { deepseekHarnessResultDivider } from './resultDivider'

describe('deepseekHarnessResultDivider', () => {
  it.each(['user', 'parent', 'disposed', 'legacy'])('shows a native aborted turn as interrupted for %s', (kind) => {
    expect(deepseekHarnessResultDivider({ type: 'turn/end', data: { turn: 1, reason: { kind: 'aborted', reason: { kind } } } })).toMatchObject({ label: expect.stringContaining('Turn interrupted') })
  })

  it('shows the message from the native structured error', () => {
    expect(deepseekHarnessResultDivider({ type: 'turn/end', data: { turn: 1, reason: { kind: 'error', error: { message: 'The exact native failure.', code: 'HTTP_400', status: 400 } } } })).toMatchObject({ isError: true, label: expect.stringContaining('The exact native failure.') })
  })
})
