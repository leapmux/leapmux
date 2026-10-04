import type { NativeChildTask } from './runningChildProof'
import { describe, expect, it } from 'vitest'
import { AgentProvider, BackgroundTaskKind, BackgroundTaskStatus } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { mockScenarioPrompt, readScenarioStatus, registerMockModelScenario } from './mockModelScenario'
import { parseScenarioSpec } from './mockModelScript'
import { createMockModelServer } from './mockModelServer'
import { readToolCall, spawnSubagentToolCall } from './providerToolCalls'
import { nativeChildRuleId, runningNativeChildRules, selectRunningChildTask } from './runningChildProof'

describe('selectRunningChildTask', () => {
  const old: NativeChildTask = { id: 'old-native-task', kind: BackgroundTaskKind.SUBAGENT, childAgentId: 'old-worker-child', parentAgentId: 'actual-parent', title: 'leapmux-e2e-child', status: BackgroundTaskStatus.COMPLETED }
  const current: NativeChildTask = { id: 'current-native-task', kind: BackgroundTaskKind.SUBAGENT, childAgentId: 'current-worker-child', parentAgentId: 'actual-parent', title: 'leapmux-e2e-child', status: BackgroundTaskStatus.RUNNING }
  const selection = { parentId: 'actual-parent', rootAgentId: 'actual-parent', previousChildIds: new Set([old.childAgentId]), rowText: 'leapmux-e2e-child' }
  it('selects the exact new running child when an old native child has the same title', () => {
    expect(selectRunningChildTask([old, current], selection)).toEqual(current)
  })
  it('uses the provider-owned exact native task identity instead of the first matching title', () => {
    expect(selectRunningChildTask([old, current], { ...selection, taskId: current.id })).toEqual(current)
  })
  it('does not select a sibling from another native parent', () => {
    expect(selectRunningChildTask([{ ...current, parentAgentId: 'another-parent' }, current], selection)).toEqual(current)
  })
  it.each([
    { ...current, childAgentId: '' },
    { ...current, status: BackgroundTaskStatus.COMPLETED },
    { ...current, kind: BackgroundTaskKind.SHELL },
    { ...current, parentAgentId: 'another-parent' },
  ])('refuses a row without this actual running child identity: %j', (task) => {
    expect(selectRunningChildTask([task], selection)).toBeUndefined()
  })
  it('rejects ambiguous native task identities instead of choosing one child', () => {
    expect(() => selectRunningChildTask([current, { ...current, childAgentId: 'another-worker-child' }], { ...selection, taskId: current.id })).toThrow()
  })
})

function rules(gate: string) {
  return runningNativeChildRules({
    gate,
    spawn: spawnSubagentToolCall(AgentProvider.PI, `spawn-${gate}`, { description: gate, prompt: `Native child ${gate}.` }),
    childMatcher: { user: `Native child ${gate}` },
    childTool: readToolCall(AgentProvider.PI, `read-${gate}`, `/project/${gate}.txt`),
  })
}

