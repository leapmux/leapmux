import type { Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ProjectMcpServer } from './nativeWorkspaceTrustLimit'
import type { ProviderWorkingDir } from './providerWorkingDir'
import type { ProviderAgent } from './workspace'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { unitWorkingDir } from '~/test-support/unitWorkingDir'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, ignoredMcpServerProjectConfiguration, instructionFileConfiguration, mcpServerProjectConfiguration, outsideFileWriteOperation, PROJECT_MCP_SERVER_NAME, projectConfigurationWorker } from './nativeWorkspaceTrustLimit'
import { gitRepositoryWorkingDir } from './providerWorkingDir'

const SCRATCH_ROOT = resolve(process.cwd(), '../.tmp')

/** The calls that the mocked browser, permission, and repository helpers receive, in order. */
const calls = vi.hoisted(() => ({
  events: [] as string[],
  request: undefined as unknown,
  proof: undefined as unknown,
  open: vi.fn<typeof import('./api').openAgentViaAPI>(),
}))

/** The private run directory that `createTestDirectory` creates its directories in. */
const run = vi.hoisted(() => ({ tmpDir: '' }))

vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  return { ...actual, expect }
})

vi.mock('./server', async importOriginal => ({
  ...await importOriginal<typeof import('./server')>(),
  getGlobalState: () => ({ tmpDir: run.tmpDir }),
}))

vi.mock('./api', async importOriginal => ({
  ...await importOriginal<typeof import('./api')>(),
  openAgentViaAPI: calls.open,
}))

// `expectNoNativeStartupControl` has its own cases in `./nativeControlObservation.test.ts`. Here it only starts the agent.
vi.mock('./nativeControlObservation', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeControlObservation')>(),
  expectNoNativeStartupControl: async (_context: unknown, options: { start: () => Promise<void> }) => {
    await options.start()
  },
}))

vi.mock('./ui', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./ui')>()
  return {
    ...actual,
    chooseSettingsOption: async (_page: Page, testId: string) => {
      calls.events.push(`choose ${testId}`)
    },
    waitForSettingsIdle: async () => {
      calls.events.push('idle')
    },
  }
})

vi.mock('./nativeConversation', async importOriginal => ({
  ...await importOriginal<typeof import('./nativeConversation')>(),
  sendNativeAnswer: async (_context: unknown, prompt: string) => {
    calls.events.push(`turn ${prompt}`)
    return calls.request
  },
}))

vi.mock('./mcpServerReceipt', async importOriginal => ({
  ...await importOriginal<typeof import('./mcpServerReceipt')>(),
  waitForMcpToolListed: async (receiptLog: string, toolName: string) => {
    calls.events.push(`listed ${toolName} in ${receiptLog}`)
  },
}))

// `ensureGitRepositoryRoot` has its own cases in `./worktree.test.ts`. The real `gitRepositoryWorkingDir` stays.
vi.mock('./worktree', async importOriginal => ({
  ...await importOriginal<typeof import('./worktree')>(),
  ensureGitRepositoryRoot: (directory: string) => {
    calls.events.push(`repository root ${existsSync(join(directory, 'AGENTS.md')) ? 'after' : 'before'} the file`)
  },
}))

// `exerciseUnsupportedControlThroughPermission` has its own cases in `./unsupportedNativeControl.test.ts`.
vi.mock('./unsupportedNativeControl', async importOriginal => ({
  ...await importOriginal<typeof import('./unsupportedNativeControl')>(),
  exerciseUnsupportedControlThroughPermission: async (_context: unknown, options: { purpose: string, classify: unknown, operation?: NativePermissionOperationPlan }) => {
    calls.events.push(`unsupported ${options.purpose}`)
    calls.proof = options
  },
}))

beforeEach(() => {
  calls.events = []
  calls.request = undefined
  calls.proof = undefined
  calls.open.mockReset()
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  run.tmpDir = mkdtempSync(join(SCRATCH_ROOT, 'workspace-trust-run-'))
})

afterEach(() => {
  rmSync(run.tmpDir, { recursive: true, force: true })
})

/** How a Cursor agent opens in these tests: in a fresh directory of the run. */
const CURSOR: ProviderAgent = { provider: AgentProvider.CURSOR, prefix: 'cursor-e2e' }

