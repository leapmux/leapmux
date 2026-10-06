import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { HELD_CHILD_NAME, HELD_CHILD_REPORT, HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { ohMyPiChildTurn, ohMyPiHeldChild, OMP_ASSIGNMENT_OPENING } from './childScenario'

const heldPrompt = mockScenarioPrompt('omp-held-child', `${HELD_CHILD_TASK}.`)

/** The `<system-reminder>` block that Oh My Pi puts before each prompt, so a prompt never starts with its task. */
const REMINDER = '<system-reminder>\nThe current date is 2026-10-06.\n</system-reminder>\n'

/** The tools that Oh My Pi offers a parent. Only a child also gets `yield`. */
const PARENT_TOOLS = ['read', 'bash', 'task'] as const

/** An Oh My Pi request whose last user text is `userText`, and that offers the tools in `toolNames`. */
function request(userText: string, toolNames: readonly string[]) {
  return {
    protocol: 'openai-chat-completions' as const,
    systemText: '',
    userText,
    body: {
      messages: [{ role: 'user', content: userText }],
      tools: toolNames.map(name => ({ type: 'function', function: { name, parameters: { type: 'object' } } })),
    },
  }
}

/** A child request: it offers `yield` beside the parent tools. */
function childRequest(userText: string) {
  return request(userText, [...PARENT_TOOLS, 'yield'])
}

describe('ohMyPiChildTurn', () => {
  it('answers the turn of a child, which opens with the assignment and offers the yield tool', () => {
    expect(matchesRequest(ohMyPiChildTurn(), childRequest(`${REMINDER}${OMP_ASSIGNMENT_OPENING}.\n\nReply with PONG.`))).toBe(true)
  })

  it('does not answer a parent turn that holds the same text, because a parent has no yield tool', () => {
    expect(matchesRequest(ohMyPiChildTurn(), request(`${REMINDER}${OMP_ASSIGNMENT_OPENING}.\n\nReply with PONG.`, PARENT_TOOLS))).toBe(false)
  })

  it('does not answer the turn of a child that has another task', () => {
    expect(matchesRequest(ohMyPiChildTurn('Reply with PONG'), childRequest(`${REMINDER}${OMP_ASSIGNMENT_OPENING}.\n\nList the files.`))).toBe(false)
  })
})

describe('ohMyPiHeldChild', () => {
  it('holds the turn of the held child', () => {
    expect(matchesRequest(ohMyPiHeldChild().childTurn, childRequest(`${REMINDER}${OMP_ASSIGNMENT_OPENING}.\n\n${heldPrompt}`))).toBe(true)
  })

  it('does not hold a parent turn that quotes the task of the held child', () => {
    expect(matchesRequest(ohMyPiHeldChild().childTurn, request(`${REMINDER}The subagent ran this task:\n${heldPrompt}`, PARENT_TOOLS))).toBe(false)
  })

  it('ends the held child through its yield tool and shows the name of the child', () => {
    const held = ohMyPiHeldChild()
    expect(held.rowTitle).toBe(HELD_CHILD_NAME)
    expect(held.heldAnswer?.toolCalls).toEqual([{ id: 'held-child-yield', name: 'yield', arguments: { data: HELD_CHILD_REPORT } }])
  })

  // `openHeldChildTab` takes the case as its own, so two cells must not share one object.
  it('returns a new case for each call', () => {
    expect(ohMyPiHeldChild()).not.toBe(ohMyPiHeldChild())
    expect(ohMyPiHeldChild().childTurn).not.toBe(ohMyPiHeldChild().childTurn)
  })
})
