import type { NativeControlFrame } from '../helpers/nativeControlWatch'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { ProcessRow } from '../helpers/processTree'
import { execFile } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, lstatSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { basename, dirname, isAbsolute, join, relative } from 'node:path'
import process from 'node:process'
import { promisify } from 'node:util'
import { expect } from '@playwright/test'
import { AMP_PERMISSION_MODE, AMP_PERMISSION_REQUEST_FIELD, AMP_PERMISSION_REQUEST_TYPE, AMP_SHELL_TOOL } from '../../../src/generated/contracts/amp-protocol'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { ampToolUseID } from '../helpers/ampSurface'
import { requireBinary } from '../helpers/binaryOnPath'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { commandStartsWithExecutable, resolveNativeProcessOwnership, sameExecutablePath, workerDataDirectory } from '../helpers/nativeProcessOwnership'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { clickNativeToolApproval, processNativeToolApproval } from '../helpers/nativeToolExecution'
import { processExecutable } from '../helpers/processExecutable'
import { listProcesses } from '../helpers/processTree'
import { bashToolCall } from '../helpers/providerToolCalls'
import { getGlobalState, hubSpawnEnv } from '../helpers/server'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { ampToolResultReader } from './toolResult'

const execFileAsync = promisify(execFile)
const NATIVE_CATALOG_COMMAND_LIMIT_MS = 60_000

/** Require one current Amp permission request for the exact held shell call. */
export function ampCatalogPermission(controls: readonly NativeControlFrame[], threadId: string, callId: string, command: string): NativeControlFrame | undefined {
  if (!threadId || !callId || !command)
    throw new Error('The native Amp catalog permission requires exact thread, call, and command identity.')
  const nativeId = ampToolUseID(threadId, callId)
  const requests = new Map<string, NativeControlFrame>()
  const latest = new Map<string, NativeControlFrame>()
  for (const frame of controls) {
    if (!frame.requestId)
      throw new Error('The native Amp catalog permission has no request ID.')
    latest.set(frame.requestId, frame)
  }
  for (const frame of latest.values()) {
    if (frame.responseState !== ControlResponseState.READY)
      continue
    const payload = frame.payload
    if (payload[AMP_PERMISSION_REQUEST_FIELD.Type] !== AMP_PERMISSION_REQUEST_TYPE.Request)
      throw new Error('The native Amp catalog received an unrelated permission request.')
    const input = payload[AMP_PERMISSION_REQUEST_FIELD.Input]
    if (payload[AMP_PERMISSION_REQUEST_FIELD.ToolUseID] !== nativeId
      || payload[AMP_PERMISSION_REQUEST_FIELD.ToolName] !== AMP_SHELL_TOOL.ShellCommand
      || !isObject(input) || input.command !== command) {
      throw new Error('The native Amp catalog permission does not match its exact call and command.')
    }
    requests.set(frame.requestId, frame)
  }
  if (requests.size > 1)
    throw new Error('The native Amp catalog permission request is ambiguous.')
  return requests.values().next().value
}

/** Native settings can use a private operating-system directory to satisfy socket path limits. */
export function assertAmpGeneratedSettings(path: string, workerPid: number, privateRun: string): void {
  const inside = relative(realpathSync(privateRun), realpathSync(path))
  if (inside !== '..' && !inside.startsWith(`..${process.platform === 'win32' ? '\\' : '/'}`) && !isAbsolute(inside)) {
    assertPrivateNativePath(path, privateRun)
    return
  }
  const directory = dirname(path)
  const parent = dirname(directory)
  const expectedParent = process.getuid ? `leapmux-agents-${process.getuid()}` : 'leapmux-agents'
  if (basename(path) !== 'settings.json' || !new RegExp(`^amp-${workerPid}-[0-9]+$`).test(basename(directory)) || basename(parent) !== expectedParent)
    throw new Error('The native Amp settings do not belong to the observed Worker directory.')
  for (const [candidate, file] of [[parent, false], [directory, false], [path, true]] as const) {
    const stat = lstatSync(candidate)
    if (stat.isSymbolicLink() || (file ? !stat.isFile() : !stat.isDirectory()) || (process.getuid && stat.uid !== process.getuid())
      || (process.platform !== 'win32' && (stat.mode & 0o777) !== (file ? 0o600 : 0o700))) {
      throw new Error('The native Amp settings require private owned files and directories without symlinks.')
    }
  }
}

