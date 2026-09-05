/* eslint-disable no-console */
import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import type { ServerOutput } from './helpers/serverOutput'
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { test as base, expect } from '@playwright/test'
import {
  authedHeaders,
  cleanupWorkspaceViaAPI,
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  elevateSessionViaAPI,
  enableSignupViaAPI,
  listOnlineWorkerIDsViaAPI,
  loginViaAPI,
  mintRegistrationKeyViaAPI,
  openPinnedModeAgentViaAPI,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
  waitForNewOnlineWorkerViaAPI,
} from './helpers/api'
import { findFreePort, getGlobalState, hubSpawnEnv, waitForServer } from './helpers/server'
import { createServerOutput, reportStartupFailure } from './helpers/serverOutput'
import { getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'

// Per-fixture PID registry written under `<dataDir>/pids.json`. The
// global-teardown sweep reads this file and reaps any process that
// survived a crash (timeout, ungraceful worker shutdown, restart that
// throws before the code updates mutableState) before the Playwright run
// exits. We append rather than overwrite so a sequence of restart
// calls leaves a full history; the teardown loops over `pids` and
// silently ignores already-dead entries.
const PIDS_FILE = 'pids.json'

export function trackSpawnedPid(dataDir: string, pid: number): void {
  const path = join(dataDir, PIDS_FILE)
  let pids: number[] = []
  if (existsSync(path)) {
    try {
      pids = JSON.parse(readFileSync(path, 'utf-8'))
    }
    catch { /* corrupted; start over */ }
  }
  if (!pids.includes(pid))
    pids.push(pid)
  writeFileSync(path, JSON.stringify(pids))
}

export interface SeparateServerInfo {
  hubUrl: string
  adminToken: string
  workerId: string
  newuserToken: string
  hubProc: ChildProcess
  workerProc: ChildProcess
  hubPid: number
  workerPid: number
  dataDir: string
  binaryPath: string
  hubPort: number
  /**
   * Captured stdout+stderr from BOTH processes, labelled per process and
   * spanning every restart. See {@link createServerOutput}.
   */
  output: ServerOutput
}

interface WorkspaceFixture {
  workspaceId: string
}

// Mutable state that tests can modify via stop/restart helpers
interface MutableState {
  hubPid: number
  workerPid: number
  hubProc: ChildProcess
  workerProc: ChildProcess
}

let mutableState: MutableState | null = null

export function getMutableState(): MutableState {
  if (!mutableState)
    throw new Error('SeparateHubWorker fixture not initialized')
  return mutableState
}

/**
 * Stop the worker process without restarting.
 */
export async function stopWorker() {
  const state = getMutableState()
  try {
    process.kill(state.workerPid, 'SIGTERM')
  }
  catch {
    // Process may already be dead
  }
  await new Promise(r => setTimeout(r, 2000))
}

/**
 * Wait for the hub to confirm the worker is offline.
 */
export async function waitForWorkerOffline(hubUrl: string, adminToken: string, timeout = 30_000) {
  const start = Date.now()
  while (Date.now() - start < timeout) {
    try {
      const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
        method: 'POST',
        headers: authedHeaders(adminToken),
        body: JSON.stringify({}),
      })
      if (res.ok) {
        const data = await res.json() as { workers: Array<{ online: boolean }> }
        if (data.workers.every(b => !b.online)) {
          return
        }
      }
    }
    catch {
      // Ignore errors during polling
    }
    await new Promise(r => setTimeout(r, 500))
  }
  throw new Error('Timed out waiting for worker to go offline')
}

/**
 * Ensure the worker is online, restarting it if needed.
 * Lightweight when the worker is already online (single HTTP request).
 */