/** A context whose browser and model script no helper of this file may touch. */
function detachedContext(overrides: Partial<ManagedNativeScenarioContext> = {}): ManagedNativeScenarioContext {
  return {
    provider: AgentProvider.CURSOR,
    providerAgent: CURSOR,
    workspaceId: 'detached',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
    page: {} as Page,
    modelScript: {} as ModelScript,
    ...overrides,
  }
}

function scratchDirectory(directories: string[], prefix: string): ProviderWorkingDir {
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  const directory = unitWorkingDir(mkdtempSync(join(SCRATCH_ROOT, prefix)))
  directories.push(directory)
  return directory
}

describe('exerciseNativeWorkspaceTrustLimit', () => {
  it.each([undefined, {}, { projectConfiguration: {} }, { projectConfiguration: { prepare() {} } }])('rejects a missing actual configuration proof before browser access: %j', async (options) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CURSOR,
      providerAgent: CURSOR,
      workspaceId: 'config-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The configuration boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The configuration boundary must run before model access.')
      },
    }
    await expect(Reflect.apply(exerciseNativeWorkspaceTrustLimit, undefined, [context, options])).rejects.toThrow('requires an actual native project configuration proof')
  })

  it.each([
    { startup: 'other' },
    { startup: 'failed' },
    { startup: 'failed', startupError: '' },
    { startup: 'failed', startupError: ' \n\t' },
  ])('rejects an invalid failure proof before configuration or browser access: %j', async (startup) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CURSOR,
      providerAgent: CURSOR,
      workspaceId: 'failed-config-boundary',
      leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
      get page(): Page {
        throw new Error('The failure boundary must run before browser access.')
      },
      get modelScript(): ModelScript {
        throw new Error('The failure boundary must run before model access.')
      },
    }
    const options = {
      ...startup,
      projectConfiguration: {
        prepare() { throw new Error('The failure boundary must run before configuration writes.') },
        async prove() { throw new Error('The failure boundary must run before configuration proof.') },
      },
    }
    await expect(Reflect.apply(exerciseNativeWorkspaceTrustLimit, undefined, [context, options])).rejects.toThrow(/workspace trust startup|requires the native configuration error/)
  })

  /** The rest of the scenario drives a browser, so the open of the agent ends it in these tests. */
  const opened = new Error('The unit test ends the trust scenario at the agent open.')

  /** Run the scenario up to the agent open, and return the project directory that `prepare` received. */
  async function preparedProject(context: ManagedNativeScenarioContext): Promise<string> {
    calls.open.mockRejectedValue(opened)
    const prepared: string[] = []
    await expect(exerciseNativeWorkspaceTrustLimit(context, {
      projectConfiguration: {
        prepare: ({ directory }) => { prepared.push(directory) },
        prove: async () => { throw new Error('The agent open ends the scenario before the proof.') },
      },
    })).rejects.toBe(opened)
    expect(prepared).toHaveLength(1)
    const [directory] = prepared
    if (directory === undefined)
      throw new Error('The scenario prepared no project directory.')
    expect(calls.open).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' }), 'detached', directory, agentOpenOptions(context.provider, {}))
    return directory
  }

  it('prepares and opens the project of a provider with a git rule in the root of a git repository of its own', async () => {
    const deepseek: ProviderAgent = { provider: AgentProvider.DEEPSEEK_HARNESS, prefix: 'deepseek-harness-e2e', workingDir: gitRepositoryWorkingDir }
    const directory = await preparedProject(detachedContext({ provider: deepseek.provider, providerAgent: deepseek }))
    expect(basename(dirname(directory))).toMatch(/^native-workspace-trust-/)
    // The run directory sits inside the LeapMux checkout, so a plain directory there reports the checkout as its top.
    expect(execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: directory, encoding: 'utf8' }).trim()).toBe(realpathSync(directory))
  })

  it('prepares and opens the project of a provider with no rule in a fresh directory of the run', async () => {
    const directory = await preparedProject(detachedContext())
    expect(dirname(directory)).toBe(run.tmpDir)
    expect(basename(directory)).toMatch(/^native-workspace-trust-/)
    expect(existsSync(join(directory, '.git'))).toBe(false)
  })
})

