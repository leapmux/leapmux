import { describe, expect, it } from 'vitest'
import { kimiApprovalRequest, kimiQuestionRequest, kimiToolResult, kimiToolStart } from './kimiFixtures'

describe('kimiFixtures', () => {
  it('keeps the session ID outside native tool payloads', () => {
    const opening = kimiToolStart('call_1', 'Bash', { command: 'printf 42' })
    const result = kimiToolResult('call_1', '42', { isError: false })
    expect(opening).toEqual({
      type: 'tool.call.started',
      agentId: 'main',
      turnId: 0,
      toolCallId: 'call_1',
      name: 'Bash',
      args: { command: 'printf 42' },
    })
    expect(result).toEqual({
      type: 'tool.result',
      agentId: 'main',
      turnId: 0,
      toolCallId: 'call_1',
      output: '42',
      isError: false,
    })
    expect(result).not.toHaveProperty('name')
    expect(result).not.toHaveProperty('truncated')
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
