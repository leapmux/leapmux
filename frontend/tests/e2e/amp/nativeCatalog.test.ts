import type { Locator, Page } from '@playwright/test'
import type { MockModelScenarioStatus } from '../helpers/mockModelScript'
import type { ModelScript } from '../helpers/modelScriptFixture'
import type { NativeControlFrame } from '../helpers/nativeControlWatch'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { AmpCatalogCommand } from './nativeCatalog'
import { execFile } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider, AgentStatus, ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { ampToolUseID } from '../helpers/ampSurface'
import { ampCatalogCommandTimeout, ampCatalogPermission, ampCatalogProcess, ampExecutorToolNames, ampPidReceipt, ampSettingsPath, ampWorkerDataDirectory, ampWorkspaceMcpAwaitingApproval, ampWorkspaceMcpConfiguration, assertAmpGeneratedSettings, readAmpExecutorCatalog } from './nativeCatalog'

const calls = vi.hoisted(() => ({ processes: vi.fn(), executable: vi.fn(), current: vi.fn(), catalog: vi.fn<AmpCatalogCommand>(), send: vi.fn(), idle: vi.fn(), watch: vi.fn(), cancelWatch: vi.fn(), channel: vi.fn(), nativeAgent: vi.fn(), state: { binaryPath: '', dataDir: '', tmpDir: '', ampPath: '' } }))
vi.mock('../helpers/processTree', async importOriginal => ({ ...await importOriginal<typeof import('../helpers/processTree')>(), listProcesses: calls.processes }))
vi.mock('../helpers/processExecutable', () => ({ processExecutable: calls.executable }))
vi.mock('../helpers/nativeScenario', async importOriginal => ({ ...await importOriginal<typeof import('../helpers/nativeScenario')>(), currentNativeAgent: calls.current, nativeAgentById: calls.nativeAgent }))
vi.mock('../helpers/binaryOnPath', () => ({ lookupBinary: () => ({ path: calls.state.ampPath, skipReason: null }) }))
vi.mock('../helpers/server', () => ({ getGlobalState: () => calls.state, hubSpawnEnv: (environment: NodeJS.ProcessEnv) => ({ ...environment }) }))
vi.mock('../helpers/ui', () => ({ sendMessage: calls.send, waitForAgentIdle: calls.idle }))
vi.mock('../helpers/nativeControlWatch', () => ({ watchNativeControls: calls.watch }))
vi.mock('../helpers/api', async importOriginal => ({ ...await importOriginal<typeof import('../helpers/api')>(), getTestChannel: calls.channel }))
vi.mock('@playwright/test', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@playwright/test')>()
  const onePoll = new Proxy(actual.expect, {
    get: (target, property) => property === 'poll'
      ? (read: () => unknown | Promise<unknown>) => ({
          toBe: async (expected: unknown) => expect(await read()).toBe(expected),
          not: { toBe: async (expected: unknown) => expect(await read()).not.toBe(expected) },
        })
      : Reflect.get(target, property),
  })
  return { ...actual, expect: onePoll }
})

function guardedHandle<T extends object>(methods: Partial<T>): T {
  const target = methods as T
  return new Proxy(target, {
    get: (value, property, receiver) => {
      if (property in value)
        return Reflect.get(value, property, receiver)
      // Matcher inspection checks optional symbols. Runtime method reads must still fail.
      if (typeof property === 'symbol')
        return undefined
      throw new Error(`The Amp unit accessed an unimplemented handle property: ${String(property)}.`)
    },
  })
}

let directory = ''
const extraDirectories: string[] = []
function processFixture() {
  const scratch = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'amp-catalog-regression-'))
  const settingsPath = join(directory, 'settings.json')
  const workerDataDir = join(directory, 'worker')
  mkdirSync(workerDataDir)
  calls.state.binaryPath = join(directory, 'leapmux')
  calls.state.ampPath = join(directory, 'amp')
  calls.state.dataDir = workerDataDir
  calls.state.tmpDir = directory
  writeFileSync(calls.state.binaryPath, '')
  writeFileSync(calls.state.ampPath, '')
  writeFileSync(settingsPath, JSON.stringify({ 'amp.mcpServers': { credential_probe: { command: 'private-runtime', args: ['private-echo-server'] } } }))
  const worker = { pid: 10, ppid: 1, command: `"${calls.state.binaryPath}" worker --data-dir "${workerDataDir}"`, executable: calls.state.binaryPath }
  const amp = { pid: 20, ppid: 10, command: `"${calls.state.ampPath}" --execute --stream-json --stream-json-input --settings-file "${settingsPath}" --no-ide --no-color`, executable: calls.state.ampPath }
  const tool = { pid: 30, ppid: 20, command: 'private-runtime held-native-tool' }
  return { settingsPath, worker, amp, tool, proof: { toolPid: 30, toolParentPid: 20, workerExecutable: calls.state.binaryPath, workerDataDir, ampExecutables: [calls.state.ampPath] } }
}
beforeEach(() => {
  vi.resetAllMocks()
})
afterEach(() => {
  vi.restoreAllMocks()
  for (const extra of extraDirectories.splice(0))
    rmSync(extra, { recursive: true, force: true })
  if (directory) {
    rmSync(directory, { recursive: true, force: true })
    directory = ''
  }
})

