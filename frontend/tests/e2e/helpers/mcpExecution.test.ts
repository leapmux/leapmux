import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeToolTurn } from './nativeToolExecution'
import type { ProviderAgent } from './workspace'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { fakeLocator } from '~/test-support/fakeLocator'
import { exerciseMcpEcho, invokeNativeMcpTool, nativeMcpAnswer, withNativeMcpFormAgent } from './mcpExecution'
import { MCP_FORM_SERVER_NAME } from './mcpFormServer'

/** The turns, the browser steps, and the files of one test, in order. */
const run = vi.hoisted(() => ({
  turns: [] as NativeToolTurn[],
  events: [] as string[],
  resultText: '',
  scratch: '',
  configurationAtOpen: null as string | null,
  configurationPath: '',
}))

vi.mock('./nativeToolExecution', () => ({
  runNativeToolTurn: async (_context: unknown, turn: NativeToolTurn) => {
    run.turns.push(turn)
    const callId = turn.toolCalls[0]?.id ?? ''
    const resultRequest: MockModelRequestRecord = {
      protocol: 'anthropic-messages',
      path: '/v1/messages',
      stepIndex: 4,
      body: { messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: callId, content: run.resultText }] }] },
    }
    return { start: 3, toolRequest: { ...resultRequest, stepIndex: 3 }, resultRequest }
  },
}))
vi.mock('./ui', () => ({
  assistantBubbles: () => ({ filter: ({ hasText }: { hasText: string }) => ({ first: () => passingLocator(`answer ${hasText}`) }) }),
  openWorkspace: async (_page: unknown, workspaceId: string) => { run.events.push(`open workspace ${workspaceId}`) },
  applyPermissionPreset: async (_page: unknown, preset: string) => { run.events.push(`preset ${preset}`) },
}))
vi.mock('./api', () => ({
  openAgentViaAPI: async (_server: unknown, workspaceId: string, _workingDir: string, options: { agentProvider: AgentProvider }) => {
    run.events.push(`open agent ${AgentProvider[options.agentProvider]} in ${workspaceId}`)
    run.configurationAtOpen = readFileSync(run.configurationPath, 'utf8')
    return 'agent'
  },
}))
vi.mock('../agentSettings', () => ({ agentOpenOptions: (agentProvider: AgentProvider) => ({ agentProvider }) }))
vi.mock('./nativeScenario', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeScenario')>(),
  selectedAgentTabId: async () => 'agent',
}))
vi.mock('./server', () => ({ getGlobalState: () => ({ tmpDir: run.scratch }) }))
vi.mock('./runDirectory', () => ({ createTestDirectory: (prefix: string) => mkdtempSync(join(run.scratch, prefix)) }))

/** A fake locator whose Playwright checks pass and leave a record. */
function passingLocator(name: string): Locator {
  return fakeLocator((check) => {
    run.events.push(`${name} ${check.expression}`)
    return true
  })
}

/** How a Claude Code agent opens in these tests: in a fresh directory of the run. */
const claude: ProviderAgent = { provider: AgentProvider.CLAUDE_CODE, prefix: 'claude-e2e' }

/** The scenario context of an agent of `provider` that opens by the rule of `providerAgent`. */
function context(provider = AgentProvider.CLAUDE_CODE, providerAgent: ProviderAgent = { ...claude, provider }): ManagedNativeScenarioContext {
  return {
    page: {} as Page,
    modelScript: {} as ModelScript,
    provider,
    providerAgent,
    workspaceId: 'workspace',
    leapmuxServer: { hubUrl: 'http://hub', adminToken: 'token', workerId: 'worker' },
  }
}

const scratchRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
beforeEach(() => {
  mkdirSync(scratchRoot, { recursive: true })
  run.scratch = mkdtempSync(join(scratchRoot, 'mcp-execution-unit-'))
  run.turns = []
  run.events = []
  run.resultText = ''
  run.configurationAtOpen = null
  run.configurationPath = join(run.scratch, 'native', 'mcp.json')
})
afterEach(() => rmSync(run.scratch, { recursive: true, force: true }))

describe('nativeMcpAnswer', () => {
  it('gives each call ID its own answer', () => {
    expect(nativeMcpAnswer('first')).toBe('The MCP call first completed.')
    expect(nativeMcpAnswer('first')).not.toBe(nativeMcpAnswer('second'))
  })
})

describe('invokeNativeMcpTool', () => {
  it('runs one turn with the built call and returns the request that holds the result', async () => {
    const request = await invokeNativeMcpTool(context(), { server: 'echo_probe', tool: 'echo', callId: 'call-1', input: { value: '' } })
    expect(request.stepIndex).toBe(4)
    expect(run.turns).toEqual([{
      toolCalls: [{ id: 'call-1', name: 'mcp__echo_probe__echo', arguments: { value: '' } }],
      prompt: 'Call the echo_probe echo tool once.',
      answer: nativeMcpAnswer('call-1'),
    }])
  })

  it.each(['', ' '])('refuses an empty call ID before a turn: %j', async (callId) => {
    await expect(invokeNativeMcpTool(context(), { server: 'echo_probe', tool: 'echo', callId, input: {} })).rejects.toThrow('needs a call ID')
    expect(run.turns).toEqual([])
  })

  it('refuses a provider whose builder changes the call ID, before a turn', async () => {
    await expect(invokeNativeMcpTool(context(AgentProvider.DROID), { server: 'echo_probe', tool: 'echo', callId: 'call-1', input: {} }))
      .rejects
      .toThrow('has the ID call_call-1, not call-1')
    expect(run.turns).toEqual([])
  })
})