export async function ensureWorkerOnline(serverInfo: SeparateServerInfo) {
  try {
    const res = await fetch(`${serverInfo.hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
      method: 'POST',
      headers: authedHeaders(serverInfo.adminToken),
      body: JSON.stringify({}),
    })
    if (res.ok) {
      const data = await res.json() as { workers: Array<{ id: string, online: boolean }> }
      if (data.workers.some(w => w.id === serverInfo.workerId && w.online))
        return
    }
  }
  catch {
    // Hub might be unresponsive; fall through to restart
  }
  await restartWorker(serverInfo)
}

/**
 * Restart the worker process.
 */
export async function restartWorker(serverInfo: SeparateServerInfo) {
  await stopWorker()

  const workerDataDir = join(serverInfo.dataDir, 'worker')
  const workerProc = spawn(serverInfo.binaryPath, [
    'worker',
    '-hub',
    serverInfo.hubUrl,
    '-data-dir',
    workerDataDir,
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
    env: { ...process.env, LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet', LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low', LEAPMUX_WORKER_NAME: 'test-worker' },
  })
  workerProc.unref()
  // Track immediately so the fixture teardown / global-teardown sweep still
  // cleans up after a crash that precedes the state update.
  trackSpawnedPid(serverInfo.dataDir, workerProc.pid!)
  // The REPLACEMENT worker feeds the same buffer, so a test that restarts one
  // gets both halves of its own window rather than losing everything the new
  // process says.
  serverInfo.output.capture(workerProc, 'worker')

  try {
    // Wait for the worker to connect to the hub
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('Worker restart timed out')), 30_000)
      const onData = (chunk: Buffer) => {
        const text = chunk.toString()
        if (text.includes('connected to hub')) {
          clearTimeout(timeout)
          workerProc.stderr?.off('data', onData)
          workerProc.stdout?.off('data', onData)
          resolve()
        }
      }
      workerProc.stderr?.on('data', onData)
      workerProc.stdout?.on('data', onData)
    })
  }
  catch (err) {
    // The new worker did not start in time; kill it so it doesn't leak.
    // Without this, mutableState still points at the previous (already
    // stopped) PID and the fixture teardown sees no surviving process,
    // leaving the just-spawned one orphaned for the OS.
    try {
      workerProc.kill('SIGKILL')
    }
    catch { /* already dead */ }
    throw err
  }

  // Update mutable state
  const state = getMutableState()
  state.workerPid = workerProc.pid!
  state.workerProc = workerProc

  // Give the hub a moment to fully register the reconnected worker
  await new Promise(r => setTimeout(r, 1000))
}

/**
 * Stop the hub process without restarting.
 */
export async function stopHub() {
  const state = getMutableState()
  try {
    process.kill(state.hubPid, 'SIGTERM')
  }
  catch {
    // Process may already be dead
  }
  await new Promise(r => setTimeout(r, 2000))
}

/**
 * Restart the hub process.
 */
export async function restartHub(serverInfo: SeparateServerInfo) {
  await stopHub()

  const hubDataDir = join(serverInfo.dataDir, 'hub')
  const hubProc = spawn(serverInfo.binaryPath, [
    'hub',
    '-listen',
    `:${serverInfo.hubPort}`,
    '-data-dir',
    hubDataDir,
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
    env: hubSpawnEnv({ LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet', LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low' }),
  })
  hubProc.unref()
  // Track immediately so the fixture teardown / global-teardown sweep still
  // cleans up after a crash that precedes the state update.
  trackSpawnedPid(serverInfo.dataDir, hubProc.pid!)
  // Capture rather than `resume()`: both drain the stream, and only one of them
  // keeps what a hub that fails its health check below just said.
  serverInfo.output.capture(hubProc, 'hub')

  try {
    // Wait for the hub to become ready
    await waitForServer(serverInfo.hubUrl)

    // Verify the hub is fully operational by testing login
    for (let i = 0; i < 10; i++) {
      try {
        await loginViaAPI(serverInfo.hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
        break
      }
      catch {
        if (i === 9)
          throw new Error('Hub restart: login health check failed')
        await new Promise(r => setTimeout(r, 500))
      }
    }
  }
  catch (err) {
    // Same rationale as restartWorker: a startup failure must not leave
    // the just-spawned hub orphaned. mutableState still points at the
    // stopped predecessor, so fixture teardown wouldn't see this one.
    try {
      hubProc.kill('SIGKILL')
    }
    catch { /* already dead */ }
    throw err
  }

  // Update mutable state
  const state = getMutableState()
  state.hubPid = hubProc.pid!
  state.hubProc = hubProc

  await new Promise(r => setTimeout(r, 1000))
}

export const processTest = base.extend<
  {
    toastRecorder: void
    workspace: WorkspaceFixture
    authenticatedWorkspace: WorkspaceFixture
  },
  {
    separateHubWorker: SeparateServerInfo
  }
>({
  // Worker-scoped fixture: spawns separate hub + worker per test file
  // eslint-disable-next-line no-empty-pattern
  separateHubWorker: [async ({}, use) => {
    const globalState = getGlobalState()
    const dataDir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-separate-'))
    const hubDataDir = join(dataDir, 'hub')
    const workerDataDir = join(dataDir, 'worker')
    const hubPort = await findFreePort()
    const hubUrl = `http://localhost:${hubPort}`

    console.log(`[e2e] Starting separate hub on port ${hubPort}...`)

    // ONE buffer for the hub AND the worker, labelled per process: their lines
    // interleave in real time, and a reader chasing a worker-side failure needs
    // the hub's answer to the same request beside it.
    const output = createServerOutput()

    // Start hub in its own process group so stray signals from the test
    // runner's process group don't kill it prematurely.
    const hubProc = spawn(globalState.binaryPath, [
      'hub',
      '-listen',
      `:${hubPort}`,
      '-data-dir',
      hubDataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      detached: true,
      env: hubSpawnEnv({ LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet', LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low' }),
    })
    hubProc.unref()
    trackSpawnedPid(dataDir, hubProc.pid!)
    output.capture(hubProc, 'hub')

    await waitForServer(hubUrl).catch(err => reportStartupFailure(output, `hub on port ${hubPort}`, err))
    console.log(`[e2e] Hub ready on port ${hubPort}`)

    // Create the admin. A hub with no users at all accepts one sign-up and
    // makes it an administrator, so this account needs no open-signup setting.
    const adminToken = await signUpViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)

    // Every hub-settings write demands an elevated session, and the very next
    // line is one. A sign-up mints a session no more elevated than a fresh
    // login does, so this fixture elevates for the same reason
    // `mintCLITokenForAdmin` does.
    await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)

    // Every LATER sign-up does. This is a plain `leapmux hub`, not `leapmux
    // dev`, and `signup_enabled` resolves from its stored row with a closed
    // code default -- only dev mode reports it open. So the `newuser` sign-up
    // below answered `failed_precondition: sign-up is disabled`, the fixture
    // threw, and every test in the nine spec files that use this fixture
    // failed with it.
    await enableSignupViaAPI(hubUrl, adminToken)

    // Mint a registration key (new flow: admin creates key, hands it to
    // worker via --registration-key). PR #216 removed the old self-serve
    // token flow (worker prints token, admin approves).
    const registrationKey = await mintRegistrationKeyViaAPI(hubUrl, adminToken)

    // Snapshot online workers BEFORE spawning so we can identify the
    // new worker by diffing.
    const beforeIds = new Set(await listOnlineWorkerIDsViaAPI(hubUrl, adminToken))

    // Start worker in its own process group so stray signals from the test
    // runner's process group don't kill it prematurely.
    console.log('[e2e] Starting separate worker...')
    const workerProc = spawn(globalState.binaryPath, [
      'worker',
      '--hub',
      hubUrl,
      '--registration-key',
      registrationKey,
      '--data-dir',
      workerDataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      detached: true,
      env: { ...process.env, LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet', LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low', LEAPMUX_WORKER_NAME: 'test-worker' },
    })
    workerProc.unref()
    trackSpawnedPid(dataDir, workerProc.pid!)
    output.capture(workerProc, 'worker')

    // Both waits run OUTSIDE any test, so a failure has no test to attach the
    // tail to -- `reportStartupFailure` prints it instead. Without it a worker
    // that refused its registration key reports as a bare "Timed out waiting
    // for worker".
    const workerId = await waitForNewOnlineWorkerViaAPI(hubUrl, adminToken, beforeIds)
      .catch(err => reportStartupFailure(output, 'worker registration', err))
    console.log(`[e2e] Worker connected: ${workerId}`)

    // Create newuser
    const newuserToken = await signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com')

    // Save state path for backward compatibility with loginViaUI which reads token from localStorage
    const stateForFile = {
      tmpDir: dataDir,
      hubPid: hubProc.pid,
      workerPid: workerProc.pid,
      hubUrl,
      adminToken,
      workerId,
      newuserToken,
    }
    writeFileSync(join(dataDir, 'state.json'), JSON.stringify(stateForFile, null, 2))

    const serverInfo: SeparateServerInfo = {
      hubUrl,
      adminToken,
      workerId,
      newuserToken,
      hubProc,
      workerProc,
      hubPid: hubProc.pid!,
      workerPid: workerProc.pid!,
      dataDir,
      binaryPath: globalState.binaryPath,
      hubPort,
      output,
    }

    // Set mutable state for stop/restart helpers
    mutableState = {
      hubPid: hubProc.pid!,
      workerPid: workerProc.pid!,
      hubProc,
      workerProc,
    }

    await use(serverInfo)

    // Teardown: kill both processes
    try {
      process.kill(mutableState.workerPid, 'SIGTERM')
    }
    catch { /* already dead */ }
    try {
      process.kill(mutableState.hubPid, 'SIGTERM')
    }
    catch { /* already dead */ }
    await new Promise(r => setTimeout(r, 1000))
    try {
      process.kill(mutableState.workerPid, 'SIGKILL')
    }
    catch { /* already dead */ }
    try {
      process.kill(mutableState.hubPid, 'SIGKILL')
    }
    catch { /* already dead */ }
    rmSync(dataDir, { recursive: true, force: true })
    mutableState = null
    console.log(`[e2e] Separate hub+worker on port ${hubPort} stopped`)
  }, { scope: 'worker' }],

  // Override page to set baseURL dynamically
  page: async ({ separateHubWorker, browser }, use) => {
    const context = await browser.newContext({
      baseURL: separateHubWorker.hubUrl,
    })
    const page = await context.newPage()
    await use(page)
    await context.close()
  },

  // Toast recorder: auto-use so it runs for every test
  toastRecorder: [async ({ page, separateHubWorker }, use, testInfo) => {
    await installToastRecorder(page)
    const serverMark = separateHubWorker.output.mark()
    await use()

    const toasts = await getRecordedToasts(page).catch(() => [])
    if (toasts.length > 0) {
      const toastLog = toasts.map(t =>
        `[${new Date(t.timestamp).toISOString()}] [${t.variant || 'info'}] ${t.message}`,
      ).join('\n')
      await testInfo.attach('toast-log', {
        body: toastLog,
        contentType: 'text/plain',
      })
    }

    // A failing test gets the hub's and the worker's recent output, exactly as
    // the dev-instance fixture does (see fixtures.ts). Both run out of process,
    // so their errors are otherwise invisible and a worker-side failure
    // surfaces only as a timeout on an unrelated locator.
    //
    // The fixture attaches it as a FILE, not as a body: the list reporter
    // truncates an inline attachment to its first line, which is the startup
    // banner and nothing else. A path puts the whole tail under test-results/,
    // where a reader can actually open it.
    if (testInfo.status !== testInfo.expectedStatus) {
      const logPath = testInfo.outputPath('server-log.txt')
      writeFileSync(logPath, separateHubWorker.output.since(serverMark))
      await testInfo.attach('server-log', { path: logPath, contentType: 'text/plain' })
    }
  }, { auto: true }],

  // Workspace fixture — ensure worker is online before creating workspace + initial agent
  workspace: async ({ separateHubWorker }, use) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
    )
    await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    await use({ workspaceId })
    // Stop the workspace's agents on the worker BEFORE the hub soft-delete -- the
    // same cascade the browser app runs (deleteWorkspaceViaAPI only does the hub
    // half). Without it the worker keeps every test's Claude CLI subprocess alive;
    // these accumulate on the shared separateHubWorker across the suite and
    // exhaust resources, which makes later settings-menu interactions flaky.
    // Best effort.
    try {
      await cleanupWorkspaceViaAPI(hubUrl, adminToken, workerId, workspaceId)
    }
    catch { /* best effort */ }
    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  // Authenticated workspace
  authenticatedWorkspace: async ({ page, workspace, separateHubWorker }, use) => {
    await loginViaToken(page, separateHubWorker.adminToken)
    await openWorkspace(page, workspace.workspaceId)
    await use(workspace)
  },
})

export { expect }
