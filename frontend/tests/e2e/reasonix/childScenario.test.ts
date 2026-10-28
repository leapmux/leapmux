import { describe, expect, it } from 'vitest'
import { mockScenarioPrompt } from '../helpers/mockModelScenario'
import { matchesRequest } from '../helpers/mockModelScript'
import { reasonixChildContextPack } from './childContext.fixtures'
import { REASONIX_SPAWN_TASK, reasonixSpawnChildRule } from './childScenario'

/** The prompt that the spawn call gives the child: the task, then the marker of the test's script. */
const childPrompt = mockScenarioPrompt('reasonix-spawn-transcript', REASONIX_SPAWN_TASK)

/** The first user turn of a Reasonix child: the context pack that Reasonix builds around the task of the spawn call. */
const childPack = reasonixChildContextPack(childPrompt)

function answers(userText: string): boolean {
  return matchesRequest(reasonixSpawnChildRule().when, { protocol: 'openai-chat-completions', systemText: '', userText, body: {} })
}

describe('reasonixSpawnChildRule', () => {
  it('answers the turn of the child, whose context pack states the task', () => {
    expect(answers(childPack)).toBe(true)
  })

  it('does not answer a turn that holds the task outside a context pack', () => {
    expect(answers(childPrompt)).toBe(false)
    expect(answers(JSON.stringify({ description: 'Ask the subagent for one word', prompt: childPrompt }))).toBe(false)
  })

  it('does not answer a context pack whose task section holds another task', () => {
    expect(answers(childPack.replace(`\n\n${childPrompt}`, `\n\nReply with another word first.\n${childPrompt}`))).toBe(false)
    expect(answers(childPack.replace(`\n\n${childPrompt}`, `\n\n## Task\n${childPrompt}`))).toBe(false)
  })
})
