import { describe, expect, it } from 'vitest'
import { matchesRequest } from '../helpers/mockModelScript'
import { kimiChildTurn } from './childScenario'

describe('kimiChildTurn', () => {
  it('matches the task and native child reminder without consuming the parent turn', () => {
    const task = 'Reply with CHILD_LIVE_DONE.'
    const system = 'You are Kimi Code CLI, an interactive general AI agent.'
    const reminder = '<system-reminder>\nYou are running as a subagent: ending your turn is your final hand-off to the parent agent.\n</system-reminder>'
    const request = (messages: Record<string, unknown>[]) => ({
      protocol: 'openai-chat-completions' as const,
      systemText: system,
      userText: String(messages.findLast(message => message.role === 'user')?.content ?? ''),
      body: {
        messages: [
          { role: 'system', content: system },
          ...messages,
        ],
      },
    })
    const child = request([
      { role: 'user', content: task },
      { role: 'user', content: reminder },
    ])
    const parent = request([
      { role: 'user', content: 'Delegate a child.' },
      {
        role: 'assistant',
        tool_calls: [
          {
            function: {
              name: 'Agent',
              arguments: JSON.stringify({ prompt: task }),
            },
          },
        ],
      },
    ])
    expect(matchesRequest(kimiChildTurn('CHILD_LIVE_DONE'), child)).toBe(true)
    const followup = request([
      { role: 'user', content: task },
      { role: 'user', content: reminder },
      { role: 'assistant', content: 'CHILD_LIVE_DONE' },
      { role: 'user', content: 'Now reply with exactly CHILD_FOLLOWUP.' },
    ])
    expect(matchesRequest(kimiChildTurn('CHILD_FOLLOWUP'), followup)).toBe(true)
    expect(matchesRequest(kimiChildTurn('CHILD_LIVE_DONE'), parent)).toBe(false)
    expect(matchesRequest(kimiChildTurn('another task'), child)).toBe(false)
    expect(matchesRequest(kimiChildTurn(['CHILD_LIVE_DONE', 'Reply with']), child)).toBe(true)
    expect(matchesRequest(kimiChildTurn(['CHILD_LIVE_DONE', 'another task']), child)).toBe(false)
    expect(matchesRequest(kimiChildTurn('CHILD_LIVE_DONE'), request([
      { role: 'user', content: `Summarize the task ${task}` },
    ]))).toBe(false)
  })
})