/** The default executes the installed CLI. Unit tests can supply controlled command I/O. */
export type AmpCatalogCommand = (
  executable: string,
  args: string[],
  options: { cwd: string, env: NodeJS.ProcessEnv, maxBuffer: number, timeout?: number },
) => Promise<{ stdout: string, stderr: string }>

/** Use the current whole-test deadline and a finite maximum for each local command. */
export function ampCatalogCommandTimeout(deadline: number | undefined, now = Date.now()): number {
  if (!Number.isFinite(now) || (deadline !== undefined && !Number.isFinite(deadline)))
    throw new RangeError('The native Amp catalog requires a finite test deadline and current time.')
  if (deadline === undefined)
    return NATIVE_CATALOG_COMMAND_LIMIT_MS
  const remaining = Math.floor(deadline - now)
  if (remaining <= 0)
    throw new Error('The native Amp catalog deadline leaves no complete millisecond for its command.')
  return Math.min(remaining, NATIVE_CATALOG_COMMAND_LIMIT_MS)
}

function flagPositions(command: string, flag: string): number[] {
  const positions: number[] = []
  const whitespace = (value: string | undefined) => value === undefined || value.trim() === ''
  let offset = 0
  for (;;) {
    const index = command.indexOf(flag, offset)
    if (index < 0)
      return positions
    if (whitespace(command[index - 1]) && whitespace(command[index + flag.length]))
      positions.push(index)
    offset = index + flag.length
  }
}

/** Read the generated settings argument from the actual Amp launch. */
export function ampSettingsPath(command: string): string {
  const flags = flagPositions(command, '--settings-file')
  const position = flags[0]
  if (flags.length !== 1 || position === undefined)
    throw new Error('The native Amp launch must contain one settings-file argument.')
  const start = position + '--settings-file'.length
  const ends = flagPositions(command, '--no-ide').filter(index => index > start)
  if (ends.length !== 1)
    throw new Error('The native Amp launch contains no unambiguous settings-file boundary.')
  const matched = command.slice(start, ends[0]).trim()
  if (!matched)
    throw new Error('The native Amp launch contains no complete settings-file argument.')
  const path = (matched.startsWith('"') && matched.endsWith('"')) || (matched.startsWith('\'') && matched.endsWith('\'')) ? matched.slice(1, -1) : matched
  if (!isAbsolute(path) || !path.endsWith('settings.json') || path.includes('\n'))
    throw new Error('The native Amp settings path must identify its absolute generated file.')
  return path
}

/** Read the installed CLI's nonempty executor catalog without accepting malformed entries. */
export function ampExecutorToolNames(text: string): string[] {
  const decoded: unknown = JSON.parse(text)
  if (!Array.isArray(decoded) || decoded.length === 0)
    throw new Error('The native Amp executor catalog must contain tools.')
  return decoded.map((tool: unknown) => {
    if (!isObject(tool) || typeof tool.name !== 'string' || tool.name.trim() === '' || typeof tool.source !== 'string' || tool.source.trim() === '')
      throw new Error('The native Amp executor catalog contains an invalid tool.')
    return tool.name
  })
}

interface AmpProcessProof {
  toolPid: number
  toolParentPid: number
  workerExecutable: string
  workerDataDir: string
  ampExecutables: readonly string[]
}

