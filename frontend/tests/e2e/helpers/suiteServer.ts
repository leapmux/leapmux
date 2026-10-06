import type { ChildProcess } from 'node:child_process'
import type { CustomizedHubSetting } from './api'
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
  listCustomizedHubSettingsViaAPI,
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
import { hubDataDir, hubSpawnEnv, waitForHubStart } from './server'

export interface SuiteServerState {
  hubUrl: string
  /** The resolved primary bind address that the hub publishes as its default URL. */
  boundHubUrl: string
  adminToken: string
  /**
   * The administrator user ID.
   * Browser storage requires an account ID before addInitScript can set a preference for a page that did not sign in yet.
   */
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
  /**
   * The hub settings that held a stored value when the hub started, before any test ran.
   * The hub stores them itself, such as its generated captcha key. The per-test reset keeps them.
   */
  baselineHubSettings: CustomizedHubSetting[]
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

/**
 * The failure that states how many model requests held the text of a sentinel instruction file, or '' when none did.
 * See ./ancestorInstructions.ts.
 */
export function ancestorInstructionsReport(count: number): string {
  if (count === 0)
    return ''
  return `The mock refused ${count} model ${count === 1 ? 'request' : 'requests'} that held the text of an instruction file above the working directory of an agent. The failed tests state each request; a request with no scenario marker is in the model server log.`
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
    const ancestorRequests = mockModel.ancestorInstructionRequests()
    await finishCleanup([
      proc ? stopProcess(proc) : Promise.resolve(),
      mockModel.close(),
    ])
    rmSync(dataDir, { recursive: true, force: true })
    if (socketDir)
      rmSync(socketDir, { recursive: true, force: true })
    // A request with a scenario marker already failed its test. A request with none fails no test, so the run fails here.
    const report = ancestorInstructionsReport(ancestorRequests)
    if (report)
      throw new Error(report)
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
    const { hubUrl, listen } = await waitForHubStart(join(hubDataDir(dataDir), 'state.json'), proc)
    const boundHubUrl = `http://${listen}`
    const adminToken = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
    await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)
    const [adminUserId, workerId, newuserToken, baselineHubSettings] = await Promise.all([
      getUserId(hubUrl, adminToken),
      getWorkerId(hubUrl, adminToken),
      signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com'),
      // The setup writes no hub setting, so this list holds only what the hub stored itself.
      listCustomizedHubSettingsViaAPI(hubUrl, adminToken),
    ])

    return {
      state: {
        hubUrl,
        boundHubUrl,
        adminToken,
        adminUserId,
        workerId,
        newuserToken,
        dataDir,
        serverLogPath,
        mockModelUrl: mockModel.url,
        piAgentDir: mockAgent.piAgentDir,
        agentEnv: mockAgent.env,
        baselineHubSettings,
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

function readLog(path: string): string {
  try {
    return readFileSync(path, 'utf8')
  }
  catch {
    return ''
  }
}
