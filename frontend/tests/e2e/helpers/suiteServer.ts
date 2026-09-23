import type { ChildProcess } from 'node:child_process'
import { execFile, spawn } from 'node:child_process'
import { closeSync, mkdtempSync, openSync, readFileSync, rmSync } from 'node:fs'
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
import { E2E_BROWSER_HOST, findFreePort, hubDataDir, hubSpawnEnv } from './server'

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

  const stop = async () => {
    if (stopped)
      return
    stopped = true
    await finishCleanup([
      proc ? stopProcess(proc) : Promise.resolve(),
      mockModel.close(),
    ])
    rmSync(dataDir, { recursive: true, force: true })
  }

  try {
    await bootstrapFirstAdmin(options.binaryPath, hubDataDir(dataDir))
    const port = await findFreePort()
    const hubUrl = `http://${E2E_BROWSER_HOST}:${port}`
    const mockAgent = createMockAgentEnvironment(
      options.tmpDir,
      mockModel.url,
      process.env.HOME ? { realHomeDir: process.env.HOME } : {},
    )
    const log = openSync(serverLogPath, 'a')
    try {
      proc = spawn(options.binaryPath, [
        'dev',
        '-listen',
        `:${port}`,
        '-data-dir',
        dataDir,
      ], {
        stdio: ['ignore', log, log],
        env: hubSpawnEnv({ ...agentDefaultsEnv(), ...mockAgent.env, LEAPMUX_WORKER_NAME: 'Local' }),
      })
      trackProcess(options.tmpDir, proc)
    }
    finally {
      closeSync(log)
    }

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

/** Wait until the server responds or its process exits. */
function waitForSuiteServer(url: string, proc: ChildProcess, timeoutMs = 30_000): Promise<void> {
  return new Promise((resolve, reject) => {
    let finished = false
    let retry: ReturnType<typeof setTimeout> | undefined
    let lastError: unknown
    const deadline = setTimeout(() => finish(new Error(`Server at ${url} did not start within ${timeoutMs}ms`, { cause: lastError })), timeoutMs)

    const onError = (error: Error) => finish(error)
    const onExit = (code: number | null, signal: NodeJS.Signals | null) => {
      finish(new Error(`The shared server exited before startup completed: code=${code ?? 'none'} signal=${signal ?? 'none'}`))
    }
    proc.once('error', onError)
    proc.once('exit', onExit)

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
