import type { ChildProcess } from 'node:child_process'
import { execFile, spawn } from 'node:child_process'
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { promisify } from 'node:util'
import { agentDefaultsEnv } from '../agentSettings'
import {
  elevateSessionViaAPI,
  getUserId,
  getWorkerId,
  loginViaAPI,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './api'
import { finishCleanup } from './cleanup'
import { createMockAgentEnvironment, MOCK_MODEL_IDS } from './mockAgentEnvironment'
import { registerAmbientScenario } from './mockModelScenario'
import { createMockModelServer } from './mockModelServer'
import { stopProcess } from './process'
import { trackProcess } from './processRegistry'
import { E2E_BROWSER_HOST, hubDataDir, hubSpawnEnv } from './server'

export interface SuiteServerState {
  hubUrl: string
  adminToken: string
  adminUserId: string
  workerId: string
  newuserToken: string
  dataDir: string
  serverLogPath: string
  mockModelUrl: string
  piAgentDir: string
  /**
   * The isolated agent configuration this run wrote.
   *
   * A test that starts its OWN hub merges this through `hubSpawnEnv`, so its
   * agents reach the mock endpoint rather than the developer's real provider.
   */
  agentEnv: Record<string, string>
}

export interface StartedSuiteServer {
  state: SuiteServerState
  stop: () => Promise<void>
}

interface SuiteServerOptions {
  binaryPath: string
  tmpDir: string
}

const execFileAsync = promisify(execFile)

/**
 * The lines that state each host that the refusing proxy turned away in a run.
 *
 * A refusal keeps a request on the machine, and it fails that request at once. These
 * lines are how a reader learns which real host a process tried to reach, so an
 * unexpected host is visible and not only refused.
 */
export function refusedHostsReport(refused: ReadonlyMap<string, number>): string[] {
  return [...refused]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([host, count]) => `The mock proxy refused ${count} ${count === 1 ? 'request' : 'requests'} to ${host}.`)
}

/** Start the LeapMux process and the model server that one test run shares. */
export async function startSuiteServer(options: SuiteServerOptions): Promise<StartedSuiteServer> {
  const mockModel = await createMockModelServer({ models: MOCK_MODEL_IDS })
  // A provider names its own session at a moment no test controls. The ambient
  // scenario answers that housekeeping turn and nothing else, so a content turn
  // that no test scripted fails with its request recorded.
  await registerAmbientScenario(mockModel.url)
  const dataDir = mkdtempSync(join(options.tmpDir, 'leapmux-e2e-dev-'))
  const serverLogPath = join(options.tmpDir, 'suite-server.log')
  let proc: ChildProcess | undefined
  let stopped = false
  let socketDir = ''

  const stop = async () => {
    if (stopped)
      return
    stopped = true
    for (const line of refusedHostsReport(mockModel.refusedHosts()))
      process.stderr.write(`${line}\n`)
    await finishCleanup([
      proc ? stopProcess(proc) : Promise.resolve(),
      mockModel.close(),
    ])
    rmSync(dataDir, { recursive: true, force: true })
    if (socketDir)
      rmSync(socketDir, { recursive: true, force: true })
  }

  try {
    await bootstrapFirstAdmin(options.binaryPath, hubDataDir(dataDir))
    // Awaiting matters: the function runs each provider's CLI setup (Letta's
    // `backend local` / `connect`, which discover the mock's model catalog). A
    // caller that left it unawaited started the worker against an agent
    // environment whose setup was still in flight, and Letta's catalog was
    // empty.
    const mockAgent = await createMockAgentEnvironment(
      options.tmpDir,
      mockModel.url,
      process.env.HOME ? { realHomeDir: process.env.HOME } : {},
    )
    const log = openSync(serverLogPath, 'a')
    try {
      // The hub's local socket binds at a short path: macOS `sun_path` caps a
      // Unix socket at 104 bytes, and the run's data directory is already long.
      // `--listen` carries both addresses: an ephemeral TCP port (the operating
      // system assigns it and the hub reports it in its state file -- scanning
      // for a free port and rebinding it is a window another process can win)
      // and the local IPC socket at the short path.
      socketDir = mkdtempSync(join(tmpdir(), 'lm-e2e-sock-'))
      const localListen = `unix:${join(socketDir, 'hub.sock')}`
      proc = spawn(options.binaryPath, [
        'dev',
        '-listen',
        '127.0.0.1:0',
        '-listen',
        localListen,
        '-data-dir',
        dataDir,
      ], {
        stdio: ['ignore', log, log],
        env: hubSpawnEnv({
          ...agentDefaultsEnv(),
          ...mockAgent.env,
          LEAPMUX_WORKER_NAME: 'Local',
        }),
      })
      trackProcess(options.tmpDir, proc)
    }
    finally {
      closeSync(log)
    }

    // The hub writes its resolved bind set to <data-dir>/state.json once every
    // listener is bound; the TCP entry there is the port the browser reaches.
    const statePath = join(hubDataDir(dataDir), 'state.json')
    const hubUrl = hubUrlFromStateJson(await waitForHubStateFile(statePath, proc))
    await waitForSuiteServer(hubUrl, proc)
    const adminToken = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)
    const [adminUserId, workerId, newuserToken] = await Promise.all([
      getUserId(hubUrl, adminToken),
      getWorkerId(hubUrl, adminToken),
      signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com'),
    ])

    return {
      state: {
        hubUrl,
        adminToken,
        adminUserId,
        workerId,
        newuserToken,
        dataDir,
        serverLogPath,
        mockModelUrl: mockModel.url,
        piAgentDir: mockAgent.piAgentDir,
        agentEnv: mockAgent.env,
      },
      stop,
    }
  }
  catch (error) {
    const output = readLog(serverLogPath)
    await stop().catch((cleanupError) => {
      throw new AggregateError([error, cleanupError], `Shared server startup failed.\n${output}`)
    })
    if (output)
      throw new Error(`Shared server startup failed.\n${output}`, { cause: error })
    throw error
  }
}

