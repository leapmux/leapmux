import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { lastUserText, matchesRequest } from '../helpers/mockModelScript'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { QWEN_HELD_CHILD_TURN, qwenChildTurn } from './childScenario'

const TASK = 'Reply with the single word PONG.'

/** The prompt that the spawn call gives the child: the task, then the marker of the test's script. */
const childPrompt = mockScenarioPrompt('qwen-child-turn', TASK)

/** One startup reminder of Qwen Code, as a text part. */
function reminder(body: string): { type: 'text', text: string } {
  return { type: 'text', text: `<system-reminder>\n${body}\n</system-reminder>` }
}

/** The startup reminders that Qwen Code 0.24.7 sends before the task of a child: the skills, then the environment. */
const startupReminders = [
  reminder('The following skills are available for use with the Skill tool.\n\n<available_skills>\n</available_skills>'),
  reminder('This is the Qwen Code. We are setting up the context for our chat.\nMy operating system is: darwin'),
]

/**
 * The user message of a Qwen Code turn, in the shape that Qwen Code 0.24.7 sends. The startup reminders and the
 * prompt are text parts of ONE user message, because Qwen merges its user entries before the request.
 */
function userMessage(prompt: string, reminders = startupReminders): { role: 'user', content: { type: 'text', text: string }[] } {
  return { role: 'user', content: [...reminders, { type: 'text', text: prompt }] }
}

const childSystem = { role: 'system', content: 'You are a general-purpose subagent working for a parent agent.' }

function matches(matcher: ReturnType<typeof qwenChildTurn>, messages: unknown[]): boolean {
  const body = { messages }
  return matchesRequest(matcher, { protocol: 'openai-chat-completions', systemText: '', userText: lastUserText('openai-chat-completions', body), body })
}

describe('qwenChildTurn', () => {
  it('answers the first turn of the child, whose user message holds the startup reminders and then the spawn prompt', () => {
    expect(matches(qwenChildTurn(TASK), [childSystem, userMessage(childPrompt)])).toBe(true)
  })

  it('answers a later turn of the child, which keeps that user message as its last one', () => {
    const toolTurn = [
      { role: 'assistant', tool_calls: [{ id: 'read', type: 'function', function: { name: 'read_file', arguments: '{"path":"/note.txt"}' } }] },
      { role: 'tool', tool_call_id: 'read', content: 'The note.' },
    ]
    expect(matches(qwenChildTurn(TASK), [childSystem, userMessage(childPrompt), ...toolTurn])).toBe(true)
  })

  it('answers a child turn that holds no startup reminder', () => {
    expect(matches(qwenChildTurn(TASK), [childSystem, userMessage(childPrompt, [])])).toBe(true)
    expect(matches(qwenChildTurn(TASK), [childSystem, { role: 'user', content: childPrompt }])).toBe(true)
  })

  it('answers a child turn with one startup reminder', () => {
    expect(matches(qwenChildTurn(TASK), [childSystem, userMessage(childPrompt, startupReminders.slice(1))])).toBe(true)
  })

  it('does not answer a turn that quotes the task after other text', () => {
    expect(matches(qwenChildTurn(TASK), [{ role: 'user', content: `Generate a short title for this task:\n${childPrompt}` }])).toBe(false)
    expect(matches(qwenChildTurn(TASK), [userMessage(`Generate a short title for this task:\n${childPrompt}`)])).toBe(false)
    expect(matches(qwenChildTurn(TASK), [userMessage(JSON.stringify({ description: 'Ask for one word', prompt: childPrompt }))])).toBe(false)
  })

  it('does not answer a turn whose text between the reminders and the task is not a reminder', () => {
    const [skills, environment] = startupReminders
    const other = { type: 'text', text: 'The parent quotes the task.' }
    expect(matches(qwenChildTurn(TASK), [{ role: 'user', content: [skills, other, environment, { type: 'text', text: childPrompt }] }])).toBe(false)
    expect(matches(qwenChildTurn(TASK), [{ role: 'user', content: [skills, environment, other, { type: 'text', text: childPrompt }] }])).toBe(false)
  })

  it('does not answer a turn that quotes the task inside a reminder', () => {
    const parentPrompt = mockScenarioPrompt('qwen-child-turn', 'Delegate one word to a subagent.')
    expect(matches(qwenChildTurn(TASK), [userMessage(parentPrompt, [...startupReminders, reminder(childPrompt)])])).toBe(false)
    expect(matches(qwenChildTurn(TASK), [userMessage(childPrompt, [{ type: 'text', text: '<system-reminder>\nA reminder that never closes.' }])])).toBe(false)
  })

  it('does not answer the parent turn, whose last user message holds the parent prompt', () => {
    const spawn = { role: 'assistant', tool_calls: [{ id: 'spawn', type: 'function', function: { name: 'agent', arguments: JSON.stringify({ prompt: childPrompt }) } }] }
    const parent = [userMessage(mockScenarioPrompt('qwen-child-turn', 'Delegate one word to a subagent.')), spawn, { role: 'tool', tool_call_id: 'spawn', content: 'PONG' }]
    expect(matches(qwenChildTurn(TASK), parent)).toBe(false)
  })

  it('reads the punctuation of the task literally', () => {
    expect(matches(qwenChildTurn(TASK), [userMessage(childPrompt.replace('PONG.', 'PONGX'))])).toBe(false)
  })

  it.each(['', '  '])('refuses an empty task: %j', (task) => {
    expect(() => qwenChildTurn(task)).toThrow('task that is not empty')
  })
})

describe('QWEN_HELD_CHILD_TURN', () => {
  it('answers the held child and not a turn that quotes its task', () => {
    const heldPrompt = mockScenarioPrompt('qwen-held-child', `${HELD_CHILD_TASK}.`)
    expect(matches(QWEN_HELD_CHILD_TURN, [childSystem, userMessage(heldPrompt)])).toBe(true)
    expect(matches(QWEN_HELD_CHILD_TURN, [userMessage(`Generate a short title for this task:\n${heldPrompt}`)])).toBe(false)
  })
})
