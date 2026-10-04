import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODEL_IDS } from '../helpers/mockAgentEnvironment'
import { mockScenarioPrompt, readScenarioStatus, registerMockModelScenario } from '../helpers/mockModelScenario'
import { createMockModelServer } from '../helpers/mockModelServer'
import { runningChildOptions } from './scenarios'

describe('runningChildOptions', () => {
  function options(id: string) {
    return runningChildOptions({ provider: AgentProvider.FAST_AGENT, prompt: text => mockScenarioPrompt(id, text), textStep: text => ({ text }) })
  }
  it('generates a source-valid native label and distinct call ID for every actual child', () => {
    const first = options('native-fast-first')
    const second = options('native-fast-second')
    const label = first.spawn.arguments?.label
    expect(typeof label).toBe('string')
    if (typeof label !== 'string')
      throw new Error('The actual Fast Agent spawn contains no native label.')
    expect(label.length).toBeGreaterThan(0)
    expect(label.length).toBeLessThanOrEqual(32)
    expect(label).toMatch(/^[A-Z0-9][\w -]*[A-Z0-9]$/i)
    expect(first.spawn.id).not.toBe(second.spawn.id)
    expect(first.parentSteps).toHaveLength(2)
  })
  it('gives two actual children distinct final reports for native archive correlation', () => {
    const first = options('native-fast-report-first')
    const second = options('native-fast-report-second')
    expect(first.childFinalStep.text).toBeTruthy()
    expect(second.childFinalStep.text).toBeTruthy()
    expect(first.childFinalStep.text).not.toBe(second.childFinalStep.text)
    expect(first.spawn.arguments?.message).toContain(first.childFinalStep.text)
    expect(second.spawn.arguments?.message).toContain(second.childFinalStep.text)
  })
  it('does not consume the child rule for actual parent tool history and native label rejection over HTTP', async () => {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    const id = 'native-fast-parent-error'
    const child = options(id)
    try {
      await registerMockModelScenario(server.url, id, { housekeeping: [], steps: [{ text: 'ACTUAL_PARENT_REPLY' }], rules: [{ name: 'actual child matcher', when: child.childMatcher, respond: { text: 'INCORRECT_CHILD_REPLY' }, once: true }] })
      const response = await fetch(`${server.url}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'mock-model', stream: false, messages: [
          { role: 'user', content: mockScenarioPrompt(id, 'Create the scripted native child.') },
          { role: 'assistant', tool_calls: [{ id: child.spawn.id, type: 'function', function: { name: 'subagent', arguments: JSON.stringify(child.spawn.arguments) } }] },
          { role: 'tool', tool_call_id: child.spawn.id, content: 'Error: 1 validation error for call[subagent]\nlabel\n  String should have at most 32 characters' },
        ] }),
      })
      expect(response.status).toBe(200)
      expect(await response.text()).toContain('ACTUAL_PARENT_REPLY')
      const status = await readScenarioStatus(server.url, id)
      expect(status.nextStep).toBe(1)
      expect(status.ruleMatches).toEqual({})
    }
    finally {
      await server.close()
    }
  })
  it('matches the actual current native child user task over HTTP', async () => {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    const id = 'native-fast-current-child'
    const child = options(id)
    try {
      await registerMockModelScenario(server.url, id, { housekeeping: [], steps: [], rules: [{ name: 'actual child matcher', when: child.childMatcher, respond: { text: 'ACTUAL_CHILD_REPLY' }, once: true }] })
      const response = await fetch(`${server.url}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'mock-model', stream: false, messages: [{ role: 'user', content: child.spawn.arguments?.message }] }),
      })
      expect(response.status).toBe(200)
      expect(await response.text()).toContain('ACTUAL_CHILD_REPLY')
      expect(await readScenarioStatus(server.url, id)).toMatchObject({ nextStep: 0, ruleMatches: { 'actual child matcher': 1 } })
    }
    finally {
      await server.close()
    }
  })
})