describe('projectConfigurationWorker', () => {
  // The provider's `nativeLaunch` resolves the executable; `resolveNativeStartupLaunch` has the tests of that lookup.
  const launch = { binaryName: 'native-agent', executable: '/private/native-agent', holdWhen: ['acp'] }

  it('starts the stated launch and turns the project configuration back on', () => {
    const worker = projectConfigurationWorker({ NATIVE_DISABLE_PROJECT_CONFIG: 'true' }, launch, 'NATIVE_DISABLE_PROJECT_CONFIG')
    expect(worker.launch).toBe(launch)
    expect(Reflect.apply(worker.workerEnvironment, undefined, [])).toEqual({ NATIVE_DISABLE_PROJECT_CONFIG: 'false' })
  })

  it.each([
    { label: 'an absent environment', environment: undefined },
    { label: 'a variable the environment does not set', environment: { NATIVE_DISABLE_PROJECT_CONFIG_TYPO: 'true' } },
    { label: 'a variable that already loads project configuration', environment: { NATIVE_DISABLE_PROJECT_CONFIG: 'false' } },
    { label: 'a variable set to 1', environment: { NATIVE_DISABLE_PROJECT_CONFIG: '1' } },
  ])('refuses $label', ({ environment }) => {
    expect(() => projectConfigurationWorker(environment, launch, 'NATIVE_DISABLE_PROJECT_CONFIG')).toThrow('NATIVE_DISABLE_PROJECT_CONFIG')
  })
})

describe('instructionFileConfiguration', () => {
  const directories: string[] = []
  afterEach(() => {
    for (const directory of directories.splice(0))
      rmSync(directory, { recursive: true, force: true })
  })

  function instructionRequest(system: string, assistant = ''): MockModelRequestRecord {
    return {
      protocol: 'anthropic-messages',
      path: '/v1/messages',
      body: { system, messages: [{ role: 'user', content: 'Reply once.' }, { role: 'assistant', content: assistant }] },
    }
  }

  it('writes the marker into the file and proves it through the instruction reader', async () => {
    const directory = scratchDirectory(directories, 'instruction-file-')
    const configuration = instructionFileConfiguration('.goosehints')
    await configuration.prepare({ directory, marker: 'INSTRUCTIONMARKER' })
    expect(readFileSync(join(directory, '.goosehints'), 'utf8')).toContain('INSTRUCTIONMARKER')
    calls.request = instructionRequest('Keep INSTRUCTIONMARKER as a standing project instruction.')
    await configuration.prove(detachedContext(), { directory, marker: 'INSTRUCTIONMARKER', agentId: 'agent' })
    expect(calls.events).toEqual(['turn Reply once after native project configuration loads.'])
  })

  it('refuses a request that holds the marker outside the instructions and the user text', async () => {
    const directory = scratchDirectory(directories, 'instruction-file-')
    calls.request = instructionRequest('No project instruction.', 'INSTRUCTIONMARKER')
    await expect(instructionFileConfiguration('AGENTS.md').prove(detachedContext(), { directory, marker: 'INSTRUCTIONMARKER', agentId: 'agent' })).rejects.toThrow('INSTRUCTIONMARKER')
  })

  it('reads through the context reader of the provider when the provider sets one', async () => {
    const directory = scratchDirectory(directories, 'instruction-file-')
    // The body holds no instruction, so the generic reader would throw.
    calls.request = { protocol: 'anthropic-messages', path: '/v1/messages', body: {} } satisfies MockModelRequestRecord
    const context = detachedContext({ readModelContext: () => 'server-held INSTRUCTIONMARKER' })
    await instructionFileConfiguration('AGENTS.md').prove(context, { directory, marker: 'INSTRUCTIONMARKER', agentId: 'agent' })
    const absent = detachedContext({ readModelContext: () => 'server-held context without the marker' })
    await expect(instructionFileConfiguration('AGENTS.md').prove(absent, { directory, marker: 'INSTRUCTIONMARKER', agentId: 'agent' })).rejects.toThrow('INSTRUCTIONMARKER')
  })

  it('makes the repository before it writes the file, and requires the source line', async () => {
    const directory = scratchDirectory(directories, 'instruction-file-')
    const configuration = instructionFileConfiguration('AGENTS.md', { gitRoot: true, sourceLine: path => `Instructions from: ${path}` })
    await configuration.prepare({ directory, marker: 'INSTRUCTIONMARKER' })
    expect(calls.events).toEqual(['repository root before the file'])
    expect(existsSync(join(directory, 'AGENTS.md'))).toBe(true)
    const project = { directory, marker: 'INSTRUCTIONMARKER', agentId: 'agent' }
    calls.request = instructionRequest('Keep INSTRUCTIONMARKER as a standing project instruction.')
    await expect(configuration.prove(detachedContext(), project)).rejects.toThrow('Instructions from:')
    calls.request = instructionRequest(`Instructions from: ${join(directory, 'AGENTS.md')}\nKeep INSTRUCTIONMARKER as a standing project instruction.`)
    await configuration.prove(detachedContext(), project)
  })

  it('writes no repository without the option', async () => {
    const directory = scratchDirectory(directories, 'instruction-file-')
    await instructionFileConfiguration('GEMINI.md').prepare({ directory, marker: 'INSTRUCTIONMARKER' })
    expect(calls.events).toEqual([])
  })

  it.each(['', '.', '..', 'docs/AGENTS.md', '../AGENTS.md'])('refuses the file name %j, which is not one file name component', (fileName) => {
    expect(() => instructionFileConfiguration(fileName)).toThrow('one file name component')
  })
})

