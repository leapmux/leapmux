/* eslint-disable no-console */
import type { ChildProcess } from 'node:child_process'
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { test as base, expect } from '@playwright/test'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  enableSignupViaAPI,
  findFreePort,
  getAdminOrgId,
  getGlobalState,
  getRecordedToasts,
  getWorkerId,
  installToastRecorder,
  loginViaAPI,
  loginViaToken,
  signUpViaAPI,
  waitForServer,
  waitForWorkspaceReady,
} from './helpers'

export interface ServerInfo {
  hubUrl: string
  adminToken: string
  adminOrgId: string
  workerId: string
  newuserToken: string
  serverProc: ChildProcess
  dataDir: string
}

interface WorkspaceFixture {
  workspaceId: string
  workspaceUrl: string
}

export const test = base.extend<
  {
    toastRecorder: void
    workspace: WorkspaceFixture
    authenticatedWorkspace: WorkspaceFixture
  },
  {
    leapmuxServer: ServerInfo
  }
>({
  // Worker-scoped fixture: spawns a fresh standalone instance per test file
  // eslint-disable-next-line no-empty-pattern
  leapmuxServer: [async ({}, use) => {
    const globalState = getGlobalState()
    const dataDir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-standalone-'))
    const port = await findFreePort()
    const hubUrl = `http://localhost:${port}`

    console.log(`[e2e] Starting standalone instance on port ${port}...`)

    const proc = spawn(globalState.binaryPath, [
      '-addr',
      `:${port}`,
      '-data-dir',
      dataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, LEAPMUX_DEFAULT_MODEL: 'haiku' },
    })

    // Drain stdout/stderr
    proc.stdout?.resume()
    proc.stderr?.resume()

    await waitForServer(hubUrl)
    console.log(`[e2e] Standalone instance ready on port ${port}`)

    // Login as admin (bootstrap creates admin/admin)
    const adminToken = await loginViaAPI(hubUrl, 'admin', 'admin')
    const adminOrgId = await getAdminOrgId(hubUrl, adminToken)
    const workerId = await getWorkerId(hubUrl, adminToken)

    // Enable signup so signup tests work
    await enableSignupViaAPI(hubUrl, adminToken)

    // Create newuser for sharing tests
    const newuserToken = await signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com')

    await use({ hubUrl, adminToken, adminOrgId, workerId, newuserToken, serverProc: proc, dataDir })

    // Teardown: kill process, clean up data dir
    proc.kill('SIGTERM')
    await new Promise(r => setTimeout(r, 1000))
    try {
      proc.kill('SIGKILL')
    }
    catch { /* already dead */ }
    rmSync(dataDir, { recursive: true, force: true })
    console.log(`[e2e] Standalone instance on port ${port} stopped`)
  }, { scope: 'worker' }],

  // Override page to set baseURL dynamically from the standalone instance
  page: async ({ leapmuxServer, browser }, use) => {
    const context = await browser.newContext({
      baseURL: leapmuxServer.hubUrl,
    })
    const page = await context.newPage()
    await use(page)
    await context.close()
  },

  // Toast recorder: auto-use so it runs for every test
  toastRecorder: [async ({ page }, use, testInfo) => {
    await installToastRecorder(page)
    await use()

    // After test: collect toasts and attach to test report
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

  // Workspace fixture: creates workspace via API, provides ID and URL
  workspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      workerId,
      `e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
      adminOrgId,
    )
    const workspaceUrl = `/o/admin/workspace/${workspaceId}`

    await use({ workspaceId, workspaceUrl })

    // Teardown: delete workspace via API (best effort)
    try {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId)
    }
    catch {
      // Best effort
    }
  },

  // Authenticated workspace: logs in via token + navigates to workspace
  authenticatedWorkspace: async ({ page, workspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    await use(workspace)
    // Teardown handled by workspace fixture
  },
})

export { expect }
