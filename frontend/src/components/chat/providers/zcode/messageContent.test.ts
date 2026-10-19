import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { zcodeAssistantText } from './messageContent'

function sessionUpdated(payload: Record<string, unknown>): Record<string, unknown> {
  return { type: ZCODE_EVENT.SessionUpdated, payload, sessionId: 's-1', seq: 1 }
}

describe('zcodeAssistantText', () => {
  it('reads content from a model-response session.updated', () => {
    expect(zcodeAssistantText(sessionUpdated({ content: 'the answer', stopReason: 'stop' })))
      .toBe('the answer')
  })

  it('returns empty for a tool-only turn whose content is empty', () => {
    expect(zcodeAssistantText(sessionUpdated({ content: '', stopReason: 'tool-calls' }))).toBe('')
  })

  it('returns empty for a row that is not session.updated', () => {
    expect(zcodeAssistantText({ type: ZCODE_EVENT.TurnCompleted, payload: { content: 'no' } })).toBe('')
  })

  it('returns empty when there is no envelope at all', () => {
    expect(zcodeAssistantText(null)).toBe('')
    expect(zcodeAssistantText('not an object')).toBe('')
  })
})
