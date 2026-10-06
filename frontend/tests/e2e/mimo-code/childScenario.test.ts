import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { MIMO_HELD_CHILD_TURN, mimoChildTurn } from './childScenario'

const TASK = 'Reply with the single word PONG.'
const childPrompt = mockScenarioPrompt('mimo-child-turn', TASK)

function answers(matcher: ReturnType<typeof mimoChildTurn>, userText: string): boolean {
  return matchesRequest(matcher, { protocol: 'openai-chat-completions', systemText: '', userText, body: {} })
}

describe('mimoChildTurn', () => {
  it('answers the turn of the child, which opens with its task', () => {
    expect(answers(mimoChildTurn(TASK), childPrompt)).toBe(true)
  })

  it('does not answer a parent turn that quotes the task of its spawn call', () => {
    expect(answers(mimoChildTurn(TASK), JSON.stringify({ description: 'Ask for one word', prompt: childPrompt }))).toBe(false)
    expect(answers(mimoChildTurn(TASK), `The actor finished this task:\n${childPrompt}`)).toBe(false)
  })

  it('reads the punctuation of the task literally', () => {
    expect(answers(mimoChildTurn(TASK), childPrompt.replace('PONG.', 'PONGX'))).toBe(false)
  })
})

describe('MIMO_HELD_CHILD_TURN', () => {
  it('answers the held child and not a turn that quotes its task', () => {
    const heldPrompt = mockScenarioPrompt('mimo-held-child', `${HELD_CHILD_TASK}.`)
    expect(answers(MIMO_HELD_CHILD_TURN, heldPrompt)).toBe(true)
    expect(answers(MIMO_HELD_CHILD_TURN, `The actor finished this task:\n${heldPrompt}`)).toBe(false)
  })
})
