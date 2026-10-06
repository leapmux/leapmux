import type { Locator, Page } from '@playwright/test'
import type { MockModelRequestRecord } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import type { NativePermissionOperationPlan } from './nativePermission'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMissingWorkspaceTrustRoute, exerciseNativeWorkspaceTrustLimit, expectMcpServerLoaded, instructionFileConfiguration, outsideFileWriteOperation, projectConfigurationWorker } from './nativeWorkspaceTrustLimit'

const SCRATCH_ROOT = resolve(process.cwd(), '../.tmp')

/** The calls that the mocked browser, permission, and repository helpers receive, in order. */
const calls = vi.hoisted(() => ({ events: [] as string[], request: undefined as unknown, decision: undefined as unknown, classify: undefined as unknown }))

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

vi.mock('./worktree', async importOriginal => ({
  ...await importOriginal<typeof import('./worktree')>(),
  createGitRepo: (directory: string, name: string) => {
    calls.events.push(`repository ${name} ${existsSync(join(directory, 'AGENTS.md')) ? 'after' : 'before'} the file`)
    return join(directory, name)
  },
}))

vi.mock('./nativePermission', async importOriginal => ({
  ...await importOriginal<typeof import('./nativePermission')>(),
  createNativePermissionFileWrite: async (_context: unknown, options: { fileName: string }): Promise<NativePermissionOperationPlan> => {
    calls.events.push(`default operation ${options.fileName}`)
    return {
      toolCall: { id: 'default-call', name: 'Bash', arguments: {} },
      beforeDecision: () => {
        calls.events.push('default before')
      },
      nativeProof: () => {
        calls.events.push('default proof')
      },
    }
  },
  exerciseNativePermissionDecision: async (_context: unknown, options: { beforeDecision: (banner: Locator) => Promise<void>, nativeProof: (request: MockModelRequestRecord) => Promise<void> | void }) => {
    calls.decision = options
    calls.events.push('decision')
    await options.beforeDecision({} as Locator)
    await options.nativeProof({} as MockModelRequestRecord)
  },
}))

vi.mock('./unsupportedNativeControl', async importOriginal => ({
  ...await importOriginal<typeof import('./unsupportedNativeControl')>(),
  exerciseUnsupportedNativeControl: async (_context: unknown, options: { purpose: string, classify: unknown, relatedProof: (beforeDecision: (banner: Locator) => Promise<void>) => Promise<void> }) => {
    calls.events.push(`unsupported ${options.purpose}`)
    calls.classify = options.classify
    await options.relatedProof(async () => {
      calls.events.push('route check')
    })
  },
}))

beforeEach(() => {
  calls.events = []
  calls.request = undefined
  calls.decision = undefined
  calls.classify = undefined
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  run.tmpDir = mkdtempSync(join(SCRATCH_ROOT, 'workspace-trust-run-'))
})

afterEach(() => {
  rmSync(run.tmpDir, { recursive: true, force: true })
})

/** A context whose browser and model script no helper of this file may touch. */
function detachedContext(overrides: Partial<ManagedNativeScenarioContext> = {}): ManagedNativeScenarioContext {
  return {
    provider: AgentProvider.CURSOR,
    workspaceId: 'detached',
    leapmuxServer: { hubUrl: 'http://unused.invalid', adminToken: 'unused', workerId: 'unused' },
    page: {} as Page,
    modelScript: {} as ModelScript,
    ...overrides,
  }
}

function scratchDirectory(directories: string[], prefix: string): string {
  mkdirSync(SCRATCH_ROOT, { recursive: true })
  const directory = mkdtempSync(join(SCRATCH_ROOT, prefix))
  directories.push(directory)
  return directory
}

