import type { ChildProcess } from 'node:child_process'
/* eslint-disable no-console */
import type { ModelScript } from './helpers/modelScriptFixture'
import type { ServerOutput } from './helpers/serverOutput'
import type { WorkspaceFixture } from './helpers/workspace'
import { rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { test as base, expect } from '@playwright/test'
import { agentDefaultsEnv } from './agentSettings'
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
import { cleanupOnFailure, finishCleanup, withCleanup } from './helpers/cleanup'
import { closeAllUserEventsSubscriptions } from './helpers/crdt'
import { runModelScriptFixture } from './helpers/modelScriptFixture'
import { stopProcess, stopProcesses } from './helpers/process'
import { spawnTestProcess } from './helpers/processRegistry'
import { createTestDirectory } from './helpers/runDirectory'
import { getGlobalState, hubSpawnEnv, hubUrlFromStateJson, waitForHubReady, waitForHubStateFile, waitForServer } from './helpers/server'
import { createServerOutput, reportStartupFailure } from './helpers/serverOutput'
import { getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

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
   * The captured Hub and Worker output includes every restart.
   * Each line identifies its process. See {@link createServerOutput}.
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
 * Confirm the Worker connection with one HTTP request.
 * Restart the Worker if that request does not confirm its connection.
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
    // The status request failed. Restart the Worker after this failure.
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
    env: hubSpawnEnv({ ...agentDefaultsEnv(), LEAPMUX_WORKER_NAME: 'test-worker' }),
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
    env: hubSpawnEnv(agentDefaultsEnv()),
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
    testStartedAt: number
    modelScript: ModelScript
    toastRecorder: void
    workspace: WorkspaceFixture
    authenticatedWorkspace: WorkspaceFixture
  },
  {
    separateHubWorker: SeparateServerInfo
  }
>({
  // This automatic fixture has no dependencies. Read the test start before other fixtures run.
  // Playwright counts fixture setup in each test's deadline.
  // The model script uses this time to fail a stalled wait before that deadline.
  // eslint-disable-next-line no-empty-pattern
  testStartedAt: [async ({}, use) => {
    await use(Date.now())
  }, { auto: true }],

  // hubSpawnEnv supplies this run's isolated agent configuration to each Hub.
  // Every agent reaches the run's mock endpoint. Teardown verifies each test's model script.
  modelScript: async ({ testStartedAt }, use, testInfo) => runModelScriptFixture(use, testInfo, testStartedAt),

  // One Playwright worker owns this separate Hub and Worker.
  // eslint-disable-next-line no-empty-pattern
  separateHubWorker: [async ({}, use) => {
    const globalState = getGlobalState()
    const dataDir = createTestDirectory('leapmux-e2e-separate-')
    const hubDataDir = join(dataDir, 'hub')
    const workerDataDir = join(dataDir, 'worker')
    let hubUrl = ''

    // Keep the Hub and Worker output together. Each line identifies its process.
    const output = createServerOutput()
    const started: ChildProcess[] = []
    let serverInfo: SeparateServerInfo | undefined
    await withCleanup(async () => {
      console.log('[e2e] Start the separate Hub on an assigned port.')
      // A separate process group protects the Hub from signals to the test runner.
      const hubProc = spawnTestProcess(globalState.binaryPath, [
        'hub',
        '-listen',
        '127.0.0.1:0',
        '-data-dir',
        hubDataDir,
      ], {
        stdio: ['ignore', 'pipe', 'pipe'],
        detached: true,
        env: hubSpawnEnv(agentDefaultsEnv()),
      })
      started.push(hubProc)
      hubProc.unref()
      output.capture(hubProc, 'hub')

      const state = await waitForHubStateFile(join(hubDataDir, 'state.json'), hubProc)
        .catch(error => reportStartupFailure(output, 'Hub state file', error))
      hubUrl = hubUrlFromStateJson(state)
      const hubPort = Number(new URL(hubUrl).port)
      await waitForHubReady(hubUrl, hubProc)
        .catch(error => reportStartupFailure(output, `Hub on port ${hubPort}`, error))
      console.log(`[e2e] The separate Hub is ready on port ${hubPort}.`)

      // A Hub with no users makes its first registered user an administrator.
      // This first signup does not require the open-signup setting.
      const adminToken = await signUpViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)

      // Elevate the session before changing hub settings. Signup alone does not grant session elevation.
      await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)

      // Enable signup before creating newuser. A standalone hub defaults to closed signup after the first administrator exists.
      await enableSignupViaAPI(hubUrl, adminToken)

      // Create the registration key as an administrator and pass it to the worker.
      // PR #216 removed the previous worker-token approval flow.
      const registrationKey = await mintRegistrationKeyViaAPI(hubUrl, adminToken)

      // Read the online Worker IDs before startup. Identify the new Worker from the added ID.
      const beforeIds = new Set(await listOnlineWorkerIDsViaAPI(hubUrl, adminToken))

      // A separate process group protects the Worker from signals to the test runner.
      console.log('[e2e] Start the separate Worker.')
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
        env: hubSpawnEnv({ ...agentDefaultsEnv(), LEAPMUX_WORKER_NAME: 'test-worker' }),
      })
      workerProc.unref()
      started.push(workerProc)
      output.capture(workerProc, 'worker')

      // Startup waits run outside a test. Print recent server output on failure because no test attachment exists yet.
      const workerId = await waitForNewOnlineWorkerViaAPI(hubUrl, adminToken, beforeIds)
        .catch(err => reportStartupFailure(output, 'worker registration', err))
      console.log(`[e2e] The separate Worker is connected: ${workerId}.`)

      // Register the second test user.
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
    }, async () => {
      const active = serverInfo ? [serverInfo.workerProc, serverInfo.hubProc] : started
      // Close subscriptions even when setup or a restart fails.
      await finishCleanup([
        closeAllUserEventsSubscriptions(),
        hubUrl ? closeTestChannels(hubUrl) : Promise.resolve(),
        stopProcesses(active),
      ])
      rmSync(dataDir, { recursive: true, force: true })
    })
  }, { scope: 'worker' }],

  baseURL: async ({ separateHubWorker }, use) => {
    await use(separateHubWorker.hubUrl)
  },

  // Record toasts for every test.
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

    // Attach recent Hub and Worker output after a failure, as ./fixtures.ts does.
    // Both processes can fail without a browser error. Their logs explain a timeout on a browser locator.
    // Use a file attachment. The list reporter shows only the first line of an inline attachment.
    // The test output directory keeps the complete file.
    if (testInfo.status !== testInfo.expectedStatus) {
      const logPath = testInfo.outputPath('server-log.txt')
      writeFileSync(logPath, separateHubWorker.output.since(serverMark))
      await testInfo.attach('server-log', { path: logPath, contentType: 'text/plain' })
    }
  }, { auto: true }],

  // Confirm the Worker connection before creating the workspace and its initial agent.
  workspace: async ({ separateHubWorker }, use) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    await withTestWorkspace(separateHubWorker, 'e2e', async (workspace) => {
      await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, workspace.workspaceId)
      await use(workspace)
    })
  },

  // Sign in and open the test workspace.
  authenticatedWorkspace: async ({ page, workspace, separateHubWorker }, use) => {
    await loginViaToken(page, separateHubWorker.adminToken)
    await openWorkspace(page, workspace.workspaceId)
    await use(workspace)
  },
})

export { expect }
