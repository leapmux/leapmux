import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { runningChildOptions } from './scenarios'

describe('runningChildOptions', () => {
  const context = { provider: AgentProvider.DROID, prompt: (text: string) => `${text}\nNATIVE_SCENARIO`, textStep: (text: string) => ({ text }) }
  it('gives two actual native Tasks distinct call IDs and task prompts', () => {
    const first = runningChildOptions(context)
    const second = runningChildOptions(context, { allowExistingRows: true })
    expect(first.spawn.name).toBe('Task')
    expect(second.spawn.name).toBe('Task')
    expect(first.spawn.id).not.toBe(second.spawn.id)
    expect(first.spawn.arguments?.prompt).not.toBe(second.spawn.arguments?.prompt)
    expect(first.parentSteps).toHaveLength(2)
    expect(second.parentSteps).toHaveLength(2)
    expect(second.allowExistingRows).toBe(true)
  })
})