describe('exerciseNativeWorkspaceTrustLimit', () => {
  it.each([undefined, {}, { projectConfiguration: {} }, { projectConfiguration: { prepare() {} } }])('rejects a missing actual configuration proof before browser access: %j', async (options) => {
    const context: ManagedNativeScenarioContext = {
      provider: AgentProvider.CURSOR,
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
    expect(calls.events).toEqual(['repository . before the file'])
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

describe('expectMcpServerLoaded', () => {
  const directories: string[] = []
  afterEach(() => {
    for (const directory of directories.splice(0))
      rmSync(directory, { recursive: true, force: true })
  })

  function writeReceipt(receipt: { initializeCapabilities: Record<string, unknown> | null, tools: string[] }): string {
    const receiptLog = join(scratchDirectory(directories, 'mcp-loaded-'), 'receipt.json')
    writeFileSync(receiptLog, JSON.stringify({
      initializeCapabilities: receipt.initializeCapabilities,
      toolCatalogs: [{ id: 1, tools: receipt.tools.map(name => ({ name, inputSchema: { type: 'object' } })) }],
      elicitationRequests: [],
      elicitationReplies: [],
      toolResults: [],
      // The receipt runtime records the exchange of every server, so a real receipt always holds the field.
      exchange: [],
    }))
    return receiptLog
  }

  it('accepts a server that answered initialize and listed the tool', async () => {
    await expectMcpServerLoaded(writeReceipt({ initializeCapabilities: { tools: {} }, tools: ['echo'] }))
    await expectMcpServerLoaded(writeReceipt({ initializeCapabilities: {}, tools: ['ask', 'probe'] }), 'probe')
  })

  it('refuses a server that listed no such tool', async () => {
    await expect(expectMcpServerLoaded(writeReceipt({ initializeCapabilities: {}, tools: ['ask'] }))).rejects.toThrow('lists echo')
  })

  it('refuses a server that never answered initialize', async () => {
    await expect(expectMcpServerLoaded(writeReceipt({ initializeCapabilities: null, tools: ['echo'] }))).rejects.toThrow()
  })

  it('refuses an absent receipt', async () => {
    await expect(expectMcpServerLoaded(join(scratchDirectory(directories, 'mcp-loaded-'), 'absent.json'))).rejects.toThrow('lists echo')
  })

  it('refuses an empty tool name', async () => {
    await expect(expectMcpServerLoaded(writeReceipt({ initializeCapabilities: {}, tools: ['echo'] }), '')).rejects.toThrow('name of a tool')
  })
})

describe('exerciseMissingWorkspaceTrustRoute', () => {
  const classify = () => null

  it('applies the ask option before it prepares the default operation, and allows that operation under the route check', async () => {
    await exerciseMissingWorkspaceTrustRoute(detachedContext(), { askOption: 'permissionMode-ask', classify })
    expect(calls.events).toEqual([
      'choose permissionMode-ask',
      'idle',
      'default operation native-workspace-trust-control.txt',
      'unsupported workspace-trust',
      'decision',
      'default before',
      'route check',
      'default proof',
    ])
    expect(calls.decision).toMatchObject({ decision: 'allow', toolCall: { id: 'default-call' } })
    expect(calls.decision).not.toHaveProperty('outputGate')
    expect(calls.classify).toBe(classify)
  })

  it('prepares a stated operation after the ask option, and passes its output gate', async () => {
    const outputGate = { gate: {}, shown: async () => {} } as unknown as NonNullable<NativePermissionOperationPlan['outputGate']>
    await exerciseMissingWorkspaceTrustRoute(detachedContext(), {
      askOption: 'permissionMode-ask',
      classify,
      operation: () => {
        calls.events.push('own operation')
        return {
          toolCall: { id: 'own-call', name: 'Bash', arguments: {} },
          outputGate,
          beforeDecision: () => {
            calls.events.push('own before')
          },
          nativeProof: () => {
            calls.events.push('own proof')
          },
        }
      },
    })
    expect(calls.events).toEqual(['choose permissionMode-ask', 'idle', 'own operation', 'unsupported workspace-trust', 'decision', 'own before', 'route check', 'own proof'])
    expect(calls.decision).toMatchObject({ decision: 'allow', toolCall: { id: 'own-call' } })
    expect((calls.decision as { outputGate?: unknown }).outputGate).toBe(outputGate)
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
    await expect(Promise.resolve().then(() => operation.beforeDecision())).rejects.toThrow()
    const request: MockModelRequestRecord = {
      protocol: 'anthropic-messages',
      path: '/v1/messages',
      body: { messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'native-control-permission', content: 'NATIVECONTROL42' }] }] },
    }
    await operation.nativeProof(request)
    writeFileSync(file, 'NATIVECONTROL41\n')
    await expect(Promise.resolve().then(() => operation.nativeProof(request))).rejects.toThrow()
  })
})
