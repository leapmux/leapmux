import { describe, expect, it } from 'vitest'
import { kimiApprovalRequest, kimiQuestionRequest, kimiTranscriptToolFrame } from './kimiFixtures'

describe('kimiFixtures', () => {
  it('keeps native tool identity and complete input in both lifecycle states', () => {
    const opening = kimiTranscriptToolFrame({
      toolCallId: 'call_1',
      name: 'Bash',
      input: { command: 'printf 42' },
    })
    const result = kimiTranscriptToolFrame({
      toolCallId: 'call_1',
      name: 'Bash',
      input: { command: 'printf 42' },
      state: 'done',
      output: '42',
    })
    for (const [row, state] of [
      [opening, 'running'],
      [result, 'done'],
    ] as const) {
      expect(row).toMatchObject({
        type: 'transcript.ops',
        session_id: 'session_1',
        payload: {
          type: 'transcript.ops',
          agent_id: 'main',
          ops: [
            {
              op: 'frame.upsert',
              turnId: 't0',
              stepId: 't0.1',
              frame: {
                kind: 'tool',
                frameId: 't0.1.call_1',
                toolCallId: 'call_1',
                name: 'Bash',
                input: { command: 'printf 42' },
                state,
              },
            },
          ],
        },
      })
    }
    expect(result.payload.ops[0]?.frame).toHaveProperty('output', '42')
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
