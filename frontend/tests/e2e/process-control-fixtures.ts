import type { ChildProcess } from 'node:child_process'
/* eslint-disable no-console */
import type { ModelScriptFixtures } from './helpers/modelScriptFixture'
import type { ServerOutput } from './helpers/serverOutput'
import type { AgentWorkspaceFixture } from './helpers/workspace'
import { rmSync } from 'node:fs'
import { join } from 'node:path'
import { test as base, expect } from '@playwright/test'
import { agentDefaultsEnv } from './agentSettings'
import {
  API_POLL_INTERVAL_MS,
  closeTestChannels,
  elevateSessionViaAPI,
  enableSignupViaAPI,
  listOnlineWorkerIDsViaAPI,
  listWorkersViaAPI,
  loginViaAPI,
  openPinnedModeAgentViaAPI,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './helpers/api'
import { cleanupOnFailure, finishCleanup, withCleanup } from './helpers/cleanup'
import { closeAllUserEventsSubscriptions } from './helpers/crdt'
import { modelScriptFixtures } from './helpers/modelScriptFixture'
import { spawnRegisteredWorker, spawnWorkerProcess } from './helpers/nativeWorker'
import { stopProcess, stopProcesses } from './helpers/process'
import { spawnTestProcess } from './helpers/processRegistry'
import { createTestDirectory } from './helpers/runDirectory'
import { getGlobalState, hubSpawnEnv, waitForHubStart, waitForServer } from './helpers/server'
import { attachServerLog, createServerOutput, reportStartupFailure } from './helpers/serverOutput'
import { attachToastLog, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { agentWorkspaceFixture, withTestWorkspace } from './helpers/workspace'

/** The name of the Worker of the separate Hub. The output capture uses it as the label of the Worker lines also. */
export const SEPARATE_WORKER_NAME = 'test-worker'

export interface SeparateServerInfo {
  hubUrl: string
  adminToken: string
  workerId: string
  newuserToken: string
  hubProc: ChildProcess
  workerProc: ChildProcess
  dataDir: string
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
    const workers = await listWorkersViaAPI(serverInfo.hubUrl, serverInfo.adminToken)
    if (workers.some(worker => worker.id === serverInfo.workerId && worker.online))
      return
  }
  catch {
    // The status request failed. Restart the Worker after this failure.
  }
  await restartWorker(serverInfo)
}

/**
 * Restart the worker and wait for its new connection.
 * The Worker keeps its registration in its data directory, so the restart needs no registration key.
 */
export async function restartWorker(serverInfo: SeparateServerInfo): Promise<void> {
  await stopWorker(serverInfo)
  await waitForWorkerOffline(serverInfo)

  const workerProc = spawnWorkerProcess({
    hubUrl: serverInfo.hubUrl,
    name: SEPARATE_WORKER_NAME,
    dataDir: join(serverInfo.dataDir, 'worker'),
    env: agentDefaultsEnv(),
    output: serverInfo.output,
    detached: true,
  })
  serverInfo.workerProc = workerProc
  await cleanupOnFailure(async () => {
    await waitForWorkerState(serverInfo, true)
  }, () => stopProcess(workerProc))
    .catch(error => reportStartupFailure(serverInfo.output, 'worker restart', error))
}

/** Stop the hub and wait for its process to exit. */
export async function stopHub(serverInfo: SeparateServerInfo): Promise<void> {
  await stopProcess(serverInfo.hubProc)
}

/**
 * Spawn the separate Hub with its data below `dataDir`, and capture its output.
 * A separate process group protects the Hub from a signal to the test runner, and the test process exits without a
 * wait for it.
 */
function spawnSeparateHub(listen: string, dataDir: string, output: ServerOutput): ChildProcess {
  const hubProc = spawnTestProcess(getGlobalState().binaryPath, [
    'hub',
    '-listen',
    listen,
    '-data-dir',
    join(dataDir, 'hub'),
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
    env: hubSpawnEnv(agentDefaultsEnv()),
  })
  hubProc.unref()
  output.capture(hubProc, 'hub')
  return hubProc
}

/** Restart the hub on its port and verify that its authentication handler responds. */
export async function restartHub(serverInfo: SeparateServerInfo): Promise<void> {
  await stopHub(serverInfo)
  const hubProc = spawnSeparateHub(`:${serverInfo.hubPort}`, serverInfo.dataDir, serverInfo.output)
  serverInfo.hubProc = hubProc
  await cleanupOnFailure(async () => {
    await waitForServer(serverInfo.hubUrl)
    await loginViaAPI(serverInfo.hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
  }, () => stopProcess(hubProc))
    .catch(error => reportStartupFailure(serverInfo.output, 'hub restart', error))
}

export const processTest = base.extend<
  ModelScriptFixtures & {
    toastRecorder: void
    /** The working directory of the agent that the `workspace` fixture opens. See the same option in `./fixtures.ts`. */
    agentWorkingDir: string | undefined
    workspace: AgentWorkspaceFixture
    authenticatedWorkspace: AgentWorkspaceFixture
  },
  {
    separateHubWorker: SeparateServerInfo
  }
>({
  // hubSpawnEnv supplies this run's isolated agent configuration to each Hub.
  // Every agent reaches the run's mock endpoint. Teardown verifies each test's model script.
  ...modelScriptFixtures,

  // One Playwright worker owns this separate Hub and Worker.
  // eslint-disable-next-line no-empty-pattern
  separateHubWorker: [async ({}, use) => {
    const dataDir = createTestDirectory('leapmux-e2e-separate-')
    const workerDataDir = join(dataDir, 'worker')
    let hubUrl = ''

    // Keep the Hub and Worker output together. Each line identifies its process.
    const output = createServerOutput()
    const started: ChildProcess[] = []
    let serverInfo: SeparateServerInfo | undefined
    await withCleanup(async () => {
      console.log('[e2e] Start the separate Hub on an assigned port.')
      const hubProc = spawnSeparateHub('127.0.0.1:0', dataDir, output)
      started.push(hubProc)

      // Startup waits run outside a test. Print recent server output on failure because no test attachment exists yet.
      const hub = await waitForHubStart(join(dataDir, 'hub', 'state.json'), hubProc)
        .catch(error => reportStartupFailure(output, 'Hub startup', error))
      hubUrl = hub.hubUrl
      const hubPort = Number(new URL(hubUrl).port)
      console.log(`[e2e] The separate Hub is ready on port ${hubPort}.`)

      // A Hub with no users makes its first registered user an administrator.
      // This first signup does not require the open-signup setting.
      const adminToken = await signUpViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD, TEST_ADMIN_DISPLAY_NAME)

      // Elevate the session before changing hub settings. Signup alone does not grant session elevation.
      await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)

      // Enable signup before creating newuser. A standalone hub defaults to closed signup after the first administrator exists.
      await enableSignupViaAPI(hubUrl, adminToken)

      // A separate process group protects the Worker from signals to the test runner.
      console.log('[e2e] Start the separate Worker.')
      const { proc: workerProc, workerId } = await spawnRegisteredWorker({ hubUrl, adminToken }, {
        name: SEPARATE_WORKER_NAME,
        dataDir: workerDataDir,
        env: agentDefaultsEnv(),
        output,
        detached: true,
        onSpawn: proc => started.push(proc),
      }).catch(error => reportStartupFailure(output, 'worker registration', error))
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

    await attachToastLog(page, testInfo)
    // Attach the recent output of the Hub and the Worker after a failure, as ./fixtures.ts does.
    if (testInfo.status !== testInfo.expectedStatus)
      await attachServerLog(testInfo, separateHubWorker.output.since(serverMark))
  }, { auto: true }],

  agentWorkingDir: [undefined, { option: true }],

  // Confirm the Worker connection before creating the workspace and its initial agent.
  // The separate hub has no per-test reset, so `withTestWorkspace` deletes the workspace after the test.
  workspace: async ({ separateHubWorker, agentWorkingDir }, use) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    await withTestWorkspace(separateHubWorker, 'e2e', async (workspace) => {
      const agentId = await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, workspace.workspaceId, agentWorkingDir)
      await use(agentWorkspaceFixture(workspace, agentId, agentWorkingDir))
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
