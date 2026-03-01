/* eslint-disable no-console */
import type { Buffer } from 'node:buffer'
import type { ChildProcess } from 'node:child_process'
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { test as base, expect } from '@playwright/test'
import {
  approveRegistrationViaAPI,
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  enableSignupViaAPI,
  getAdminOrgId,
  loginViaAPI,
  signUpViaAPI,
} from './helpers/api'
import { findFreePort, getGlobalState, waitForServer } from './helpers/server'
import { getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

export interface SeparateServerInfo {
  hubUrl: string
  adminToken: string
  adminOrgId: string
  workerId: string
  newuserToken: string
  hubProc: ChildProcess
  workerProc: ChildProcess
  hubPid: number
  workerPid: number
  dataDir: string
  binaryPath: string
  hubPort: number
}

interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
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
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${adminToken}`,
        },
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
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${serverInfo.adminToken}`,
      },
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
  })

  // Wait for the worker to connect to the hub
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('Worker restart timed out')), 30_000)
    const onData = (chunk: Buffer) => {
      const text = chunk.toString()
      if (text.includes('connected to hub')) {
        clearTimeout(timeout)
        workerProc.stderr?.off('data', onData)
        workerProc.stdout?.off('data', onData)
        workerProc.stderr?.resume()
        workerProc.stdout?.resume()
        resolve()
      }
    }
    workerProc.stderr?.on('data', onData)
    workerProc.stdout?.on('data', onData)
  })

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
    '-addr',
    `:${serverInfo.hubPort}`,
    '-data-dir',
    hubDataDir,
  ], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, LEAPMUX_DEFAULT_MODEL: 'sonnet', LEAPMUX_DEFAULT_EFFORT: 'low' },
  })

  hubProc.stdout?.resume()
  hubProc.stderr?.resume()

  // Wait for the hub to become ready
  await waitForServer(serverInfo.hubUrl)

  // Verify the hub is fully operational by testing login
  for (let i = 0; i < 10; i++) {
    try {
      await loginViaAPI(serverInfo.hubUrl, 'admin', 'admin')
      break
    }
    catch {
      if (i === 9)
        throw new Error('Hub restart: login health check failed')
      await new Promise(r => setTimeout(r, 500))
    }
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

    // Start hub
    const hubProc = spawn(globalState.binaryPath, [
      'hub',
      '-addr',
      `:${hubPort}`,
      '-data-dir',
      hubDataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, LEAPMUX_DEFAULT_MODEL: 'sonnet', LEAPMUX_DEFAULT_EFFORT: 'low' },
    })
    hubProc.stdout?.resume()
    hubProc.stderr?.resume()

    await waitForServer(hubUrl)
    console.log(`[e2e] Hub ready on port ${hubPort}`)

    // Login as admin and setup
    const adminToken = await loginViaAPI(hubUrl, 'admin', 'admin')
    const adminOrgId = await getAdminOrgId(hubUrl, adminToken)

    // Enable signup
    await enableSignupViaAPI(hubUrl, adminToken)

    // Start worker
    console.log('[e2e] Starting separate worker...')
    const workerProc = spawn(globalState.binaryPath, [
      'worker',
      '-hub',
      hubUrl,
      '-data-dir',
      workerDataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
    })

    // Wait for registration token
    const registrationToken = await new Promise<string>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('Timed out waiting for worker registration token')), 30_000)
      const onData = (chunk: Buffer) => {
        const text = chunk.toString()
        const jsonMatch = text.match(/"token"\s*:\s*"([^"]+)"/)
        const urlMatch = text.match(/\/register\/(\S+)/)
        const token = jsonMatch?.[1] ?? urlMatch?.[1]
        if (token) {
          clearTimeout(timeout)
          workerProc.stderr?.off('data', onData)
          workerProc.stdout?.off('data', onData)
          workerProc.stderr?.resume()
          workerProc.stdout?.resume()
          resolve(token)
        }
      }
      workerProc.stderr?.on('data', onData)
      workerProc.stdout?.on('data', onData)
    })

    // Approve worker
    const workerId = await approveRegistrationViaAPI(
      hubUrl,
      adminToken,
      registrationToken,
      'test-worker',
      adminOrgId,
    )
    console.log(`[e2e] Worker approved: ${workerId}`)

    // Wait for worker to come online
    const start = Date.now()
    while (Date.now() - start < 30_000) {
      const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${adminToken}`,
        },
        body: JSON.stringify({}),
      })
      if (res.ok) {
        const data = await res.json() as { workers: Array<{ id: string, online: boolean }> }
        if (data.workers.some(w => w.id === workerId && w.online))
          break
      }
      await new Promise(r => setTimeout(r, 500))
    }
    console.log('[e2e] Worker connected')

    // Create newuser
    const newuserToken = await signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com')

    // Save state path for backward compatibility with loginViaUI which reads token from localStorage
    const stateForFile = {
      tmpDir: dataDir,
      hubPid: hubProc.pid,
      workerPid: workerProc.pid,
      hubUrl,
      adminToken,
      adminOrgId,
      workerId,
      newuserToken,
    }
    writeFileSync(join(dataDir, 'state.json'), JSON.stringify(stateForFile, null, 2))

    const serverInfo: SeparateServerInfo = {
      hubUrl,
      adminToken,
      adminOrgId,
      workerId,
      newuserToken,
      hubProc,
      workerProc,
      hubPid: hubProc.pid!,
      workerPid: workerProc.pid!,
      dataDir,
      binaryPath: globalState.binaryPath,
      hubPort,
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
  toastRecorder: [async ({ page }, use, testInfo) => {
    await installToastRecorder(page)
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
  }, { auto: true }],

  // Workspace fixture — ensure worker is online before creating workspace
  workspace: async ({ separateHubWorker }, use) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      workerId,
      `e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
      adminOrgId,
    )
    const workspaceUrl = `/o/admin/workspace/${workspaceId}`
    await use({ workspaceId, workspaceUrl })
    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch { /* best effort */ }
  },

  // Authenticated workspace
  authenticatedWorkspace: async ({ page, workspace, separateHubWorker }, use) => {
    await loginViaToken(page, separateHubWorker.adminToken)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)
    await use(workspace)
  },
})

export { expect }