describe('mcpServerProjectConfiguration', () => {
  const directories: string[] = []
  afterEach(() => {
    for (const directory of directories.splice(0))
      rmSync(directory, { recursive: true, force: true })
  })

  it('makes the repository, writes the echo server, and passes its launch to the provider configuration', async () => {
    const directory = scratchDirectory(directories, 'mcp-project-')
    const servers: ProjectMcpServer[] = []
    await mcpServerProjectConfiguration((dir, server) => {
      expect(dir).toBe(directory)
      servers.push(server)
    }).prepare({ directory, marker: 'UNUSED' })
    expect(calls.events).toEqual(['repository root before the file'])
    expect(servers).toEqual([{ name: PROJECT_MCP_SERVER_NAME, command: process.execPath, args: [join(directory, 'mcp-echo.mjs')] }])
    expect(existsSync(servers[0]!.args[0]!)).toBe(true)
  })

  it('proves the configuration through one native turn and the echo tool that the server lists', async () => {
    const directory = scratchDirectory(directories, 'mcp-project-')
    await mcpServerProjectConfiguration(() => {}).prove(detachedContext(), { directory, marker: 'UNUSED', agentId: 'agent' })
    expect(calls.events).toEqual([
      'turn Return one native response from this scratch project.',
      `listed echo in ${join(directory, 'workspace-mcp-receipt.json')}`,
    ])
  })
})

describe('ignoredMcpServerProjectConfiguration', () => {
  const directories: string[] = []
  afterEach(() => {
    for (const directory of directories.splice(0))
      rmSync(directory, { recursive: true, force: true })
  })

  /** A model request whose tool catalog offers the tools of `names`. */
  function toolRequest(...names: string[]): MockModelRequestRecord {
    return {
      protocol: 'openai-chat-completions',
      path: '/v1/chat/completions',
      body: { messages: [{ role: 'user', content: 'Reply once.' }], tools: names.map(name => ({ type: 'function', function: { name, description: '', parameters: {} } })) },
    }
  }

  it('prepares the project as the loaded configuration does', async () => {
    const directory = scratchDirectory(directories, 'mcp-project-')
    const servers: ProjectMcpServer[] = []
    await ignoredMcpServerProjectConfiguration((dir, server) => {
      expect(dir).toBe(directory)
      servers.push(server)
    }).prepare({ directory, marker: 'UNUSED' })
    expect(calls.events).toEqual(['repository root before the file'])
    expect(servers).toEqual([{ name: PROJECT_MCP_SERVER_NAME, command: process.execPath, args: [join(directory, 'mcp-echo.mjs')] }])
  })

  it('proves through one native turn that the request offers no project tool and the server wrote no receipt', async () => {
    const directory = scratchDirectory(directories, 'mcp-project-')
    calls.request = toolRequest('read_file', 'echo_probe__echo')
    await ignoredMcpServerProjectConfiguration(() => {}).prove(detachedContext(), { directory, marker: 'UNUSED', agentId: 'agent' })
    expect(calls.events).toEqual(['turn Return one native response from this scratch project.'])
  })

  it('fails when the model request offers a tool of the project server', async () => {
    const directory = scratchDirectory(directories, 'mcp-project-')
    calls.request = toolRequest('read_file', `${PROJECT_MCP_SERVER_NAME}__echo`)
    await expect(ignoredMcpServerProjectConfiguration(() => {}).prove(detachedContext(), { directory, marker: 'UNUSED', agentId: 'agent' }))
      .rejects
      .toThrow('the model request offers no tool of the project server')
  })

  it('fails when the project server wrote its receipt', async () => {
    const directory = scratchDirectory(directories, 'mcp-project-')
    writeFileSync(join(directory, 'workspace-mcp-receipt.json'), '{}\n')
    calls.request = toolRequest('read_file')
    await expect(ignoredMcpServerProjectConfiguration(() => {}).prove(detachedContext(), { directory, marker: 'UNUSED', agentId: 'agent' }))
      .rejects
      .toThrow('the project server wrote no receipt')
  })
})

