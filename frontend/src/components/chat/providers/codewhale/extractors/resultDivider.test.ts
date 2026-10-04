import { describe, expect, it } from 'vitest'
import { CODEWHALE_EVENT, CODEWHALE_TURN_STATUS } from '~/generated/contracts/codewhale-protocol'
import { codewhaleEvent, itemFinished, turnCompleted } from '../toolResults.fixtures'
import { codewhaleResultDivider } from './resultDivider'

describe('codewhaleResultDivider', () => {
  it('states each ending the turn record reports', () => {
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Completed, { duration_ms: 2380 }))).toStrictEqual({ label: 'Turn ended (2.4s)' })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Interrupted))).toStrictEqual({ label: 'Turn interrupted' })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Canceled))).toStrictEqual({ label: 'Turn interrupted' })
  })

  it('states the reason of a failed turn, and its long form as the detail', () => {
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Failed, { error: 'The model returned no output' })))
      .toStrictEqual({ label: 'Turn failed — The model returned no output', isError: true })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Failed, { error: 'HTTP 500\n{"error":"boom"}' })))
      .toStrictEqual({ label: 'Turn failed — HTTP 500', isError: true, detail: '{"error":"boom"}' })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Failed))).toStrictEqual({ label: 'Turn failed', isError: true })
  })

  it('states the duration beside every ending, and no duration the record does not state', () => {
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Completed))).toStrictEqual({ label: 'Turn ended' })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Interrupted, { duration_ms: 1500 }))).toStrictEqual({ label: 'Turn interrupted (1.5s)' })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Failed, { duration_ms: 1500, error: 'boom' }))).toStrictEqual({ label: 'Turn failed (1.5s) — boom', isError: true })
    // A duration in another shape states none, rather than a wrong one.
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Completed, { duration_ms: '1500' }))).toStrictEqual({ label: 'Turn ended' })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Completed, { duration_ms: 0 }))).toStrictEqual({ label: 'Turn ended (0ms)' })
  })

  it('states a failure whose error holds no words, or only a body, without a blank reason', () => {
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Failed, { error: ' \n \n' }))).toStrictEqual({ label: 'Turn failed', isError: true })
    expect(codewhaleResultDivider(turnCompleted(CODEWHALE_TURN_STATUS.Failed, { error: 'HTTP 500\n\n  \n' }))).toStrictEqual({ label: 'Turn failed — HTTP 500', isError: true })
  })

  it('answers null for a status it does not know and for any other row', () => {
    expect(codewhaleResultDivider(turnCompleted('a_later_status'))).toBeNull()
    expect(codewhaleResultDivider(itemFinished('agent_message', 'x'))).toBeNull()
    expect(codewhaleResultDivider({ type: 'result' })).toBeNull()
  })

  it('answers null for a turn end that carries no turn record', () => {
    expect(codewhaleResultDivider(codewhaleEvent(CODEWHALE_EVENT.TurnCompleted, {}))).toBeNull()
    expect(codewhaleResultDivider(codewhaleEvent(CODEWHALE_EVENT.TurnCompleted, { turn: 'completed' }))).toBeNull()
  })
})
