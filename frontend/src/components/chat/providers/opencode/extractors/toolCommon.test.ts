import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { openCodeToolRecord, openCodeToolSpanRole, openCodeToolText, sameOpenCodeTool } from './toolCommon'

describe('openCodeToolRecord', () => {
  it('reads resolved live calls while preserving zero and false arguments', () => {
    const record = openCodeToolRecord({
      type: 'session.tool.called',
      data: {
        sessionID: 'ses_1',
        assistantMessageID: 'msg_1',
        id: '',
        name: 'shell',
        input: { timeout: 0, background: false },
      },
    })
    expect(record?.callId).toBe('')
    expect(record?.input).toEqual({ timeout: 0, background: false })
    expect(record?.wireForm).toBe('event')
  })

  it('distinguishes recovered opening and closing rows through Worker completion', () => {
    const record = openCodeToolRecord({
      type: 'tool',
      id: 'call_1',
      name: 'read',
      state: {
        status: 'completed',
        input: {},
        content: [
          { type: 'text', text: 'saved' },
        ],
      },
    })
    expect(record).not.toBeNull()
    if (!record)
      throw new Error('The native projected tool must decode.')
    expect(openCodeToolSpanRole(record)).toBe('request')
    expect(openCodeToolSpanRole(record, MessageCompletion.COMPLETE)).toBe('result')
    expect(openCodeToolText(record)).toBe('saved')
  })

  it('keeps retained running output on the result side', () => {
    const record = openCodeToolRecord({
      type: 'tool',
      id: 'call_1',
      name: 'shell',
      state: {
        status: 'running',
        input: {},
        metadata: { output: 'partial' },
      },
    })
    if (!record)
      throw new Error('The native running tool must decode.')
    expect(openCodeToolSpanRole(record, MessageCompletion.INTERRUPTED)).toBe('result')
  })

  it('rejects incomplete call identities and non-object arguments', () => {
    for (const value of [
      null,
      {},
      {
        type: 'tool',
        id: 'c',
        name: 'read',
        state: { status: 'running', input: false },
      },
      {
        type: 'session.tool.called',
        data: { id: 'c', name: 'read', input: {} },
      },
    ])
      expect(openCodeToolRecord(value)).toBeNull()
  })

  it('does not pair live calls from another message or session', () => {
    const make = (messageId: string, sessionId: string) => openCodeToolRecord({
      type: 'session.tool.called',
      data: {
        sessionID: sessionId,
        assistantMessageID: messageId,
        id: 'same-call',
        name: 'read',
        input: {},
      },
    })
    const own = make('msg_1', 'ses_1')
    const otherMessage = make('msg_2', 'ses_1')
    const otherSession = make('msg_1', 'ses_2')
    if (!own || !otherMessage || !otherSession)
      throw new Error('The native call fixtures must decode.')
    expect(sameOpenCodeTool(own, otherMessage)).toBe(false)
    expect(sameOpenCodeTool(own, otherSession)).toBe(false)
  })
})
