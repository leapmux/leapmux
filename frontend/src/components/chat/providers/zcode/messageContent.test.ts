import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_QUERY_SOURCE, ZCODE_STOP_REASON } from '~/generated/contracts/zcode-protocol'
import { zcodeAssistantText, zcodeIsMainReply } from './messageContent'

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

describe('zcodeIsMainReply', () => {
  it('reads the main turn of a finished generation as the reply', () => {
    expect(zcodeIsMainReply({ content: 'hi', querySource: ZCODE_QUERY_SOURCE.MainTurn, stopReason: 'stop' })).toBe(true)
    expect(zcodeIsMainReply({ content: 'hi', stopReason: 'end_turn' })).toBe(true)
  })

  it('rejects every side query source', () => {
    for (const source of ['subagent', 'compact', 'goal_summary_title', 'target_completion_verification', 'git_commit_message', 'workspace_generate_text']) {
      expect(zcodeIsMainReply({ content: 'hi', querySource: source, stopReason: 'stop' }), source).toBe(false)
    }
  })

  it('rejects the tool-internal stop reason when no source is stated', () => {
    expect(zcodeIsMainReply({ content: '', stopReason: ZCODE_STOP_REASON.ToolInternal })).toBe(false)
  })

  // The source states the role. The stop reason decides only when the payload
  // states no source. A main-turn reply keeps its text beside the tool-internal
  // stop reason.
  it('lets a stated main-turn source override the tool-internal stop reason', () => {
    expect(zcodeIsMainReply({ content: '', querySource: ZCODE_QUERY_SOURCE.MainTurn, stopReason: ZCODE_STOP_REASON.ToolInternal })).toBe(true)
    expect(zcodeIsMainReply({ content: '', querySource: 'subagent', stopReason: 'end_turn' })).toBe(false)
  })

  it('rejects an empty or non-string source', () => {
    expect(zcodeIsMainReply({ content: 'hi', querySource: '', stopReason: 'stop' })).toBe(false)
    expect(zcodeIsMainReply({ content: 'hi', querySource: 5, stopReason: 'stop' })).toBe(false)
    expect(zcodeIsMainReply({ content: 'hi', querySource: null, stopReason: 'stop' })).toBe(false)
  })

  it('requires both a string content field and a stop reason', () => {
    expect(zcodeIsMainReply({ content: 'not a finished generation' })).toBe(false)
    expect(zcodeIsMainReply({ content: 'hi', stopReason: '' })).toBe(false)
    expect(zcodeIsMainReply({ content: 12, stopReason: 'stop' })).toBe(false)
  })
})
