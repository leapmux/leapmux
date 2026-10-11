import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { KIRO_CHILD_AGENT } from '../helpers/providerToolCalls'
import { HELD_CHILD_TASK } from '../helpers/subagentRegistry'
import { KIRO_HELD_CHILD_TURN, kiroChildTurn } from './childScenario'

const heldPrompt = mockScenarioPrompt('kiro-held-child', `${HELD_CHILD_TASK}.`)

/** A Kiro request in `agentMode` whose newest user turn is `userText`. */
function request(agentMode: string, userText: string) {
  return { protocol: 'aws-event-stream' as const, systemText: '', userText, body: { conversationState: { agentMode, currentMessage: { userInputMessage: { content: userText } } } } }
}

describe('kiroChildTurn', () => {
  it('answers a turn in the agent mode of the context gatherer', () => {
    expect(matchesRequest(kiroChildTurn('Reply with the single word PONG'), request(KIRO_CHILD_AGENT, 'Reply with the single word PONG.'))).toBe(true)
  })

  it('does not answer a turn in another agent mode that holds the same text', () => {
    expect(matchesRequest(kiroChildTurn('Reply with the single word PONG'), request('vibe', 'Reply with the single word PONG.'))).toBe(false)
  })
})

describe('KIRO_HELD_CHILD_TURN', () => {
  it('answers the turn of the held child', () => {
    expect(matchesRequest(KIRO_HELD_CHILD_TURN, request(KIRO_CHILD_AGENT, heldPrompt))).toBe(true)
  })

  it('does not answer a parent turn that quotes the task of the held child', () => {
    expect(matchesRequest(KIRO_HELD_CHILD_TURN, request('vibe', `The subagent ran this task:\n${heldPrompt}`))).toBe(false)
  })
})
