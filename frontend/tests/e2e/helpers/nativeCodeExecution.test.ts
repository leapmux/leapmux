import type { Page } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProviderAgent } from './workspace'
import { runInNewContext } from 'node:vm'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { unitWorkingDir } from '~/test-support/unitWorkingDir'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { exerciseNativeCodeExecution, expectNativeCodeExecutionAbsent, nativeCodeExecutionSchema, openNativeCatalogTurn, validateNativeScriptCases } from './nativeCodeExecution'

const opened = vi.hoisted(() => ({ events: [] as string[], open: vi.fn() }))
const execution = vi.hoisted(() => ({
  rows: new Map<string, { text: string, status: string }>(),
  answers: [] as string[],
  rawCalls: [] as string[],
  reloads: 0,
}))

interface CodeRowProbe { codeRowProbe: true, callId: string }
interface CodeAnswerProbe { codeAnswerProbe: true, text: string }

function codeRow(value: unknown): value is CodeRowProbe {
  return typeof value === 'object' && value !== null && 'codeRowProbe' in value
}

function codeAnswer(value: unknown): value is CodeAnswerProbe {
  return typeof value === 'object' && value !== null && 'codeAnswerProbe' in value
}

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const check = (value: unknown, message?: string) => {
    if (codeRow(value)) {
      return {
        toHaveCount: async (count: number) => actual.expect(execution.rows.has(value.callId) ? 1 : 0, 'the computed native result row exists').toBe(count),
        toHaveAttribute: async (attribute: string, expected: string) => {
          actual.expect(attribute).toBe('data-tool-status')
          actual.expect(execution.rows.get(value.callId)?.status).toBe(expected)
        },
        toContainText: async (expected: string) => actual.expect(execution.rows.get(value.callId)?.text).toContain(expected),
      }
    }
    if (codeAnswer(value))
      return { toBeVisible: async () => actual.expect(execution.answers).toContain(value.text) }
    return actual.expect(value, message)
  }
  return { ...actual, expect: Object.assign(check, actual.expect) }
})

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
  toolCallRow: (_page: unknown, callId: string): CodeRowProbe => ({ codeRowProbe: true, callId }),
  assistantBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: (): CodeAnswerProbe => ({ codeAnswerProbe: true, text: hasText }) }) }),
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

vi.mock('./nativeToolExecution', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeToolExecution')>(),
  runNativeToolTurn: async (_context: unknown, turn: { toolCalls: Array<{ id: string, arguments?: Record<string, unknown> }>, answer: string }) => {
    const call = turn.toolCalls[0]
    const source = call?.arguments?.code
    if (!call || typeof source !== 'string')
      throw new Error('The native code fixture requires its actual script source.')
    let text: string
    let status: string
    try {
      text = String(runInNewContext(`(function () { ${source} })()`, { Error, String }))
      status = 'completed'
    }
    catch (error) {
      if (!(error instanceof Error))
        throw error
      text = error.message
      status = 'failed'
    }
    execution.rawCalls.push(call.id)
    execution.rows.set(`part-${call.id}`, { text, status })
    execution.answers.push(turn.answer)
    const resultRequest = { protocol: 'openai-chat-completions', path: '/model', body: { messages: [{ role: 'tool', tool_call_id: call.id, content: text }] } }
    return { start: 0, toolRequest: { protocol: 'openai-chat-completions', path: '/model', body: { tools: [{ function: { name: 'exec' } }] } }, resultRequest }
  },
}))

beforeEach(() => {
  opened.events = []
  execution.rows.clear()
  execution.answers = []
  execution.rawCalls = []
  execution.reloads = 0
  opened.open.mockReset()
  opened.open.mockImplementation(async (_server: unknown, _workspace: string, directory: string) => {
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

  it.each([undefined, []])('rejects a missing catalog: %j', (tools) => {
    expect(() => nativeCodeExecutionSchema(request(tools), 'native_runner', { source: 'string' })).toThrow('The native model request contains no nonempty tool catalog.')
  })

  it.each([
    { tools: [{ function: { name: 'other', parameters: schema } }], count: 0 },
    { tools: [{ function: tool }, { function: tool }], count: 2 },
  ])('rejects $count descriptors for the tool', ({ tools, count }) => {
    expect(() => nativeCodeExecutionSchema(request(tools), 'native_runner', { source: 'string' })).toThrow(`The native catalog contains ${count} descriptors for native_runner.`)
  })

  it('rejects a catalog entry that is not an object', () => {
    expect(() => nativeCodeExecutionSchema(request(['native_runner', { function: tool }]), 'native_runner', { source: 'string' })).toThrow('The native model tool catalog contains an invalid entry.')
  })

  it.each([null, {}, { type: 'array', properties: {} }])('rejects an absent or incomplete object schema: %j', (parameters) => {
    expect(() => nativeCodeExecutionSchema(request([{ function: { name: tool.name, parameters } }]), 'native_runner', { source: 'string' })).toThrow('The native executor has no complete object argument schema.')
  })

  it.each([{ type: 'object', properties: {} }, { type: 'object', properties: { source: { type: 'number' } } }])('rejects a changed source field: %j', (parameters) => {
    expect(() => nativeCodeExecutionSchema(request([{ function: { name: tool.name, parameters } }]), 'native_runner', { source: 'string' })).toThrow('The native executor argument source has no string schema.')
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
    // Playwright's `expect` fails with the result of the matcher: `not.toContain` failed, because the catalog holds the name.
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'anthropic-messages', path: '/v1/messages', body: { tools: [{ name: 'exec' }] } }, ['exec']))
      .toThrow(expect.objectContaining({ matcherResult: expect.objectContaining({ name: 'toContain', pass: true }) }))
  })

  it('rejects an empty catalog and absent executor definitions', () => {
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { tools: [] } }, ['exec'])).toThrow('nonempty tool catalog')
    expect(() => expectNativeCodeExecutionAbsent({ protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: {} }, [])).toThrow('audited executor names')
  })
})

