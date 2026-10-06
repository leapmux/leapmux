import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { KILO_SPAWN_TASK, kiloSpawnChildRule } from './childScenario'

/** The prompt that the spawn call gives the child: the task, then the marker of the test's script. */
const childPrompt = mockScenarioPrompt('kilo-spawn-transcript', KILO_SPAWN_TASK)

function answers(userText: string): boolean {
  return matchesRequest(kiloSpawnChildRule().when, { protocol: 'openai-chat-completions', systemText: '', userText, body: {} })
}

describe('kiloSpawnChildRule', () => {
  it('answers the turn of the child, which opens with its task', () => {
    expect(answers(childPrompt)).toBe(true)
  })

  it('does not answer a parent turn that quotes the task of its spawn call', () => {
    expect(answers(JSON.stringify({ description: 'Run the shell probe', prompt: childPrompt }))).toBe(false)
    expect(answers(`The subagent ran this task:\n${childPrompt}`)).toBe(false)
  })

  it('does not answer the parent prompt that asks for the spawn', () => {
    expect(answers(mockScenarioPrompt('kilo-spawn-transcript', 'Spawn a subagent that runs the shell probe and reports the result.'))).toBe(false)
  })

  it('reads the punctuation of the task literally', () => {
    expect(answers(childPrompt.replace('result.', 'resultX'))).toBe(false)
  })
})