/** Create the first administrator before the hub opens its database. */
async function bootstrapFirstAdmin(binaryPath: string, dataDir: string): Promise<void> {
  await execFileAsync(binaryPath, [
    'recover',
    'bootstrap',
    'create-admin',
    '--username',
    TEST_ADMIN_USERNAME,
    '--password',
    TEST_ADMIN_PASSWORD,
    '--display-name',
    TEST_ADMIN_DISPLAY_NAME,
    '--data-dir',
    dataDir,
  ], {
    env: { ...process.env, LEAPMUX_LOG_LEVEL: 'error' },
  })
}

/**
 * The browser-facing hub URL a state file names.
 *
 * The hub writes `<data-dir>/state.json` after every listener is bound, with
 * the resolved bind set in `listen`. The TCP entry is the one that is not a
 * local IPC URL; its port is the one the operating system assigned to the
 * `127.0.0.1:0` request. A bind set with no TCP entry (or with more than one)
 * is not a state this suite starts, so it fails rather than guessing.
 */
export function hubUrlFromStateJson(raw: string): string {
  const state = JSON.parse(raw) as { listen?: string[] }
  const listen = state.listen ?? []
  const tcp = listen.filter(entry => !entry.startsWith('unix:') && !entry.startsWith('npipe:'))
  const entry = tcp[0]
  if (tcp.length !== 1 || entry === undefined)
    throw new Error(`expected one TCP address in the hub state file's listen list, got ${JSON.stringify(listen)}`)
  const separator = entry.lastIndexOf(':')
  const port = entry.slice(separator + 1)
  if (separator < 0 || !/^\d+$/.test(port))
    throw new Error(`the TCP address ${entry} names no port`)
  return `http://${E2E_BROWSER_HOST}:${port}`
}

/**
 * Read the hub's state file once it appears, or fail when the process dies or
 * the deadline passes. The poll never sleeps a fixed time: a hub that binds
 * fast starts the suite fast.
 */
function waitForHubStateFile(statePath: string, proc: ChildProcess, timeoutMs = 30_000): Promise<string> {
  return new Promise((resolve, reject) => {
    let finished = false
    let retry: ReturnType<typeof setTimeout> | undefined
    const deadline = setTimeout(() => finish(undefined, new Error(`The hub wrote no state file at ${statePath} within ${timeoutMs}ms`)), timeoutMs)
    const onError = (error: Error) => finish(undefined, error)
    // The file wins over the exit: a process that writes its state file and
    // exits at once has still told the suite where the hub was.
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      try {
        finish(readFileSync(statePath, 'utf8'))
        return
      }
      catch {}
      finish(undefined, new Error(`The shared server exited before it wrote ${statePath}: code=${code ?? 'none'} signal=${signal ?? 'none'}`))
    }
    proc.once('error', onError)
    proc.once('exit', onExit)
    // A process that already exited never fires 'exit' again; without this
    // the waiter would hold its whole deadline for a dead server.
    if (proc.exitCode !== null || proc.signalCode !== null)
      onExit(proc.exitCode, proc.signalCode)

    function finish(content: string | undefined, error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      clearTimeout(retry)
      proc.removeListener('error', onError)
      proc.removeListener('exit', onExit)
      if (error)
        reject(error)
      else
        resolve(content as string)
    }

    function check() {
      try {
        finish(readFileSync(statePath, 'utf8'))
        return
      }
      catch {}
      if (!finished)
        retry = setTimeout(check, 25)
    }
    check()
  })
}

/** Wait until the server responds or its process exits. */
function waitForSuiteServer(url: string, proc: ChildProcess, timeoutMs = 30_000): Promise<void> {
  return new Promise((resolve, reject) => {
    let finished = false
    let retry: ReturnType<typeof setTimeout> | undefined
    let lastError: unknown
    const deadline = setTimeout(() => finish(new Error(`Server at ${url} did not start within ${timeoutMs}ms`, { cause: lastError })), timeoutMs)

    const onError = (error: Error) => finish(error)
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      finish(new Error(`The shared server at ${url} exited before startup completed: code=${code ?? 'none'} signal=${signal ?? 'none'}`))
    }
    proc.once('error', onError)
    proc.once('exit', onExit)
    // A process that already exited never fires 'exit' again; without this
    // the waiter would hold its whole deadline for a dead server.
    if (proc.exitCode !== null || proc.signalCode !== null)
      onExit(proc.exitCode, proc.signalCode)

    function finish(error?: Error) {
      if (finished)
        return
      finished = true
      clearTimeout(deadline)
      clearTimeout(retry)
      proc.removeListener('error', onError)
      proc.removeListener('exit', onExit)
      if (error)
        reject(error)
      else
        resolve()
    }

    async function check() {
      try {
        const response = await fetch(url)
        if (response.ok) {
          await response.body?.cancel()
          finish()
          return
        }
        await response.body?.cancel()
        lastError = new Error(`Startup request returned HTTP ${response.status}`)
      }
      catch (error) {
        lastError = error
      }
      if (!finished)
        retry = setTimeout(check, 25)
    }
    void check()
  })
}

function readLog(path: string): string {
  try {
    return readFileSync(path, 'utf8')
  }
  catch {
    return ''
  }
}
