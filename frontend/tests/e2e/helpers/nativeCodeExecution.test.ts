import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { expectNativeCodeExecutionAbsent, nativeCodeExecutionSchema, openNativeCatalogTurn, validateNativeScriptCases } from './nativeCodeExecution'

const opened = vi.hoisted(() => ({ events: [] as string[], open: vi.fn() }))
vi.mock('./api', () => ({ openAgentViaAPI: opened.open }))
vi.mock('./runDirectory', async importOriginal => ({
  ...await importOriginal<typeof import('./runDirectory')>(),
  createTestDirectory: (prefix: string) => {
    opened.events.push(`directory ${prefix}`)
    return `/run/${prefix}directory`
  },
}))
vi.mock('./ui', async importOriginal => ({
  ...await importOriginal<typeof import('./ui')>(),
  openWorkspace: async (_page: unknown, workspaceId: string) => { opened.events.push(`workspace ${workspaceId}`) },
}))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  selectedAgentTabId: async () => 'agent-1',
}))
vi.mock('./nativeConversation', () => ({
  sendNativeAnswer: async (_context: unknown, prompt: string) => {
    opened.events.push(`answer ${prompt}`)
    return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { catalog: true } }
  },
}))

beforeEach(() => {
  opened.events = []
  opened.open.mockReset()
  opened.open.mockImplementation(async (_hub: string, _cookie: string, _worker: string, _workspace: string, directory: string) => {
    opened.events.push(`open ${directory}`)
    return 'agent-1'
  })
})

describe('nativeCodeExecutionSchema', () => {
  const schema = { type: 'object', properties: { source: { type: 'string' }, options: { type: 'object' } } }
  const tool = { name: 'native_runner', parameters: schema }
  const request = (tools: unknown) => ({ protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', body: { tools } })

  it('reads exact direct and nested native schemas without changing them', () => {
    expect(nativeCodeExecutionSchema(request([{ function: tool }]), 'native_runner', { source: 'string', options: 'object' })).toBe(schema)
    expect(nativeCodeExecutionSchema(request([{ name: tool.name, input_schema: schema }]), 'native_runner', { source: 'string' })).toBe(schema)
  })

  it('reads the exact Google parametersJsonSchema from function declarations', () => {
    const body = { tools: [{ functionDeclarations: [{ name: tool.name, parametersJsonSchema: schema }] }] }
    expect(nativeCodeExecutionSchema({ protocol: 'google-generative-language', path: '/google', body }, tool.name, { source: 'string' })).toBe(schema)
  })

  it.each([undefined, [], [{ function: { name: 'other', parameters: schema } }], [{ function: tool }, { function: tool }]])('rejects a missing or ambiguous actual descriptor: %j', (tools) => {
    expect(() => nativeCodeExecutionSchema(request(tools), 'native_runner', { source: 'string' })).toThrow()
  })

  it.each([null, {}, { type: 'array', properties: {} }, { type: 'object', properties: {} }, { type: 'object', properties: { source: { type: 'number' } } }])('rejects an absent or changed source schema: %j', (parameters) => {
    expect(() => nativeCodeExecutionSchema(request([{ function: { name: tool.name, parameters } }]), 'native_runner', { source: 'string' })).toThrow()
  })

  it('rejects a proof without a tool or argument fields', () => {
    expect(() => nativeCodeExecutionSchema(request([{ function: tool }]), '', { source: 'string' })).toThrow('tool and its argument fields')
    expect(() => nativeCodeExecutionSchema(request([{ function: tool }]), tool.name, {})).toThrow('tool and its argument fields')
  })
})

describe('validateNativeScriptCases', () => {
  const output = { label: 'output', source: 'text(40 + 2)', expected: '42', failed: false }
  const failure = { label: 'failure', source: 'throw new Error(String(70 + 7))', expected: '77', failed: true }

  it('accepts actual computed output and a computed failure', () => {
    expect(() => validateNativeScriptCases([output, failure])).not.toThrow()
  })

  it.each([{ cases: [] }, { cases: [output] }, { cases: [output, output] }, { cases: [failure, failure] }])('requires both native outcomes: %j', ({ cases }) => {
    expect(() => validateNativeScriptCases(cases)).toThrow('output and failure cases')
  })

  it.each([
    { ...output, label: '' },
    { ...output, source: '' },
    { ...output, expected: '' },
    { ...output, source: 'text("42")' },
  ])('refuses a missing or predetermined proof: %j', (invalid) => {
    expect(() => validateNativeScriptCases([invalid, failure])).toThrow('require script execution')
  })
})

describe('expectNativeCodeExecutionAbsent', () => {
  it('checks an actual nonempty native catalog', () => {
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools: [{ function: { name: 'native_read' } }] } }, ['exec', 'codemode'])).not.toThrow()
  })

  it('rejects a native executor that the catalog actually offers', () => {
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'anthropic-messages', path: '/v1/messages', body: { tools: [{ name: 'exec' }] } }, ['exec'])).toThrow()
  })

  it('rejects an empty catalog and absent executor definitions', () => {
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools: [] } }, ['exec'])).toThrow('nonempty tool catalog')
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: {} }, [])).toThrow('audited executor names')
  })
})

const context = {
  page: {} as Page,
  provider: AgentProvider.CURSOR,
  workspaceId: 'workspace-1',
  leapmuxServer: { hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' },
} as unknown as ManagedNativeScenarioContext

const cursor: ProviderAgent = { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e' }

describe('openNativeCatalogTurn', () => {
  it('opens the agent with the pinned settings in a fresh directory, shows the workspace, and returns the catalog turn', async () => {
    const request = await openNativeCatalogTurn(context, cursor)
    expect(request.body).toEqual({ catalog: true })
    expect(opened.open).toHaveBeenCalledWith('http://hub', 'token', 'worker-1', 'workspace-1', '/run/native-code-limit-directory', agentOpenOptions(AgentProvider.CURSOR))
    expect(opened.events).toEqual(['directory native-code-limit-', 'open /run/native-code-limit-directory', 'workspace workspace-1', 'answer Reply once while the native tool catalog remains available.'])
  })

  it('takes the directory prefix and the open settings of the caller', async () => {
    await openNativeCatalogTurn(context, cursor, { directoryPrefix: 'native-workflow-code-', overrides: { optionValues: { permissionMode: 'manual' } } })
    expect(opened.open).toHaveBeenCalledWith('http://hub', 'token', 'worker-1', 'workspace-1', '/run/native-workflow-code-directory', agentOpenOptions(AgentProvider.CURSOR, { optionValues: { permissionMode: 'manual' } }))
  })

  it('creates the directory by the working directory rule of the provider', async () => {
    const workingDir = vi.fn((prefix: string) => `/run/${prefix}repository/repo`)
    await openNativeCatalogTurn(context, { ...cursor, workingDir })
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-code-limit-')
    expect(opened.open).toHaveBeenCalledWith('http://hub', 'token', 'worker-1', 'workspace-1', '/run/native-code-limit-repository/repo', agentOpenOptions(AgentProvider.CURSOR))
  })

  it('runs no turn when the agent does not open', async () => {
    opened.open.mockRejectedValue(new Error('the Worker refused the agent'))
    await expect(openNativeCatalogTurn(context, cursor)).rejects.toThrow('the Worker refused the agent')
    expect(opened.events).toEqual(['directory native-code-limit-'])
  })
})
