import type { ChildProcess } from 'node:child_process'
/* eslint-disable no-console */
import type { ServerOutput } from './helpers/serverOutput'
import type { WorkspaceFixture } from './helpers/workspace'
import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { test as base, expect } from '@playwright/test'
import {
  API_POLL_INTERVAL_MS,
  authedHeaders,
  closeTestChannels,
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
import { cleanupOnFailure, finishCleanup } from './helpers/cleanup'
import { closeAllUserEventsSubscriptions } from './helpers/crdt'
import { stopProcess, stopProcesses } from './helpers/process'
import { spawnTestProcess } from './helpers/processRegistry'
import { createTestDirectory } from './helpers/runDirectory'
import { findFreePort, getGlobalState, hubSpawnEnv, waitForServer } from './helpers/server'
import { createServerOutput, reportStartupFailure } from './helpers/serverOutput'
import { getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'
import { realAgentEnv } from './realAgentSettings'

export interface SeparateServerInfo {
  hubUrl: string
  adminToken: string
  workerId: string
  newuserToken: string
  hubProc: ChildProcess
  workerProc: ChildProcess
  dataDir: string
  binaryPath: string
  hubPort: number
  /**
   * Captured stdout+stderr from BOTH processes, labelled per process and
   * spanning every restart. See {@link createServerOutput}.
   */
  output: ServerOutput
}

/** Stop the worker and wait for its process to exit. */
export async function stopWorker(serverInfo: SeparateServerInfo): Promise<void> {
  await stopProcess(serverInfo.workerProc)
}

async function waitForWorkerState(serverInfo: SeparateServerInfo, online: boolean, timeout = 30_000): Promise<void> {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    const ids = await listOnlineWorkerIDsViaAPI(serverInfo.hubUrl, serverInfo.adminToken)
    if (ids.includes(serverInfo.workerId) === online)
      return
    await new Promise(resolve => setTimeout(resolve, API_POLL_INTERVAL_MS))
  }
  throw new Error(`Worker ${serverInfo.workerId} did not become ${online ? 'online' : 'offline'}`)
}

/** Wait for the hub to report this worker as offline. */
export async function waitForWorkerOffline(serverInfo: SeparateServerInfo, timeout = 30_000): Promise<void> {
  await waitForWorkerState(serverInfo, false, timeout)
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

/** Restart the worker and wait for its new connection. */
export async function restartWorker(serverInfo: SeparateServerInfo): Promise<void> {
  await stopWorker(serverInfo)
  await waitForWorkerOffline(serverInfo)

  const workerProc = spawnTestProcess(serverInfo.binaryPath, [
    'worker',
    '-hub',
    serverInfo.hubUrl,
    '-data-dir',
    join(serverInfo.dataDir, 'worker'),
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
    env: { ...process.env, ...realAgentEnv(), LEAPMUX_WORKER_NAME: 'test-worker' },
  })
  workerProc.unref()
  serverInfo.workerProc = workerProc
  serverInfo.output.capture(workerProc, 'worker')
  await cleanupOnFailure(async () => {
    await waitForWorkerState(serverInfo, true)
  }, () => stopProcess(workerProc))
    .catch(error => reportStartupFailure(serverInfo.output, 'worker restart', error))
}

/** Stop the hub and wait for its process to exit. */
export async function stopHub(serverInfo: SeparateServerInfo): Promise<void> {
  await stopProcess(serverInfo.hubProc)
}

/** Restart the hub and verify that its authentication handler responds. */
export async function restartHub(serverInfo: SeparateServerInfo): Promise<void> {
  await stopHub(serverInfo)
  const hubProc = spawnTestProcess(serverInfo.binaryPath, [
    'hub',
    '-listen',
    `:${serverInfo.hubPort}`,
    '-data-dir',
    join(serverInfo.dataDir, 'hub'),
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
    env: hubSpawnEnv(realAgentEnv()),
  })
  hubProc.unref()
  serverInfo.hubProc = hubProc
  serverInfo.output.capture(hubProc, 'hub')
  await cleanupOnFailure(async () => {
    await waitForServer(serverInfo.hubUrl)
    await loginViaAPI(serverInfo.hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
  }, () => stopProcess(hubProc))
    .catch(error => reportStartupFailure(serverInfo.output, 'hub restart', error))
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
    const dataDir = createTestDirectory('leapmux-e2e-separate-')
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
    const hubProc = spawnTestProcess(globalState.binaryPath, [
      'hub',
      '-listen',
      `:${hubPort}`,
      '-data-dir',
      hubDataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      detached: true,
      env: hubSpawnEnv(realAgentEnv()),
    })
    hubProc.unref()
    output.capture(hubProc, 'hub')

    const started = [hubProc]
    let serverInfo: SeparateServerInfo | undefined
    try {
      await waitForServer(hubUrl).catch(err => reportStartupFailure(output, `hub on port ${hubPort}`, err))
      console.log(`[e2e] Hub ready on port ${hubPort}`)

      // Create the admin. A hub with no users at all accepts one sign-up and
      // makes it an administrator, so this account needs no open-signup setting.
      const adminToken = await signUpViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)

      // Elevate the session before changing hub settings. Signup alone does not grant session elevation.
      await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)

      // Enable signup before creating newuser. A standalone hub defaults to closed signup after the first administrator exists.
      await enableSignupViaAPI(hubUrl, adminToken)

      // Create the registration key as an administrator and pass it to the worker.
      // PR #216 removed the previous worker-token approval flow.
      const registrationKey = await mintRegistrationKeyViaAPI(hubUrl, adminToken)

      // Snapshot online workers BEFORE spawning so we can identify the
      // new worker by diffing.
      const beforeIds = new Set(await listOnlineWorkerIDsViaAPI(hubUrl, adminToken))

      // Start worker in its own process group so stray signals from the test
      // runner's process group don't kill it prematurely.
      console.log('[e2e] Starting separate worker...')
      const workerProc = spawnTestProcess(globalState.binaryPath, [
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
        env: { ...process.env, ...realAgentEnv(), LEAPMUX_WORKER_NAME: 'test-worker' },
      })
      workerProc.unref()
      started.push(workerProc)
      output.capture(workerProc, 'worker')

      // Startup waits run outside a test. Print recent server output on failure because no test attachment exists yet.
      const workerId = await waitForNewOnlineWorkerViaAPI(hubUrl, adminToken, beforeIds)
        .catch(err => reportStartupFailure(output, 'worker registration', err))
      console.log(`[e2e] Worker connected: ${workerId}`)

      // Create newuser
      const newuserToken = await signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com')

      serverInfo = {
        hubUrl,
        adminToken,
        workerId,
        newuserToken,
        hubProc,
        workerProc,
        dataDir,
        binaryPath: globalState.binaryPath,
        hubPort,
        output,
      }

      await use(serverInfo)
    }
    finally {
      const active = serverInfo ? [serverInfo.workerProc, serverInfo.hubProc] : started
      // Close subscriptions even when setup or a restart fails.
      await finishCleanup([
        closeAllUserEventsSubscriptions(),
        closeTestChannels(hubUrl),
        stopProcesses(active),
      ])
      rmSync(dataDir, { recursive: true, force: true })
    }
  }, { scope: 'worker' }],

  baseURL: async ({ separateHubWorker }, use) => {
    await use(separateHubWorker.hubUrl)
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
    await withTestWorkspace(separateHubWorker, 'e2e', async (workspace) => {
      await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, workspace.workspaceId)
      await use(workspace)
    })
  },

  // Authenticated workspace
  authenticatedWorkspace: async ({ page, workspace, separateHubWorker }, use) => {
    await loginViaToken(page, separateHubWorker.adminToken)
    await openWorkspace(page, workspace.workspaceId)
    await use(workspace)
  },
})

export { expect }