/** Resolve one actual held tool through its exact Worker and native Amp launch. */
export function ampCatalogProcess(rows: readonly ProcessRow[], proof: AmpProcessProof): { workerPid: number, ampPid: number, settingsPath: string } {
  const tools = rows.filter(row => row.pid === proof.toolPid)
  if (tools.length !== 1 || tools[0]?.ppid !== proof.toolParentPid)
    throw new Error('The actual Amp tool PID has no unique matching parent process.')
  const ownership = resolveNativeProcessOwnership(rows, proof.toolPid, proof.workerExecutable)
  const workers = rows.filter(row => row.pid === ownership.workerPid)
  const worker = workers[0]
  if (workers.length !== 1 || !worker || realpathSync(workerDataDirectory(worker.rawCommand ?? worker.command)) !== realpathSync(proof.workerDataDir))
    throw new Error('The actual Amp tool belongs to another Worker data directory.')
  const owned = new Set(ownership.ownedPids)
  const candidates = rows.filter((row) => {
    if (!owned.has(row.pid))
      return false
    const command = row.rawCommand ?? row.command
    const executableMatches = proof.ampExecutables.some(path => row.executable ? sameExecutablePath(row.executable, path) : commandStartsWithExecutable(command, path))
    return executableMatches && ['--execute', '--stream-json', '--stream-json-input', '--settings-file'].every(flag => flagPositions(command, flag).length === 1)
  })
  const candidate = candidates[0]
  if (candidates.length !== 1 || !candidate)
    throw new Error('The actual owned Amp process must have one complete native stream launch.')
  return { workerPid: ownership.workerPid, ampPid: candidate.pid, settingsPath: ampSettingsPath(candidate.rawCommand ?? candidate.command) }
}

interface AmpPidReceipt {
  pid: number
  ppid: number
  workingDir: string
  home: string
  endpoint: string
}

/** Validate the PID and environment that the actual held native tool writes. */
export function ampPidReceipt(text: string): AmpPidReceipt {
  const value: unknown = JSON.parse(text)
  if (!isObject(value) || !Number.isSafeInteger(value.pid) || typeof value.pid !== 'number' || value.pid <= 0
    || !Number.isSafeInteger(value.ppid) || typeof value.ppid !== 'number' || value.ppid <= 0
    || typeof value.workingDir !== 'string' || !isAbsolute(value.workingDir)
    || typeof value.home !== 'string' || !isAbsolute(value.home)
    || typeof value.endpoint !== 'string' || value.endpoint.trim() === '') {
    throw new Error('The actual Amp tool must report a valid PID and private environment.')
  }
  return { pid: value.pid, ppid: value.ppid, workingDir: value.workingDir, home: value.home, endpoint: value.endpoint }
}

/** Read the exact workspace entry from the installed CLI's combined MCP configuration list. */
export function ampWorkspaceMcpConfiguration(text: string, name: string): Record<string, unknown> {
  if (name.trim() === '')
    throw new Error('The native Amp workspace MCP proof requires a server name.')
  const value: unknown = JSON.parse(text)
  if (!Array.isArray(value))
    throw new Error('The native Amp local MCP list must contain an array.')
  const matches = value.filter(entry => isObject(entry) && entry.name === name)
  const entry = matches[0]
  if (matches.length !== 1 || !isObject(entry) || entry.source !== 'workspace' || entry.type !== 'command'
    || !isObject(entry.spec) || typeof entry.spec.command !== 'string' || entry.spec.command.trim() === ''
    || (entry.spec.args !== undefined && (!Array.isArray(entry.spec.args) || !entry.spec.args.every(value => typeof value === 'string')))) {
    throw new Error('The exact native Amp MCP server must have one valid workspace command configuration.')
  }
  return entry.spec
}

/** Require the installed doctor's final untrusted workspace status for one exact server. */
export function ampWorkspaceMcpAwaitingApproval(text: string, name: string): void {
  if (name.trim() === '')
    throw new Error('The native Amp trust status requires a server name.')
  const lines = text.split(/\r?\n/).filter(line => line.startsWith(`${name} (`))
  const last = lines.at(-1)
  if (last !== `${name} (workspace: untrusted, server: untrusted): awaiting approval`)
    throw new Error('The exact native Amp workspace MCP server must await approval.')
}

