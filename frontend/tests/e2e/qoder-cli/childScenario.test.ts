import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { QODER_CHILD_SYSTEM, qoderChildTurn } from './childScenario'

const childPrompt = mockScenarioPrompt('qoder-child-turn', 'Reply with the single word PONG.')

function answers(systemText: string, userText: string): boolean {
  return matchesRequest(qoderChildTurn('Reply with the single word PONG'), { protocol: 'anthropic-messages', systemText, userText, body: {} })
}

describe('qoderChildTurn', () => {
  it('answers a turn under the system prompt of a child', () => {
    expect(answers(`${QODER_CHILD_SYSTEM}, a CLI coding assistant.`, childPrompt)).toBe(true)
  })

  it('does not answer a root turn that quotes the task of its child', () => {
    expect(answers('You are Qoder, an interactive coding assistant.', `The subagent ran this task:\n${childPrompt}`)).toBe(false)
  })
})