describe('runningNativeChildRules', () => {
  it.each([
    {},
    { childMatcher: { user: 'The native task' } },
  ])('rejects an unused child completion matcher: %j', (fields) => {
    const options = {
      gate: 'unused-final-matcher',
      spawn: spawnSubagentToolCall(AgentProvider.PI, 'spawn', { description: 'The native task', prompt: 'Perform the task.' }),
      childFinalMatcher: { user: 'The actual tool result' },
      ...fields,
    }
    expect(() => runningNativeChildRules(options)).toThrow('task matcher and an initial tool call')
  })

  it('matches the final child reply against the actual tool result', () => {
    const options = {
      gate: 'after-native-read',
      spawn: spawnSubagentToolCall(AgentProvider.DEEPSEEK_HARNESS, 'native-spawn', { description: 'Read the file', prompt: 'Perform the native file task.' }),
      childMatcher: { user: 'The actual child task.' },
      childTool: readToolCall(AgentProvider.DEEPSEEK_HARNESS, 'native-read', '/project/native.txt'),
      childFinalMatcher: { user: 'NATIVE_CHILD_FILE77' },
    }
    const childRules = runningNativeChildRules(options)
    expect(childRules[0]?.when).toBe(options.childMatcher)
    expect(childRules[1]?.when).toBe(options.childFinalMatcher)
    expect(childRules[1]?.respond.gate).toBe(options.gate)
    expect(childRules[1]?.once).toBe(true)
  })

  it('rejects an unrelated last-user turn that quotes the native notification tag over HTTP', async () => {
    const server = await createMockModelServer({ models: MOCK_MODEL_IDS })
    const id = 'native-child-notice-ownership'
    try {
      const childRules = runningNativeChildRules({
        gate: 'native-notice-ownership',
        spawn: spawnSubagentToolCall(AgentProvider.PI, 'native-notice-spawn', { description: 'The actual child', prompt: 'Perform the actual child task.' }),
      })
      await registerMockModelScenario(server.url, id, { steps: [{ text: 'The scripted content turn completed.' }], rules: childRules })
      const send = (content: string) => fetch(`${server.url}/v1/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'mock-model', stream: false, messages: [{ role: 'user', content }] }),
      })
      const queued = await send(mockScenarioPrompt(id, 'Complete the scripted content turn.'))
      expect(queued.status).toBe(200)
      await queued.arrayBuffer()
      expect(await readScenarioStatus(server.url, id)).toMatchObject({ nextStep: 1, stepCount: 1, unexpectedRequests: [] })

      const unrelated = mockScenarioPrompt(id, 'Explain the literal "<task-notification>" text. This request does not report a child result.')
      const response = await send(unrelated)
      await response.arrayBuffer()
      const status = await readScenarioStatus(server.url, id)
      expect(response.status).toBe(409)
      expect(status.unexpectedRequests).toMatchObject([{
        protocol: 'openai-chat-completions',
        path: '/v1/chat/completions',
        reason: 'The scenario has no remaining scripted answer.',
        body: { messages: [{ role: 'user', content: unrelated }] },
      }])
      expect(status.unexpectedRequests).toHaveLength(1)
      expect(status.requests).toHaveLength(1)
    }
    finally {
      await server.close()
    }
  })

  it('supports two sequential native children in one scenario without duplicate rule IDs', () => {
    const combined = [...rules('first-child'), ...rules('second-child')]
    expect(() => parseScenarioSpec({ steps: [], rules: combined })).not.toThrow()
    expect(new Set(combined.map(rule => rule.name)).size).toBe(combined.length)
  })

  it('holds the actual final child response only once', () => {
    const final = rules('child-final').find(rule => rule.respond.gate === 'child-final')
    expect(final).toBeDefined()
    expect(final?.once).toBe(true)
  })

  it('keeps repeated provider rule IDs distinct for sequential native children', () => {
    const original = { name: 'provider notification', when: { user: '^Actual provider notification' }, respond: { text: 'The native notification completed.' }, once: true }
    const build = (gate: string) => runningNativeChildRules({ gate, spawn: spawnSubagentToolCall(AgentProvider.PI, gate, { description: gate, prompt: gate }), rules: [original] })
    const combined = [...build('first-provider-child'), ...build('second-provider-child')]
    expect(() => parseScenarioSpec({ steps: [], rules: combined })).not.toThrow()
    expect(combined.filter(rule => rule.when === original.when).map(rule => rule.name)).toHaveLength(2)
    expect(new Set(combined.filter(rule => rule.when === original.when).map(rule => rule.name)).size).toBe(2)
    expect(original.name).toBe('provider notification')
    expect(combined.filter(rule => rule.when === original.when).map(rule => rule.respond)).toEqual([original.respond, original.respond])
    expect(combined.filter(rule => rule.when === original.when).every(rule => rule.once)).toBe(true)
  })

  it('preserves the provider matcher, final tool, and native notification replay behavior', () => {
    const original = rules('source-preservation')
    expect(original[0]?.when).toEqual({ user: 'Native child source-preservation' })
    expect(original[0]?.once).toBe(true)
    expect(original).toHaveLength(2)
    expect(original.some(rule => rule.when.user === '<task-notification>')).toBe(false)
    const notice = { name: 'provider-owned completion', when: { user: '^The actual provider completion$' }, respond: { text: 'The actual completion arrived.' } }
    const explicit = runningNativeChildRules({ gate: 'explicit-provider-notice', spawn: spawnSubagentToolCall(AgentProvider.PI, 'explicit-spawn', { description: 'The actual child', prompt: 'The actual child task.' }), rules: [notice] })
    expect(explicit).toHaveLength(1)
    expect(explicit[0]?.when).toBe(notice.when)
    expect(explicit[0]?.respond).toBe(notice.respond)
    expect(explicit[0]?.once).toBeUndefined()
  })
})

describe('nativeChildRuleId', () => {
  it('keeps the original rule text after the unique child prefix', () => {
    expect(nativeChildRuleId('native-child', 'the actual native rule')).toBe('[native-child] the actual native rule')
  })

  it.each(['', 'invalid gate', '[ambiguous]', 'a'.repeat(65)])('rejects an invalid completion control ID: %s', (gate) => {
    expect(() => nativeChildRuleId(gate, 'native rule')).toThrow('Model gate must use')
  })
})