function catalogFixture(options: {
  permission?: 'ask' | 'allow_all'
  wrongCall?: boolean
  wrongCommand?: boolean
  approvalFailure?: Error
  delayedSession?: boolean
  siblingControls?: boolean
  editorOutsideTile?: boolean
} = {}) {
  const clock = vi.spyOn(Date, 'now').mockReturnValue(1_000_000)
  const { settingsPath, worker, amp, tool } = processFixture()
  calls.executable.mockImplementation(async (pid: number) => {
    if (pid !== amp.pid)
      throw new Error('The unit executable query received an unrelated PID.')
    return calls.state.ampPath
  })
  // The same Amp PID exists before the next scripted turn and stays under the exact Worker.
  calls.processes.mockImplementation(() => calls.send.mock.calls.length === 0 ? [worker, amp] : [worker, amp, tool])
  const liveAgent = create(AgentInfoSchema, {
    id: 'actual-agent',
    agentProvider: AgentProvider.AMP,
    status: AgentStatus.ACTIVE,
    agentSessionId: 'T-actual-session',
    workingDir: directory,
    optionGroups: [{ id: 'agent_mode', currentValue: 'smart' }, { id: 'permissionMode', currentValue: options.permission ?? 'allow_all' }],
  })
  calls.current.mockResolvedValue(liveAgent)
  if (options.delayedSession)
    calls.current.mockResolvedValueOnce(create(AgentInfoSchema, { ...liveAgent, agentSessionId: '' }))
  calls.catalog.mockResolvedValue({ stdout: JSON.stringify([{ name: 'read_file', source: 'built-in' }]), stderr: '' })
  let currentCallId = ''
  let pidFile = ''
  let releaseFile = ''
  const controls: NativeControlFrame[] = []
  let createPid = () => {}
  const approve = vi.fn(() => {
    expect(existsSync(pidFile)).toBe(false)
    if (options.approvalFailure)
      throw options.approvalFailure
    createPid()
    return true
  })
  // This opaque locator supports the one approval callback that reads actual buttons.
  // Playwright's generic method also admits element types that this fixture never supplies.
  const evaluateApproval = async (read: string | ((elements: HTMLButtonElement[], value: unknown) => unknown | Promise<unknown>), value?: unknown) => {
    if (typeof read !== 'function')
      throw new Error('The native approval unit requires an actual browser function.')
    const button = document.createElement('button')
    Object.defineProperty(button, 'click', { value: () => approve() })
    const elements = [button]
    document.body.append(button)
    Object.defineProperty(button, 'getClientRects', { value: () => [new DOMRect(0, 0, 10, 10)] })
    try {
      return await read(elements, value)
    }
    finally {
      button.remove()
    }
  }
  const allow: Locator = guardedHandle<Locator>({
    first: () => allow,
    evaluateAll: evaluateApproval as Locator['evaluateAll'],
    count: async () => 1,
  })
  const absent: Locator = guardedHandle<Locator>({
    evaluateAll: (async (read: (elements: Element[]) => unknown) => read([])) as Locator['evaluateAll'],
    count: async () => 0,
  })
  const outsideTile = document.createElement('div')
  outsideTile.innerHTML = '<div data-testid="tile"><div data-testid="tab" data-tab-id="actual-agent" aria-selected="true"></div></div><div data-testid="agent-editor-panel" data-agent-id="actual-agent"><fieldset data-testid="control-actions"><button data-testid="control-allow-btn"></button></fieldset></div>'
  const page = guardedHandle<Page>({ locator: (selector) => {
    if (options.editorOutsideTile) {
      const matched = outsideTile.querySelectorAll(selector.replaceAll(':visible', ''))
      if (matched.length !== 1)
        return absent
    }
    if (options.siblingControls && (!selector.includes('control-actions') || !selector.includes('data-agent-id="actual-agent"') || selector.includes('control-banner')))
      return absent
    return allow
  } })
  calls.watch.mockResolvedValue({ controls: () => controls, cancel: calls.cancelWatch })
  const workerClose = vi.fn(async () => ({ result: { failureMessage: '', failureDetail: '' } }))
  calls.channel.mockResolvedValue({ callWorker: workerClose })
  calls.nativeAgent.mockResolvedValue(liveAgent)
  workerClose.mockImplementation(async () => {
    calls.nativeAgent.mockResolvedValue(null)
    return { result: { failureMessage: '', failureDetail: '' } }
  })
  const status = (stepCount: number): MockModelScenarioStatus => ({
    complete: stepCount === 2,
    nextStep: stepCount,
    stepCount,
    ruleMatches: {},
    pendingGates: [],
    requests: stepCount === 2
      ? [{
          protocol: 'anthropic-messages',
          path: '/api/amp-native-receipt',
          stepIndex: 1,
          body: { messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: ampToolUseID('T-actual-session', currentCallId), content: JSON.stringify({ status: 'completed', result: JSON.stringify({ stdout: 'AMP_CATALOG_PID:30\n', stderr: '', exitCode: 0 }) }) }] }] },
        }]
      : [],
    unexpectedRequests: [],
  })
  const unused = (): never => {
    throw new Error('The Amp catalog test called an unused model method.')
  }
  const queue = vi.fn<ModelScript['queue']>().mockResolvedValue(0)
  const steps = vi.fn<ModelScript['waitForSteps']>().mockImplementation(async count => status(count ?? 2))
  const release = vi.fn<ModelScript['releaseGateIfHeld']>().mockResolvedValue(false)
  const script: ModelScript = {
    id: 'amp-catalog-regression',
    testDeadline: () => 1_060_000,
    status: vi.fn<ModelScript['status']>().mockResolvedValue(status(0)),
    queue,
    requestAt: unused,
    rule: unused,
    fallback: unused,
    waitForGate: vi.fn<ModelScript['waitForGate']>().mockResolvedValue(status(1)),
    waitForSteps: steps,
    releaseGateIfHeld: release,
    releaseGate: unused,
    allowUnconsumed: unused,
    prompt: (value: string) => value,
  }
  // Browser interaction supplies no process evidence. The actual reader uses the process and settings adapters above.
  const context: ManagedNativeScenarioContext = {
    page,
    modelScript: script,
    provider: AgentProvider.AMP,
    workspaceId: 'actual-workspace',
    leapmuxServer: { hubUrl: 'http://unit.invalid', adminToken: 'unit-token', workerId: 'actual-worker', agentEnv: { HOME: directory, AMP_URL: 'http://unit.invalid', AMP_API_KEY: 'private-test-key' } },
  }
  calls.send.mockImplementation(async () => {
    const call = queue.mock.calls.at(-1)?.[0]?.toolCalls?.[0]
    const input = call?.arguments
    const command = isObject(input) ? input.command : undefined
    if (!call || typeof command !== 'string')
      throw new Error('The Amp catalog fixture received no actual central shell command.')
    const basename = /amp-catalog-[A-Za-z0-9-]+\.pid/.exec(command)?.[0]
    if (!basename)
      throw new Error('The actual Amp catalog command contains no PID receipt file.')
    currentCallId = call.id
    pidFile = join(directory, basename)
    releaseFile = join(directory, `${basename.slice(0, -4)}.release`)
    createPid = () => writeFileSync(pidFile, JSON.stringify({ pid: 30, ppid: 20, workingDir: directory, home: context.leapmuxServer.agentEnv?.HOME, endpoint: 'http://unit.invalid' }))
    if (options.permission === 'ask') {
      controls.push({ requestId: 'native-permission', responseState: ControlResponseState.READY, payload: {
        type: 'leapmux_amp_permission',
        tool_name: 'shell_command',
        tool_use_id: ampToolUseID('T-actual-session', options.wrongCall ? 'other-call' : currentCallId),
        input: { command: options.wrongCommand ? 'another command' : command },
      } })
    }
    else {
      createPid()
    }
  })
  const files = () => {
    if (!pidFile || !releaseFile)
      throw new Error('The Amp catalog fixture contains no actual queued receipt paths.')
    return { pidFile, releaseFile }
  }
  return { context, steps, status, files, clock, settingsPath, worker, amp, tool, approve, controls, workerClose }
}