const cursor: ProviderAgent = { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e' }

/** The scenario context of an agent that opens by the rule of `providerAgent`. */
function contextOf(providerAgent: ProviderAgent = cursor): ManagedNativeScenarioContext {
  return {
    page: {} as Page,
    provider: providerAgent.provider,
    providerAgent,
    workspaceId: 'workspace-1',
    leapmuxServer: { hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' },
  } as unknown as ManagedNativeScenarioContext
}

const context = contextOf()

describe('exerciseNativeCodeExecution', () => {
  it('keeps model call IDs in computed receipts and selects native result IDs on both browser passes', async () => {
    const resolveToolRowId = vi.fn(async (query: { callId: string, agentId: string }) => {
      expect(query.agentId).toBe('agent-1')
      return `part-${query.callId}`
    })
    const nativeProof = vi.fn(async (_request: unknown, callId: string, expected: string) => {
      expect(execution.rawCalls).toContain(callId)
      expect(execution.rows.get(`part-${callId}`)?.text).toBe(expected)
    })
    const prepareResultView = vi.fn(async (rowId: string) => {
      expect(execution.rows.has(rowId)).toBe(true)
    })
    const browserProof = vi.fn(async (rowId: string, expected: string, failed: boolean) => {
      expect(execution.rows.get(rowId)).toEqual({ text: expected, status: failed ? 'failed' : 'completed' })
    })
    const scenario = {
      ...contextOf({ provider: AgentProvider.MIMO_CODE, prefix: 'mimo-e2e' }),
      page: { reload: async () => { execution.reloads++ } } as unknown as Page,
      resolveToolRowId,
    }
    await exerciseNativeCodeExecution(scenario, {
      scripts: () => [
        { label: 'output', source: 'return String(40 + 2)', expected: '42', failed: false },
        { label: 'failure', source: 'throw new Error(String(70 + 7))', expected: '77', failed: true },
      ],
      nativeProof,
      prepareResultView,
      browserProof,
    })
    expect(execution.rawCalls).toEqual(['native-code-0', 'native-code-1'])
    expect(nativeProof.mock.calls.map(call => call[1])).toEqual(['native-code-0', 'native-code-1'])
    expect(prepareResultView.mock.calls.map(call => call[0])).toEqual(['part-native-code-0', 'part-native-code-0', 'part-native-code-1', 'part-native-code-1'])
    expect(browserProof.mock.calls.map(call => call[0])).toEqual(['part-native-code-0', 'part-native-code-0', 'part-native-code-1', 'part-native-code-1'])
    expect(execution.reloads).toBe(2)
  })

  it('preserves a failed browser proof and runs no later script', async () => {
    const failure = new Error('The browser result does not match its native output.')
    const scenario = {
      ...contextOf({ provider: AgentProvider.MIMO_CODE, prefix: 'mimo-e2e' }),
      resolveToolRowId: async ({ callId }: { callId: string }) => `part-${callId}`,
    }
    await expect(exerciseNativeCodeExecution(scenario, {
      scripts: () => [
        { label: 'output', source: 'return String(40 + 2)', expected: '42', failed: false },
        { label: 'failure', source: 'throw new Error(String(70 + 7))', expected: '77', failed: true },
      ],
      browserProof: async () => { throw failure },
    })).rejects.toBe(failure)
    expect(execution.rawCalls).toEqual(['native-code-0'])
    expect(execution.reloads).toBe(0)
  })
})

describe('openNativeCatalogTurn', () => {
  it('opens the agent with the pinned settings in a fresh directory, shows the workspace, and returns the catalog turn', async () => {
    const request = await openNativeCatalogTurn(context)
    expect(request.body).toEqual({ catalog: true })
    expect(opened.open).toHaveBeenCalledWith(expect.objectContaining({ hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' }), 'workspace-1', '/run/native-code-limit-directory', agentOpenOptions(AgentProvider.CURSOR))
    expect(opened.events).toEqual(['directory native-code-limit-', 'open /run/native-code-limit-directory', 'workspace workspace-1', 'answer Reply once while the native tool catalog remains available.'])
  })

  it('takes the directory prefix and the open settings of the caller', async () => {
    await openNativeCatalogTurn(context, { directoryPrefix: 'native-workflow-code-', overrides: { optionValues: { permissionMode: 'manual' } } })
    expect(opened.open).toHaveBeenCalledWith(expect.objectContaining({ hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' }), 'workspace-1', '/run/native-workflow-code-directory', agentOpenOptions(AgentProvider.CURSOR, { optionValues: { permissionMode: 'manual' } }))
  })

  it('creates the directory by the working directory rule of the provider', async () => {
    const workingDir = vi.fn((prefix: string) => unitWorkingDir(`/run/${prefix}repository/repo`))
    await openNativeCatalogTurn(contextOf({ ...cursor, workingDir }))
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('native-code-limit-')
    expect(opened.open).toHaveBeenCalledWith(expect.objectContaining({ hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker-1' }), 'workspace-1', '/run/native-code-limit-repository/repo', agentOpenOptions(AgentProvider.CURSOR))
  })

  it('runs no turn when the agent does not open', async () => {
    opened.open.mockRejectedValue(new Error('the Worker refused the agent'))
    await expect(openNativeCatalogTurn(context)).rejects.toThrow('the Worker refused the agent')
    expect(opened.events).toEqual(['directory native-code-limit-'])
  })
})