/** Probe the same generated settings that the actual isolated Amp process uses. */
export async function readAmpExecutorCatalog(
  context: ManagedNativeScenarioContext,
  options: {
    workerDataDir?: string
    workspaceMcpServer?: string
    onOwnershipDiagnostic?: (diagnostic: { path: string }) => Promise<void>
  } = {},
  command: AmpCatalogCommand = execFileAsync,
): Promise<{ tools: string[], settings: Record<string, unknown>, workspaceMcpConfiguration?: Record<string, unknown> }> {
  const environment = context.leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The native Amp catalog requires the isolated agent environment.')
  const home = environment.HOME
  const endpoint = environment.AMP_URL
  if (!home || !endpoint)
    throw new Error('The native Amp catalog requires its private HOME and mock endpoint.')
  const binary = requireBinary('amp', 'The native Amp catalog requires the installed CLI', hubSpawnEnv(environment))
  const state = getGlobalState()
  const workerDataDir = options.workerDataDir ?? state.dataDir
  assertPrivateNativePath(workerDataDir, state.tmpDir)
  assertPrivateNativePath(home, state.tmpDir)
  ampCatalogCommandTimeout(context.modelScript.testDeadline())
  const before = await currentNativeAgent(context)
  assertPrivateNativePath(before.workingDir, state.tmpDir)
  const marker = randomUUID()
  const pidFile = join(before.workingDir, `amp-catalog-${marker}.pid`)
  const releaseFile = join(before.workingDir, `amp-catalog-${marker}.release`)
  const callId = `amp-catalog-pid-${marker}`
  const script = `const fs=require('node:fs');fs.writeFileSync(${JSON.stringify(pidFile)},JSON.stringify({pid:process.pid,ppid:process.ppid,workingDir:process.cwd(),home:process.env.HOME,endpoint:process.env.AMP_URL}));const finish=()=>{if(fs.existsSync(${JSON.stringify(releaseFile)})){process.stdout.write('AMP_CATALOG_PID:'+process.pid+'\\n');process.exit(0)}};fs.watch(${JSON.stringify(before.workingDir)},finish);finish();setTimeout(()=>process.exit(1),600000)`
  const shellCommand = `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(script)}`
  const permissionMode = before.optionGroups.find(group => group.id === 'permissionMode')?.currentValue
  if (permissionMode !== AMP_PERMISSION_MODE.Ask && permissionMode !== AMP_PERMISSION_MODE.AllowAll)
    throw new Error('The native Amp catalog requires the actual Ask or Allow All permission mode.')
  const start = (await context.modelScript.status()).stepCount
  const watch = await watchNativeControls(context.leapmuxServer, before.id)
  let started = false
  let submitted = false
  let approved = false
  return withCleanup(async () => {
    await context.modelScript.queue(
      { toolCalls: [bashToolCall(context.provider, callId, shellCommand)] },
      { text: 'The native Amp catalog process completed.' },
    )
    submitted = true
    await sendMessage(context.page, context.modelScript.prompt('Run the supplied native process proof for the local Amp catalog.'))
    await context.modelScript.waitForSteps(start + 1)
    if (permissionMode === AMP_PERMISSION_MODE.Ask) {
      let permissionError: unknown
      await expect.poll(async () => {
        try {
          return await processNativeToolApproval({
            completed: async () => {
              if (existsSync(pidFile) && !approved)
                throw new Error('The native Amp catalog tool started before its exact permission approval.')
              return existsSync(pidFile)
            },
            clickIfReady: async () => {
              if (approved)
                return false
              const live = await nativeAgentById(context, before.id)
              if (!live || live.id !== before.id || live.workingDir !== before.workingDir
                || live.agentProvider !== before.agentProvider || (before.agentSessionId && live.agentSessionId !== before.agentSessionId)) {
                throw new Error('The native Amp catalog session changed before its permission decision.')
              }
              if (!live.agentSessionId)
                return false
              const permission = ampCatalogPermission(watch.controls(), live.agentSessionId, callId, shellCommand)
              if (!permission)
                return false
              if (existsSync(pidFile))
                throw new Error('The native Amp catalog PID exists before approval.')
              const allow = context.page.locator(`[data-testid="agent-editor-panel"][data-agent-id="${before.id}"]:visible [data-testid="control-actions"] [data-testid="control-allow-btn"]:visible`)
              const clicked = await allow.evaluateAll(clickNativeToolApproval)
              if (clicked)
                approved = true
              return clicked
            },
          })
        }
        catch (error) {
          permissionError = error
          return 'failed'
        }
      }).not.toBe('waiting')
      if (permissionError !== undefined)
        throw permissionError
    }
    await expect.poll(() => existsSync(pidFile)).toBe(true)
    started = true
    const receipt = ampPidReceipt(readFileSync(pidFile, 'utf8'))
    expect(realpathSync(receipt.workingDir)).toBe(realpathSync(before.workingDir))
    expect(realpathSync(receipt.home)).toBe(realpathSync(home))
    expect(receipt.endpoint).toBe(endpoint)
    let rows = listProcesses()
    const ownership = resolveNativeProcessOwnership(rows, receipt.pid, state.binaryPath)
    const owned = new Set(ownership.ownedPids)
    const launches = rows.filter(row => owned.has(row.pid) && ['--execute', '--stream-json', '--stream-json-input', '--settings-file'].every(flag => flagPositions(row.rawCommand ?? row.command, flag).length === 1))
    const root = launches[0]
    if (launches.length !== 1 || !root)
      throw new Error('The native Amp catalog has no unique owned provider launch.')
    if (!root.executable) {
      const executable = await processExecutable(root.pid)
      const currentRows = listProcesses()
      const currentRoot = currentRows.find(row => row.pid === root.pid)
      if (!currentRoot || currentRoot.ppid !== root.ppid || (currentRoot.rawCommand ?? currentRoot.command) !== (root.rawCommand ?? root.command))
        throw new Error('The native Amp provider process changed during its executable query.')
      const currentOwnership = resolveNativeProcessOwnership(currentRows, receipt.pid, state.binaryPath)
      if (currentOwnership.workerPid !== ownership.workerPid || !currentOwnership.ownedPids.includes(root.pid))
        throw new Error('The native Amp process ownership changed during its executable query.')
      rows = currentRows.map(row => row.pid === root.pid ? { ...row, executable } : row)
    }
    const proof = { toolPid: receipt.pid, toolParentPid: receipt.ppid, workerExecutable: state.binaryPath, workerDataDir, ampExecutables: [binary, realpathSync(binary)] }
    let launch: ReturnType<typeof ampCatalogProcess>
    try {
      launch = ampCatalogProcess(rows, proof)
    }
    catch (cause) {
      let ownership: ReturnType<typeof resolveNativeProcessOwnership> | undefined
      let ownershipError: string | undefined
      try {
        ownership = resolveNativeProcessOwnership(rows, receipt.pid, state.binaryPath)
      }
      catch (error) {
        ownershipError = error instanceof Error ? error.message : String(error)
      }
      const relevant = new Set(ownership?.ownedPids ?? [receipt.pid, receipt.ppid])
      if (ownership)
        relevant.add(ownership.workerPid)
      const diagnostic = join(before.workingDir, `amp-catalog-${marker}.ownership.json`)
      try {
        writeFileSync(diagnostic, JSON.stringify({ proof, ownership, ownershipError, rows: rows.filter(row => relevant.has(row.pid)) }, null, 2), { mode: 0o600 })
      }
      catch (error) {
        throw new AggregateError([cause, error], 'The native Amp process identity and its diagnostic write failed.')
      }
      try {
        await options.onOwnershipDiagnostic?.({ path: diagnostic })
      }
      catch (error) {
        throw new AggregateError([cause, error], 'The native Amp process identity and its diagnostic attachment failed.')
      }
      throw new Error(`The native Amp catalog process identity failed. Inspect ${diagnostic}.`, { cause })
    }
    const settingsPath = launch.settingsPath
    assertAmpGeneratedSettings(settingsPath, launch.workerPid, state.tmpDir)
    const settings: unknown = JSON.parse(readFileSync(settingsPath, 'utf8'))
    if (!isObject(settings))
      throw new Error('The native Amp generated settings must contain an object.')
    const agent = await currentNativeAgent(context)
    expect(agent.id).toBe(before.id)
    expect(agent.workingDir).toBe(before.workingDir)
    expect(agent.agentSessionId.trim()).not.toBe('')
    if (before.agentSessionId !== '')
      expect(agent.agentSessionId).toBe(before.agentSessionId)
    const mode = agent.optionGroups.find(group => group.id === 'agent_mode')?.currentValue
    if (!mode)
      throw new Error('The native Amp catalog requires the actual thread mode.')
    const output = await command(binary, ['tools', 'list', '--json', '--mode', mode, '--settings-file', settingsPath, '--no-ide', '--no-notifications', '--no-color'], { cwd: agent.workingDir, env: hubSpawnEnv(environment), maxBuffer: 2 * 1024 * 1024, timeout: ampCatalogCommandTimeout(context.modelScript.testDeadline()) })
    let workspaceMcpConfiguration: Record<string, unknown> | undefined
    if (options.workspaceMcpServer !== undefined) {
      const common = ['--settings-file', settingsPath, '--no-ide', '--no-notifications', '--no-color']
      const listing = await command(binary, ['mcp', 'list', '--json', ...common], { cwd: agent.workingDir, env: hubSpawnEnv(environment), maxBuffer: 2 * 1024 * 1024, timeout: ampCatalogCommandTimeout(context.modelScript.testDeadline()) })
      workspaceMcpConfiguration = ampWorkspaceMcpConfiguration(listing.stdout, options.workspaceMcpServer)
      const doctor = await command(binary, ['mcp', 'doctor', options.workspaceMcpServer, ...common], { cwd: agent.workingDir, env: hubSpawnEnv(environment), maxBuffer: 2 * 1024 * 1024, timeout: ampCatalogCommandTimeout(context.modelScript.testDeadline()) })
      ampWorkspaceMcpAwaitingApproval(doctor.stdout, options.workspaceMcpServer)
    }
    const after = await currentNativeAgent(context)
    expect(after.id).toBe(agent.id)
    expect(after.agentSessionId).toBe(agent.agentSessionId)
    return { tools: ampExecutorToolNames(output.stdout), settings, ...(workspaceMcpConfiguration === undefined ? {} : { workspaceMcpConfiguration }) }
  }, async () => {
    let completed = false
    try {
      if (started || existsSync(pidFile)) {
        writeFileSync(releaseFile, '')
        const status = await context.modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(context.page)
        completed = true
        const request = status.requests.find(value => value.stepIndex === start + 1)
        if (!request)
          throw new Error('The released native Amp process proof reached no model result.')
        const result = await ampToolResultReader(context)(request, callId)
        expect(result.failed).not.toBe(true)
        expect(result.exitCode).toBe(0)
        const receipt = ampPidReceipt(readFileSync(pidFile, 'utf8'))
        expect(result.text).toContain(`AMP_CATALOG_PID:${receipt.pid}`)
      }
      else if (submitted) {
        await closeNativeAgentAndWait(context, before.id)
        completed = true
      }
      else {
        completed = true
      }
    }
    finally {
      watch.cancel()
      // A live tool reads the release file asynchronously. Keep it until native idle confirms that the tool ended.
      if (completed) {
        rmSync(pidFile, { force: true })
        rmSync(releaseFile, { force: true })
      }
    }
  })
}