describe('exerciseMissingWorkspaceTrustRoute', () => {
  const classify = () => null

  it('applies the ask option, then proves the default operation of the workspace-trust purpose under the route check', async () => {
    await exerciseMissingWorkspaceTrustRoute(detachedContext(), { askOption: 'permissionMode-ask', classify })
    expect(calls.events).toEqual(['choose permissionMode-ask', 'idle', 'unsupported workspace-trust'])
    expect(calls.proof).toEqual({ purpose: 'workspace-trust', classify })
  })

  it('prepares a stated operation after the ask option, and hands it to the proof', async () => {
    const operation: NativePermissionOperationPlan = {
      toolCall: { id: 'own-call', name: 'Bash', arguments: {} },
      beforeDecision: () => {},
      nativeProof: () => {},
    }
    await exerciseMissingWorkspaceTrustRoute(detachedContext(), {
      askOption: 'permissionMode-ask',
      classify,
      operation: () => {
        calls.events.push('own operation')
        return operation
      },
    })
    expect(calls.events).toEqual(['choose permissionMode-ask', 'idle', 'own operation', 'unsupported workspace-trust'])
    expect(calls.proof).toEqual({ purpose: 'workspace-trust', classify, operation })
  })

  it('refuses an empty ask option before any browser step', async () => {
    await expect(exerciseMissingWorkspaceTrustRoute(detachedContext(), { askOption: '', classify })).rejects.toThrow('the option under which the provider asks')
    expect(calls.events).toEqual([])
  })
})

describe('outsideFileWriteOperation', () => {
  it('writes outside the working directory through the shell tool call of the provider', async () => {
    const operation = outsideFileWriteOperation((callId, command) => ({ id: callId, name: 'Bash', arguments: { command } }))
    expect(operation.toolCall.id).toBe('native-control-permission')
    const command = operation.toolCall.arguments?.command
    if (typeof command !== 'string')
      throw new Error('The operation states no shell command.')
    const file = /> '([^']+)'/.exec(command)?.[1]
    if (!file)
      throw new Error('The command writes no quoted file.')
    expect(file.endsWith('/native-control.txt')).toBe(true)
    await operation.beforeDecision()
    writeFileSync(file, 'NATIVECONTROL42\n')
    // The file exists, so the check before the decision fails.
    await expect(Promise.resolve().then(() => operation.beforeDecision())).rejects.toThrow('expected true to be false')
    const request: MockModelRequestRecord = {
      protocol: 'anthropic-messages',
      path: '/v1/messages',
      body: { messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'native-control-permission', content: 'NATIVECONTROL42' }] }] },
    }
    await operation.nativeProof(request)
    writeFileSync(file, 'NATIVECONTROL41\n')
    // The file no longer holds the computed bytes, so the check of its content fails.
    await expect(Promise.resolve().then(() => operation.nativeProof(request))).rejects.toThrow(/expected 'NATIVECONTROL41[^']*' to be 'NATIVECONTROL42/)
  })
})