describe('exerciseMcpEcho', () => {
  it('requires the echoed value in the tool result and the answer of the call', async () => {
    run.resultText = 'MCP_ECHO:'
    await exerciseMcpEcho(context(), '')
    expect(run.turns[0]?.toolCalls).toEqual([{ id: 'mcp-', name: 'mcp__echo_probe__echo', arguments: { value: '' } }])
    expect(run.events).toEqual([`answer ${nativeMcpAnswer('mcp-')} to.be.visible`])
  })

  it('fails when the tool result lacks the echoed value', async () => {
    run.resultText = 'MCP_ECHO:another'
    await expect(exerciseMcpEcho(context(), 'value')).rejects.toThrow('MCP_ECHO:value')
  })

  function writeReceipt(elicitationRequests: unknown[] = []): string {
    const receiptLog = join(run.scratch, 'receipt.json')
    writeFileSync(receiptLog, JSON.stringify({
      initializeCapabilities: {},
      toolCatalogs: [{ id: 1, tools: [{ name: 'echo', inputSchema: { type: 'object' } }] }],
      elicitationRequests,
      elicitationReplies: [],
      toolResults: [{ id: 2, tool: 'echo', text: 'MCP_ECHO:value', isError: false }],
      exchange: [],
    }))
    return receiptLog
  }

  it('requires the call in the receipt of the server', async () => {
    run.resultText = 'MCP_ECHO:value'
    await exerciseMcpEcho(context(), 'value', { receiptLog: writeReceipt() })
  })

  it('fails when the server of the receipt asked for input', async () => {
    run.resultText = 'MCP_ECHO:value'
    await expect(exerciseMcpEcho(context(), 'value', { receiptLog: writeReceipt([{ id: 3, toolRequestId: 2, params: {} }]) })).rejects.toThrow()
  })

  it('fails when the receipt holds another echo result', async () => {
    run.resultText = 'MCP_ECHO:other'
    const receiptLog = writeReceipt()
    await expect(exerciseMcpEcho(context(), 'other', { receiptLog })).rejects.toThrow()
  })
})

describe('withNativeMcpFormAgent', () => {
  it('registers the form server before the agent opens, and restores the exact earlier bytes', async () => {
    mkdirSync(dirname(run.configurationPath), { recursive: true })
    writeFileSync(run.configurationPath, '{"kept":true}')
    const seen: string[] = []
    await withNativeMcpFormAgent(context(), {
      directoryPrefix: 'form-agent-',
      configurationPath: run.configurationPath,
      configuration: server => ({ servers: { [server.name]: { command: server.command, args: server.args } } }),
    }, async ({ server, receiptLog }) => {
      seen.push(server.name, receiptLog)
      run.events.push('use')
    })
    const [name, receiptLog] = seen
    expect(name).toBe(MCP_FORM_SERVER_NAME)
    expect(receiptLog).toMatch(/form-agent-[^/]+\/native-mcp-receipt\.json$/)
    expect(JSON.parse(run.configurationAtOpen ?? 'null')).toEqual({
      servers: { [MCP_FORM_SERVER_NAME]: { command: process.execPath, args: [join(dirname(receiptLog!), 'native-form-server.mjs')] } },
    })
    expect(run.events).toEqual(['open agent CLAUDE_CODE in workspace', 'open workspace workspace', 'preset bypass', 'use'])
    expect(readFileSync(run.configurationPath, 'utf8')).toBe('{"kept":true}')
  })

  it('restores the configuration when the use fails', async () => {
    mkdirSync(dirname(run.configurationPath), { recursive: true })
    writeFileSync(run.configurationPath, 'original')
    await expect(withNativeMcpFormAgent(context(), {
      directoryPrefix: 'form-agent-',
      configurationPath: run.configurationPath,
      configuration: () => ({}),
    }, async () => {
      throw new Error('the use failed')
    })).rejects.toThrow('the use failed')
    expect(readFileSync(run.configurationPath, 'utf8')).toBe('original')
  })

  it('writes the server into the working directory that the rule of the provider creates', async () => {
    const workingDir = vi.fn((prefix: string) => {
      const repository = join(mkdtempSync(join(run.scratch, prefix)), 'repo')
      mkdirSync(repository)
      return repository
    })
    const receipts: string[] = []
    await withNativeMcpFormAgent(context(AgentProvider.CLAUDE_CODE, { ...claude, workingDir }), {
      directoryPrefix: 'form-agent-',
      configurationPath: run.configurationPath,
      configuration: () => ({}),
    }, async ({ receiptLog }) => {
      receipts.push(receiptLog)
    })
    expect(workingDir).toHaveBeenCalledExactlyOnceWith('form-agent-')
    expect(receipts).toEqual([expect.stringMatching(/form-agent-[^/]+\/repo\/native-mcp-receipt\.json$/)])
  })

  it('refuses the agent of another provider before it writes the server or the configuration', async () => {
    mkdirSync(dirname(run.configurationPath), { recursive: true })
    writeFileSync(run.configurationPath, 'original')
    const workingDir = vi.fn(() => run.scratch)
    const use = vi.fn()
    await expect(withNativeMcpFormAgent(context(AgentProvider.CLAUDE_CODE, { provider: AgentProvider.CLINE, prefix: 'cline-e2e', workingDir }), {
      directoryPrefix: 'form-agent-',
      configurationPath: run.configurationPath,
      configuration: () => ({}),
    }, use)).rejects.toThrow(`not by the rule of provider ${AgentProvider.CLINE}`)
    expect(workingDir).not.toHaveBeenCalled()
    expect(use).not.toHaveBeenCalled()
    expect(run.events).toEqual([])
    expect(readFileSync(run.configurationPath, 'utf8')).toBe('original')
  })
})
