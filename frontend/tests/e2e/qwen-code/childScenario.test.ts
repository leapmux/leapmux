import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { QWEN_HELD_CHILD_TURN, qwenChildTurn } from './childScenario'

const TASK = 'Reply with the single word PONG.'

/** The prompt that the spawn call gives the child: the task, then the marker of the test's script. */
const childPrompt = mockScenarioPrompt('qwen-child-turn', TASK)

/** The startup reminders that Qwen Code sends as a user message of their own, before the task. */
const startupReminders = { role: 'user', content: '<system-reminder>\nThe project holds no context file.\n</system-reminder>' }

function matches(matcher: ReturnType<typeof qwenChildTurn>, messages: unknown[]): boolean {
  const body = { messages }
  return matchesRequest(matcher, { protocol: 'openai-chat-completions', systemText: '', userText: lastUserText(body), body })
}

describe('qwenChildTurn', () => {
  it('answers the first turn of the child, whose last user message is the spawn prompt', () => {
    expect(matches(qwenChildTurn(TASK), [{ role: 'system', content: 'You are a subagent.' }, startupReminders, { role: 'user', content: childPrompt }])).toBe(true)
  })

  it('answers a later turn of the child, which keeps the spawn prompt as its last user message', () => {
    const toolTurn = [
      { role: 'assistant', tool_calls: [{ id: 'read', type: 'function', function: { name: 'read_file', arguments: '{"path":"/note.txt"}' } }] },
      { role: 'tool', tool_call_id: 'read', content: 'The note.' },
    ]
    expect(matches(qwenChildTurn(TASK), [startupReminders, { role: 'user', content: childPrompt }, ...toolTurn])).toBe(true)
  })

  it('does not answer a turn that quotes the task after other text', () => {
    expect(matches(qwenChildTurn(TASK), [{ role: 'user', content: `Generate a short title for this task:\n${childPrompt}` }])).toBe(false)
    expect(matches(qwenChildTurn(TASK), [{ role: 'user', content: JSON.stringify({ description: 'Ask for one word', prompt: childPrompt }) }])).toBe(false)
  })

  it('does not answer the parent turn, whose last user message is the parent prompt', () => {
    const spawn = { role: 'assistant', tool_calls: [{ id: 'spawn', type: 'function', function: { name: 'agent', arguments: JSON.stringify({ prompt: childPrompt }) } }] }
    const parent = [{ role: 'user', content: mockScenarioPrompt('qwen-child-turn', 'Delegate one word to a subagent.') }, spawn, { role: 'tool', tool_call_id: 'spawn', content: 'PONG' }]
    expect(matches(qwenChildTurn(TASK), parent)).toBe(false)
  })

  it('reads the punctuation of the task literally', () => {
    expect(matches(qwenChildTurn(TASK), [{ role: 'user', content: childPrompt.replace('PONG.', 'PONGX') }])).toBe(false)
  })
})

describe('QWEN_HELD_CHILD_TURN', () => {
  it('answers the held child and not a turn that quotes its task', () => {
    const heldPrompt = mockScenarioPrompt('qwen-held-child', `${HELD_CHILD_TASK}.`)
    expect(matches(QWEN_HELD_CHILD_TURN, [{ role: 'user', content: heldPrompt }])).toBe(true)
    expect(matches(QWEN_HELD_CHILD_TURN, [{ role: 'user', content: `Generate a short title for this task:\n${heldPrompt}` }])).toBe(false)
  })
})
