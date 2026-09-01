import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT } from '~/generated/contracts/zcode-protocol'
import { zcodeAssistantText, zcodeIsBackgroundTask, zcodeIsModelResponse } from './messageContent'

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

describe('zcodeIsModelResponse', () => {
  it('requires both a string content field and a stop reason', () => {
    expect(zcodeIsModelResponse({ content: 'hi', stopReason: 'stop' })).toBe(true)
  })

  it('rejects telemetry that has content but no stop reason', () => {
    expect(zcodeIsModelResponse({ content: 'not a finished generation' })).toBe(false)
  })

  it('rejects an empty stop reason and a non-string content', () => {
    expect(zcodeIsModelResponse({ content: 'hi', stopReason: '' })).toBe(false)
    expect(zcodeIsModelResponse({ content: 12, stopReason: 'stop' })).toBe(false)
  })
})

describe('zcodeIsBackgroundTask', () => {
  it('recognizes a payload that names a task', () => {
    expect(zcodeIsBackgroundTask({ taskId: 'task-1' })).toBe(true)
  })

  it('rejects an empty or missing task id', () => {
    expect(zcodeIsBackgroundTask({ taskId: '' })).toBe(false)
    expect(zcodeIsBackgroundTask({ content: 'hi', stopReason: 'stop' })).toBe(false)
  })
})