describe('ampSettingsPath', () => {
  it.each([
    '/private/run/settings.json',
    '/private/run with spaces/settings.json',
    '"/private/run with spaces/settings.json"',
    '\'/private/run with spaces/settings.json\'',
  ])('preserves the generated settings path: %s', (path) => {
    expect(ampSettingsPath(`amp --execute --settings-file ${path} --no-ide --no-color`)).toBe(path.replace(/^["']|["']$/g, ''))
  })

  it.each(['', 'amp --settings-file', 'amp --settings-file relative/settings.json --no-ide', 'amp --settings-file /private/other.json --no-ide', 'amp --settings-file /private/a/settings.json --settings-file /private/b/settings.json --no-ide', 'amp --settings-file-more /private/settings.json --no-ide', 'amp --settings-file /private/settings.json --no-ide-more', 'amp --settings-file /private/settings.json --no-ide --no-ide', 'amp --settings-file "/private/settings.json --no-ide'])('refuses an incomplete or ambiguous launch: %s', (command) => {
    expect(() => ampSettingsPath(command)).toThrow(/settings/)
  })

  it('parses a long path without retrying a pattern over its characters', () => {
    const path = `/private/${'x'.repeat(100_000)}/settings.json`
    expect(ampSettingsPath(`amp --settings-file ${path} --no-ide`)).toBe(path)
  })
})

describe('ampCatalogPermission', () => {
  const frame: NativeControlFrame = { requestId: 'exact-permission', responseState: ControlResponseState.READY, payload: {
    type: 'leapmux_amp_permission',
    tool_name: 'shell_command',
    tool_use_id: ampToolUseID('thread', 'call'),
    input: { command: 'exact native command' },
  } }

  it('keeps one exact request despite identical replay and ignores completed controls', () => {
    const completed = { ...frame, requestId: 'older', responseState: ControlResponseState.COMPLETED, payload: {} }
    expect(ampCatalogPermission([completed, frame, frame], 'thread', 'call', 'exact native command')).toBe(frame)
    expect(ampCatalogPermission([], 'thread', 'call', 'exact native command')).toBeUndefined()
  })

  it('uses the latest state of an older replayed request before selecting the current call', () => {
    const oldReady = { ...frame, requestId: 'older', payload: { ...frame.payload, tool_use_id: ampToolUseID('thread', 'old-call'), input: { command: 'earlier command' } } }
    const oldCompleted = { ...oldReady, responseState: ControlResponseState.COMPLETED }
    expect(ampCatalogPermission([oldReady, oldCompleted, frame], 'thread', 'call', 'exact native command')).toBe(frame)
  })

  it('rejects distinct pending requests for the same native call', () => {
    expect(() => ampCatalogPermission([frame, { ...frame, requestId: 'second' }], 'thread', 'call', 'exact native command')).toThrow('ambiguous')
  })

  it.each([{ type: 'other' }, { tool_name: 'other' }, { tool_use_id: 'other' }, { input: { command: 'other' } }, { input: null }])('rejects a wrong native permission shape: %j', (fields) => {
    expect(() => ampCatalogPermission([{ ...frame, payload: { ...frame.payload, ...fields } }], 'thread', 'call', 'exact native command')).toThrow('permission')
  })
})

describe('assertAmpGeneratedSettings', () => {
  it('requires the observed Worker and private generated file modes outside the run', () => {
    processFixture()
    const external = mkdtempSync(join(dirname(directory), 'amp-owned-settings-'))
    extraDirectories.push(external)
    const parent = join(external, process.getuid ? `leapmux-agents-${process.getuid()}` : 'leapmux-agents')
    const nativeDirectory = join(parent, 'amp-10-12345')
    mkdirSync(nativeDirectory, { recursive: true, mode: 0o700 })
    const path = join(nativeDirectory, 'settings.json')
    writeFileSync(path, '{}', { mode: 0o600 })
    expect(() => assertAmpGeneratedSettings(path, 10, directory)).not.toThrow()
    expect(() => assertAmpGeneratedSettings(path, 11, directory)).toThrow('observed Worker')
    if (process.platform !== 'win32') {
      chmodSync(path, 0o644)
      expect(() => assertAmpGeneratedSettings(path, 10, directory)).toThrow('private owned')
    }
  })
})

describe('ampExecutorToolNames', () => {
  it('reads only the actual tool identities', () => {
    expect(ampExecutorToolNames(JSON.stringify([{ name: 'shell_command', source: 'built-in', description: 'Execute a command.' }]))).toEqual(['shell_command'])
  })

  it.each(['[]', '{}', 'null', '[null]', '[{}]', '[{"name":"","source":"built-in"}]', '[{"name":" ","source":"built-in"}]', '[{"name":"shell_command","source":" "}]', '[{"name":"shell_command"}]', 'not-json'])('refuses an absent or malformed native catalog: %s', (text) => {
    expect(() => ampExecutorToolNames(text)).toThrow()
  })
})

describe('readAmpExecutorCatalog', () => {
  it('approves the exact focused editor when it renders outside every tile', async () => {
    const { context, approve } = catalogFixture({ permission: 'ask', editorOutsideTile: true })
    await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(approve).toHaveBeenCalledTimes(1)
  })

  it('proves the physical owned executable when the observed native argv starts with bare amp', async () => {
    const { context, worker, amp, tool } = catalogFixture()
    const observed = { pid: amp.pid, ppid: amp.ppid, command: amp.command.replace(`"${calls.state.ampPath}"`, 'amp') }
    calls.processes.mockReturnValue([worker, observed, tool])
    await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(calls.executable).toHaveBeenCalledExactlyOnceWith(amp.pid)
    expect(calls.catalog).toHaveBeenCalledTimes(1)
  })

  it('refuses a bare native argv when the actual physical executable belongs to another program', async () => {
    const { context, worker, amp, tool } = catalogFixture()
    const observed = { pid: amp.pid, ppid: amp.ppid, command: amp.command.replace(`"${calls.state.ampPath}"`, 'amp') }
    calls.processes.mockReturnValue([worker, observed, tool])
    const wrong = join(directory, 'not-amp')
    writeFileSync(wrong, '')
    calls.executable.mockResolvedValue(wrong)
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toThrow('process identity failed')
    expect(calls.executable).toHaveBeenCalledExactlyOnceWith(amp.pid)
    expect(calls.catalog).not.toHaveBeenCalled()
  })

  it('refuses changed physical process ancestry after querying its executable', async () => {
    const { context, worker, amp, tool } = catalogFixture()
    const observed = { pid: amp.pid, ppid: amp.ppid, command: amp.command.replace(`"${calls.state.ampPath}"`, 'amp') }
    calls.processes.mockReturnValueOnce([worker, observed, tool]).mockReturnValue([worker, { ...observed, ppid: 99 }, tool])
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toThrow('process changed during')
    expect(calls.catalog).not.toHaveBeenCalled()
  })

  it('reads the actual private Worker-generated settings outside the E2E run directory', async () => {
    const { context, worker, amp, tool, settingsPath } = catalogFixture()
    const external = mkdtempSync(join(dirname(directory), 'amp-native-generated-'))
    extraDirectories.push(external)
    const parent = join(external, process.getuid ? `leapmux-agents-${process.getuid()}` : 'leapmux-agents')
    const generated = join(parent, `amp-${worker.pid}-123456`)
    mkdirSync(generated, { recursive: true, mode: 0o700 })
    const generatedSettings = join(generated, 'settings.json')
    writeFileSync(generatedSettings, readFileSync(settingsPath), { mode: 0o600 })
    calls.processes.mockReturnValue([worker, { ...amp, command: amp.command.replace(settingsPath, generatedSettings) }, tool])
    await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(calls.catalog).toHaveBeenCalledWith(calls.state.ampPath, expect.arrayContaining(['--settings-file', generatedSettings]), expect.anything())
  })

  it('uses the actual first-turn thread after the initial active agent has no native session', async () => {
    const { context, approve } = catalogFixture({ permission: 'ask', delayedSession: true })
    await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(approve).toHaveBeenCalledTimes(1)
    expect(calls.nativeAgent).toHaveBeenCalledWith(context, 'actual-agent')
  })

  it('approves the exact active agent footer when its controls are siblings of the banner', async () => {
    const { context, approve } = catalogFixture({ permission: 'ask', siblingControls: true })
    await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(approve).toHaveBeenCalledTimes(1)
  })

  it('refuses a changed native thread before approving the held catalog call', async () => {
    const { context, approve, workerClose } = catalogFixture({ permission: 'ask' })
    const original = await calls.current(context)
    calls.nativeAgent.mockResolvedValue(create(AgentInfoSchema, { ...original, agentSessionId: 'T-other-session' }))
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toThrow('native Amp catalog session changed')
    expect(approve).not.toHaveBeenCalled()
    expect(workerClose).toHaveBeenCalledTimes(1)
  })

  it('reads the model baseline before it starts a native control subscription', async () => {
    const { context } = catalogFixture()
    const failure = new Error('The native model baseline could not be read.')
    context.modelScript.status = vi.fn<ModelScript['status']>().mockRejectedValue(failure)
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toBe(failure)
    expect(calls.watch).not.toHaveBeenCalled()
    expect(calls.send).not.toHaveBeenCalled()
    expect(calls.catalog).not.toHaveBeenCalled()
  })

  it('approves only the exact Ask request before the held tool creates its PID', async () => {
    const { context, approve, files } = catalogFixture({ permission: 'ask' })
    await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(approve).toHaveBeenCalledTimes(1)
    expect(calls.watch).toHaveBeenCalledWith(context.leapmuxServer, 'actual-agent')
    expect(calls.cancelWatch).toHaveBeenCalledTimes(1)
    expect(existsSync(files().pidFile)).toBe(false)
  })

  it.each(['call', 'command'])('refuses an unrelated native permission %s and stops the exact agent before PID creation', async (identity) => {
    const { context, approve, workerClose, files } = catalogFixture({ permission: 'ask', wrongCall: identity === 'call', wrongCommand: identity === 'command' })
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toThrow(/native.*permission|permission.*native/i)
    expect(approve).not.toHaveBeenCalled()
    expect(calls.catalog).not.toHaveBeenCalled()
    expect(workerClose).toHaveBeenCalledWith('actual-worker', 'CloseAgent', expect.anything(), expect.anything(), expect.objectContaining({ agentId: 'actual-agent' }))
    expect(calls.cancelWatch).toHaveBeenCalledTimes(1)
    expect(existsSync(files().pidFile)).toBe(false)
    expect(existsSync(files().releaseFile)).toBe(false)
  })

  it('preserves an approval failure and closes the exact Worker agent before deleting private receipts', async () => {
    const cause = new Error('The actual native approval operation failed.')
    const { context, workerClose, files } = catalogFixture({ permission: 'ask', approvalFailure: cause })
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toBe(cause)
    expect(workerClose).toHaveBeenCalledTimes(1)
    expect(calls.catalog).not.toHaveBeenCalled()
    expect(calls.cancelWatch).toHaveBeenCalledTimes(1)
    expect(existsSync(files().pidFile)).toBe(false)
    expect(existsSync(files().releaseFile)).toBe(false)
  })

  it('copies exact private ownership evidence before the held tool releases', async () => {
    const { context, worker, amp, tool, files } = catalogFixture()
    calls.processes.mockReturnValue([worker, { ...amp, executable: 'unrelated-native-executable' }, tool])
    const reportDir = mkdtempSync(join(dirname(directory), 'amp-report-evidence-'))
    extraDirectories.push(reportDir)
    const reportCopy = join(reportDir, 'ownership.json')
    const onOwnershipDiagnostic = vi.fn(async ({ path }: { path: string }) => {
      expect(existsSync(files().releaseFile)).toBe(false)
      expect(calls.idle).not.toHaveBeenCalled()
      const text = readFileSync(path, 'utf8')
      const receipt: unknown = JSON.parse(text)
      expect(receipt).toMatchObject({ proof: { toolPid: 30, toolParentPid: 20, workerExecutable: calls.state.binaryPath, workerDataDir: calls.state.dataDir }, ownership: { workerPid: 10 } })
      if (process.platform !== 'win32')
        expect(statSync(path).mode & 0o777).toBe(0o600)
      writeFileSync(reportCopy, text)
    })
    const options = { workerDataDir: calls.state.dataDir, onOwnershipDiagnostic }
    await expect(readAmpExecutorCatalog(context, options, calls.catalog)).rejects.toThrow('process identity failed')
    expect(onOwnershipDiagnostic).toHaveBeenCalledTimes(1)
    expect(calls.idle).toHaveBeenCalledWith(context.page)
    expect(existsSync(files().pidFile)).toBe(false)
    expect(existsSync(files().releaseFile)).toBe(false)
    rmSync(directory, { recursive: true, force: true })
    expect(JSON.parse(readFileSync(reportCopy, 'utf8'))).toMatchObject({ proof: { toolPid: 30 } })
  })

  it('keeps ownership and attachment failures while native cleanup still completes', async () => {
    const { context, worker, amp, tool, files } = catalogFixture()
    calls.processes.mockReturnValue([worker, { ...amp, executable: 'unrelated-native-executable' }, tool])
    const attachmentError = new Error('The private ownership attachment failed.')
    const options = { workerDataDir: calls.state.dataDir, onOwnershipDiagnostic: vi.fn(async () => {
      throw attachmentError
    }) }
    let failure: unknown
    try {
      await readAmpExecutorCatalog(context, options, calls.catalog)
    }
    catch (error) {
      failure = error
    }
    expect(failure).toBeInstanceOf(AggregateError)
    if (!(failure instanceof AggregateError))
      throw new Error('The diagnostic failure did not preserve both errors.')
    expect(failure.errors).toContain(attachmentError)
    expect(failure.errors.some(error => error instanceof Error && error.message.includes('complete native stream launch'))).toBe(true)
    expect(options.onOwnershipDiagnostic).toHaveBeenCalledTimes(1)
    expect(calls.idle).toHaveBeenCalledWith(context.page)
    expect(existsSync(files().pidFile)).toBe(false)
    expect(existsSync(files().releaseFile)).toBe(false)
  })

  it('uses the persistent owned Amp process after an earlier native credential turn', async () => {
    const { context, steps, status, files, settingsPath } = catalogFixture()
    const result = await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(result.tools).toEqual(['read_file'])
    expect(result.settings['amp.mcpServers']).toHaveProperty('credential_probe')
    expect(calls.catalog).toHaveBeenCalledWith(calls.state.ampPath, ['tools', 'list', '--json', '--mode', 'smart', '--settings-file', settingsPath, '--no-ide', '--no-notifications', '--no-color'], expect.objectContaining({ cwd: directory, env: context.leapmuxServer.agentEnv }))
    expect(steps).toHaveBeenCalledWith(2)
    expect(calls.idle).toHaveBeenCalledWith(context.page)
    expect(existsSync(files().pidFile)).toBe(false)
    expect(existsSync(files().releaseFile)).toBe(false)
    const catalogFailure = new Error('The actual local catalog process failed.')
    calls.catalog.mockRejectedValueOnce(catalogFailure)
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toBe(catalogFailure)
    expect(existsSync(files().releaseFile)).toBe(false)
    const completionFailure = new Error('The native model result did not complete.')
    steps.mockResolvedValueOnce(status(1)).mockRejectedValueOnce(completionFailure)
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toBe(completionFailure)
    expect(existsSync(files().releaseFile)).toBe(true)
  })
  it('restricts the actual catalog command to the remaining whole-test deadline', async () => {
    const { context } = catalogFixture()
    await readAmpExecutorCatalog(context, {}, calls.catalog)
    expect(calls.catalog).toHaveBeenCalledWith(calls.state.ampPath, expect.any(Array), expect.objectContaining({ timeout: 60_000 }))
  })

  it('refreshes the remaining deadline before each catalog, list, and doctor command', async () => {
    const { context, clock } = catalogFixture()
    const timeouts: Array<number | undefined> = []
    let now = 1_000_000
    calls.catalog.mockImplementation(async (_executable, args, options) => {
      timeouts.push(options.timeout)
      now += 1000
      clock.mockReturnValue(now)
      if (args[0] === 'tools')
        return { stdout: JSON.stringify([{ name: 'read_file', source: 'built-in' }]), stderr: '' }
      if (args[1] === 'list')
        return { stdout: JSON.stringify([{ name: 'project_probe', source: 'workspace', type: 'command', spec: { command: 'private-runtime', args: ['private-server'] } }]), stderr: '' }
      return { stdout: 'project_probe (workspace: untrusted, server: untrusted): awaiting approval\n', stderr: '' }
    })
    await readAmpExecutorCatalog(context, { workspaceMcpServer: 'project_probe' }, calls.catalog)
    expect(timeouts).toEqual([60_000, 59_000, 58_000])
  })

  it('uses the installed combined MCP list without the removed local flag', async () => {
    const { context, settingsPath } = catalogFixture()
    calls.catalog.mockImplementation(async (_executable, args) => {
      if (args[0] === 'tools')
        return { stdout: JSON.stringify([{ name: 'read_file', source: 'built-in' }]), stderr: '' }
      if (args[1] === 'list')
        return { stdout: JSON.stringify([{ name: 'project_probe', source: 'workspace', type: 'command', spec: { command: 'private-runtime' } }]), stderr: '' }
      return { stdout: 'project_probe (workspace: untrusted, server: untrusted): awaiting approval\n', stderr: '' }
    })
    await readAmpExecutorCatalog(context, { workspaceMcpServer: 'project_probe' }, calls.catalog)
    const list = calls.catalog.mock.calls.find(([, args]) => args[0] === 'mcp' && args[1] === 'list')
    expect(list?.[1]).toEqual(['mcp', 'list', '--json', '--settings-file', settingsPath, '--no-ide', '--no-notifications', '--no-color'])
  })

  it.each([1_000_000, 999_999, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])('refuses an expired or invalid deadline before native launch: %s', async (deadline) => {
    const { context } = catalogFixture()
    context.modelScript.testDeadline = () => deadline
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toThrow('deadline')
    expect(calls.send).not.toHaveBeenCalled()
    expect(calls.catalog).not.toHaveBeenCalled()
  })

  it('preserves an actual local command cancellation and releases its native tool proof', async () => {
    const { context, files } = catalogFixture()
    const cancellation = new AbortController()
    let child: ReturnType<typeof execFile> | undefined
    let closed = Promise.resolve()
    const command = vi.fn<AmpCatalogCommand>().mockImplementation(async (_executable, _args, options) => new Promise<{ stdout: string, stderr: string }>((resolve, reject) => {
      expect(options.timeout).toBe(60_000)
      const running = execFile(process.execPath, ['-e', 'process.stdout.write("LOCAL_COMMAND_READY\\n");process.stdin.resume()'], { ...options, encoding: 'utf8', signal: cancellation.signal }, (error, stdout, stderr) => {
        if (error)
          reject(error)
        else
          resolve({ stdout, stderr })
      })
      child = running
      closed = new Promise<void>(resolve => running.once('close', () => resolve()))
      running.stdout?.once('data', () => cancellation.abort())
    }))
    try {
      await expect(readAmpExecutorCatalog(context, {}, command)).rejects.toMatchObject({ code: 'ABORT_ERR' })
    }
    finally {
      cancellation.abort()
      await closed
    }
    expect(command).toHaveBeenCalledTimes(1)
    expect(child?.signalCode).toBe('SIGTERM')
    expect(calls.idle).toHaveBeenCalledWith(context.page)
    expect(existsSync(files().pidFile)).toBe(false)
    expect(existsSync(files().releaseFile)).toBe(false)
  }, 30_000)

  it('refuses a HOME outside the actual private run before it starts the native tool', async () => {
    const { context } = catalogFixture()
    const outside = mkdtempSync(join(dirname(directory), 'amp-outside-home-'))
    extraDirectories.push(outside)
    const environment = context.leapmuxServer.agentEnv
    if (!environment)
      throw new Error('The Amp catalog test contains no private environment.')
    environment.HOME = outside
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toThrow('outside the E2E run')
    expect(calls.send).not.toHaveBeenCalled()
    expect(calls.catalog).not.toHaveBeenCalled()
  })

  it('refuses generated settings outside the private run despite exact native process ownership', async () => {
    const { context, worker, amp, tool, settingsPath } = catalogFixture()
    const outside = mkdtempSync(join(dirname(directory), 'amp-outside-settings-'))
    extraDirectories.push(outside)
    const outsideSettings = join(outside, 'settings.json')
    writeFileSync(outsideSettings, JSON.stringify({ external: true }))
    calls.processes.mockReturnValue([worker, { ...amp, command: amp.command.replace(settingsPath, outsideSettings) }, tool])
    await expect(readAmpExecutorCatalog(context, {}, calls.catalog)).rejects.toThrow('native Amp settings do not belong to the observed Worker directory')
    expect(calls.catalog).not.toHaveBeenCalled()
  })
})

describe('ampCatalogCommandTimeout', () => {
  it('uses a finite maximum without a test deadline and caps a large remaining duration', () => {
    expect(ampCatalogCommandTimeout(undefined, 0)).toBe(60_000)
    expect(ampCatalogCommandTimeout(Number.MAX_SAFE_INTEGER, 0)).toBe(60_000)
    expect(ampCatalogCommandTimeout(100, 99)).toBe(1)
    expect(() => ampCatalogCommandTimeout(100.5, 100)).toThrow('deadline')
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])('refuses an expired or invalid deadline: %s', (deadline) => {
    expect(() => ampCatalogCommandTimeout(deadline, 0)).toThrow('deadline')
  })

  it('refuses an invalid current time even when no test deadline exists', () => {
    expect(() => ampCatalogCommandTimeout(undefined, Number.NaN)).toThrow('current time')
  })
})

describe('ampWorkerDataDirectory', () => {
  it.each(['-data-dir', '--data-dir'])('reads the actual last Worker argument with %s', (flag) => {
    expect(ampWorkerDataDirectory(`leapmux worker ${flag} "/private/worker with spaces"`)).toBe('/private/worker with spaces')
  })

  it.each(['', 'leapmux --data-dir', 'leapmux --data-dir relative', 'leapmux --data-dir-more /private/worker', 'leapmux -data-dir /private/one --data-dir /private/two', 'leapmux --data-dir /private/worker\nother'])('refuses an absent or ambiguous data directory: %s', (command) => {
    expect(() => ampWorkerDataDirectory(command)).toThrow('data')
  })
})

describe('ampCatalogProcess', () => {
  it('selects only the held tool owner and excludes another Amp sibling', () => {
    const { worker, amp, tool, proof, settingsPath } = processFixture()
    expect(ampCatalogProcess([worker, amp, tool, { ...amp, pid: 40 }], proof)).toEqual({ workerPid: 10, ampPid: 20, settingsPath })
  })

  it('supports a shell and wrapper between the Worker and the native Amp process', () => {
    const { worker, amp, tool, proof } = processFixture()
    const shell = { pid: 15, ppid: 10, command: 'private-shell' }
    const wrapper = { pid: 16, ppid: 15, command: 'private-runtime amp-startup-wrapper' }
    expect(ampCatalogProcess([worker, shell, wrapper, { ...amp, ppid: 16 }, tool], proof).ampPid).toBe(20)
  })

  it('requires the exact native command when executable metadata is absent', () => {
    const { worker, amp, tool, proof } = processFixture()
    const commandOnly = { pid: amp.pid, ppid: amp.ppid, command: amp.command }
    expect(ampCatalogProcess([worker, commandOnly, tool], proof).ampPid).toBe(20)
    expect(() => ampCatalogProcess([worker, { ...commandOnly, command: `another-program ${amp.command}` }, tool], proof)).toThrow('complete native stream')
  })

  it('refuses a wrong or duplicate actual tool parent', () => {
    const { worker, amp, tool, proof } = processFixture()
    expect(() => ampCatalogProcess([worker, amp, { ...tool, ppid: 99 }], proof)).toThrow('matching parent')
    expect(() => ampCatalogProcess([worker, amp, tool, tool], proof)).toThrow('matching parent')
  })

  it('refuses another Worker data directory despite a matching executable', () => {
    const { worker, amp, tool, proof } = processFixture()
    const other = join(directory, 'another-worker')
    mkdirSync(other)
    expect(() => ampCatalogProcess([worker, amp, tool], { ...proof, workerDataDir: other })).toThrow('another Worker')
  })

  it('refuses incomplete native flags, another executable, and duplicate owned launches', () => {
    const { worker, amp, tool, proof } = processFixture()
    expect(() => ampCatalogProcess([worker, { ...amp, command: amp.command.replace('--stream-json-input', '--stream-json-input-more') }, tool], proof)).toThrow('complete native stream')
    expect(() => ampCatalogProcess([worker, { ...amp, executable: 'another-executable' }, tool], proof)).toThrow('complete native stream')
    expect(() => ampCatalogProcess([worker, amp, tool, { ...amp, pid: 21, ppid: 20 }], proof)).toThrow('complete native stream')
  })
})

describe('ampPidReceipt', () => {
  const receipt = { pid: 30, ppid: 20, workingDir: '/private/project', home: '/private/home', endpoint: 'http://unit.invalid' }
  it('retains only actual native PID and environment fields', () => {
    expect(ampPidReceipt(JSON.stringify({ ...receipt, unrelated: 'ignored' }))).toEqual(receipt)
  })

  it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, '30', null])('refuses an invalid native PID: %j', (pid) => {
    expect(() => ampPidReceipt(JSON.stringify({ ...receipt, pid }))).toThrow('valid PID')
    expect(() => ampPidReceipt(JSON.stringify({ ...receipt, ppid: pid }))).toThrow('valid PID')
  })

  it.each([null, [], {}, { ...receipt, workingDir: 'relative' }, { ...receipt, home: '' }, { ...receipt, endpoint: '' }])('refuses missing native ownership fields: %j', (value) => {
    expect(() => ampPidReceipt(JSON.stringify(value))).toThrow('valid PID')
  })
})

describe('ampWorkspaceMcpConfiguration', () => {
  const entry = { name: 'project_probe', source: 'workspace', type: 'command', spec: { command: 'private-runtime', args: ['private-server'] } }
  it('retains the exact installed CLI workspace command and permits omitted arguments', () => {
    expect(ampWorkspaceMcpConfiguration(JSON.stringify([entry]), 'project_probe')).toEqual(entry.spec)
    expect(ampWorkspaceMcpConfiguration(JSON.stringify([{ ...entry, spec: { command: 'private-runtime' } }]), 'project_probe')).toEqual({ command: 'private-runtime' })
  })

  it.each([
    { entries: [] },
    { entries: [entry, entry] },
    { entries: [{ ...entry, source: 'global' }] },
    { entries: [{ ...entry, type: 'url' }] },
    { entries: [{ ...entry, spec: null }] },
    { entries: [{ ...entry, spec: { command: '' } }] },
    { entries: [{ ...entry, spec: { command: 'private-runtime', args: [1] } }] },
  ])('refuses absent, ambiguous, or malformed workspace entries: $entries', ({ entries }) => {
    expect(() => ampWorkspaceMcpConfiguration(JSON.stringify(entries), 'project_probe')).toThrow('workspace command')
  })

  it('refuses invalid JSON, a scalar list, and an empty server identity', () => {
    expect(() => ampWorkspaceMcpConfiguration('{broken', 'project_probe')).toThrow()
    expect(() => ampWorkspaceMcpConfiguration('{}', 'project_probe')).toThrow('array')
    expect(() => ampWorkspaceMcpConfiguration(JSON.stringify([entry]), ' ')).toThrow('server name')
  })
})

describe('ampWorkspaceMcpAwaitingApproval', () => {
  const blocked = 'project_probe (workspace: untrusted, server: untrusted): awaiting approval'
  it('reads the exact final native status after connecting progress', () => {
    expect(() => ampWorkspaceMcpAwaitingApproval(`User settings: /private/settings.json\nproject_probe (workspace: untrusted): connecting...\n${blocked}\n`, 'project_probe')).not.toThrow()
  })

  it.each(['', blocked.replace('project_probe', 'another_probe'), blocked.replace('workspace: untrusted', 'user settings'), `${blocked}\nproject_probe (workspace: trusted): connected (1 tools: echo)`])('refuses an absent, wrong-source, or superseded approval state: %s', (text) => {
    expect(() => ampWorkspaceMcpAwaitingApproval(text, 'project_probe')).toThrow('await approval')
  })
})
