import { describe, expect, it } from 'vitest'
import { kimiApprovalRequest, kimiConversationToolRecord, kimiQuestionRequest } from './kimiFixtures'

describe('kimiFixtures', () => {
  it('keeps native tool identity and complete input in both lifecycle states', () => {
    const call = {
      item_id: 'call_1',
      name: 'Bash',
      input: { command: 'printf 42' },
    }
    const opening = kimiConversationToolRecord('request', call)
    const result = kimiConversationToolRecord('result', {
      ...call,
      status: 'ended',
      outcome: 'completed',
      output: '42',
    })
    for (const [row, role, status] of [
      [opening, 'request', 'running'],
      [result, 'result', 'ended'],
    ] as const) {
      expect(row).toMatchObject({
        type: 'leapmux.kimi.item',
        session_id: 'session_1',
        agent_id: 'main',
        turn_id: 't0',
        role,
        item: { ...call, type: 'tool_call', status },
      })
    }
    expect(result.item).toHaveProperty('output', '42')
  })

  it('keeps native interaction identity separate from the nested control request', () => {
    const approval = kimiApprovalRequest('Bash', { command: 'printf 42' })
    const question = kimiQuestionRequest([
      { id: 'question_1', prompt: 'Choose a color.' },
    ])
    expect(approval.session_id).toBe('session_1')
    expect(question.session_id).toBe('session_1')
    expect(approval.type).toBe('interaction')
    expect(question.kind).toBe('question')
    expect(approval.request.tool_name).toBe('Bash')
    expect(question.request.questions).toEqual([
      { id: 'question_1', prompt: 'Choose a color.' },
    ])
    expect(approval).not.toHaveProperty('sessionId')
    expect(question).not.toHaveProperty('sessionId')
  })
})
