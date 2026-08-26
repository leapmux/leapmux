/* eslint-disable no-console */
import type { ChildProcess } from 'node:child_process'
import type { ServerOutput } from './helpers/serverOutput'
import { execFile, spawn } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { promisify } from 'node:util'
import { test as base, expect } from '@playwright/test'
import {
  cleanupWorkspaceViaAPI,
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  elevateSessionViaAPI,
  getUserId,
  getWorkerId,
  loginViaAPI,
  openAgentViaAPI,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './helpers/api'
import { closeAllUserEventsSubscriptions } from './helpers/crdt'
import { stopProcess } from './helpers/process'
import { findFreePort, getGlobalState, waitForServer } from './helpers/server'
import { createServerOutput, reportStartupFailure } from './helpers/serverOutput'
import { getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'

export interface ServerInfo {
  hubUrl: string
  adminToken: string
  /**
   * The admin's user id. Every browser-storage key is scoped to an account, so
   * a spec that seeds a preference through `addInitScript` -- before the page
   * signs in -- needs it up front.
   */
  adminUserId: string
  workerId: string
  newuserToken: string
  serverProc: ChildProcess
  dataDir: string
  /** Captured stdout+stderr from the dev instance. See {@link createServerOutput}. */
  output: ServerOutput
}

const execFileAsync = promisify(execFile)

/**
 * Create the first administrator OFFLINE, before the hub process starts.
 *
 * The offline admin-token CLI command tests used to lean on is gone;
 * first-admin bootstrap lives in `leapmux recover bootstrap
 * create-admin`, which opens the DB directly and refuses once any admin
 * exists. Dev mode splits the data dir, so the hub's DB is `<dataDir>/hub`
 * — the same directory devModeTokenSource hands to the CLI token helper.
 */
async function bootstrapFirstAdmin(hubDataDir: string): Promise<void> {
  await execFileAsync(getGlobalState().binaryPath, [
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
    hubDataDir,
  ], {
    env: { ...process.env, LEAPMUX_LOG_LEVEL: 'error' },
  })
}

interface WorkspaceFixture {
  workspaceId: string
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
  // Worker-scoped fixture: spawns a fresh dev-mode instance per test file
  // eslint-disable-next-line no-empty-pattern
  leapmuxServer: [async ({}, use) => {
    const globalState = getGlobalState()
    const dataDir = mkdtempSync(join(tmpdir(), 'leapmux-e2e-dev-'))
    const port = await findFreePort()
    const hubUrl = `http://localhost:${port}`

    // The first admin must exist BEFORE the hub opens the DB: the offline
    // bootstrap refuses once any admin exists, and the online /setup path no
    // longer reserves the `admin` username once one does.
    await bootstrapFirstAdmin(join(dataDir, 'hub'))

    console.log(`[e2e] Starting dev instance on port ${port}...`)

    const proc = spawn(globalState.binaryPath, [
      'dev',
      '-listen',
      `:${port}`,
      '-data-dir',
      dataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet', LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low', LEAPMUX_CODEX_DEFAULT_MODEL: 'gpt-5.4-mini', LEAPMUX_COPILOT_DEFAULT_MODEL: 'gpt-5.4-mini', LEAPMUX_WORKER_NAME: 'Local' },
    })

    // Consume server output (also prevents backpressure), keeping a tail for
    // failing tests to attach.
    const output = createServerOutput()
    output.capture(proc)

    // Everything up to `use` runs OUTSIDE any test, so a failure here has no
    // test to attach the tail to -- it is printed instead. Otherwise a dev
    // instance that died on startup reports as a bare "Timed out waiting".
    let adminToken: string
    let adminUserId: string
    let workerId: string
    let newuserToken: string
    try {
      await waitForServer(hubUrl)
      console.log(`[e2e] Dev instance ready on port ${port}`)

      // The admin was bootstrapped offline above; log in over HTTP for the
      // session cookie the rest of the fixtures auth with.
      adminToken = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
      // ELEVATED, once, exactly as an operator's browser session is.
      //
      // Every hub-settings write demands an elevated session, so a fixture
      // cookie without one turns each `UpdateSetting` helper into a
      // failed_precondition in setup -- and the specs that walk the admin
      // panels would measure the gate instead of the panel.
      //
      // The specs that need an UN-elevated session already mint their own
      // (006-passkey, 009-elevation, 143-cli-elevation all say so where they
      // do it), for the same reason this is safe: a shared session that is
      // reliably elevated is one less thing for them to depend on.
      await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)
      adminUserId = await getUserId(hubUrl, adminToken)
      workerId = await getWorkerId(hubUrl, adminToken)

      // Create newuser for sharing tests
      newuserToken = await signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com')
    }
    catch (err) {
      await stopProcess(proc)
      reportStartupFailure(output, `dev instance on port ${port}`, err)
    }

    await use({ hubUrl, adminToken, adminUserId, workerId, newuserToken, serverProc: proc, dataDir, output })

    // Close any UserEvents subscriptions this worker opened. Per-worker
    // cleanup matters because the singleton cache is keyed by
    // (hubUrl, cookie) and the next worker spawns a NEW dev
    // instance on a different port; leaving WS sockets dangling would
    // leak file descriptors across the test session.
    await closeAllUserEventsSubscriptions()

    // Teardown: stop the process, clean up the data dir.
    await stopProcess(proc)
    rmSync(dataDir, { recursive: true, force: true })
    console.log(`[e2e] Dev instance on port ${port} stopped`)
  }, { scope: 'worker' }],

  // Override page to set baseURL dynamically from the dev instance
  page: async ({ leapmuxServer, browser }, use) => {
    const context = await browser.newContext({
      baseURL: leapmuxServer.hubUrl,
    })
    const page = await context.newPage()
    await use(page)
    await context.close()
  },

  // Toast recorder: auto-use so it runs for every test
  toastRecorder: [async ({ page, leapmuxServer }, use, testInfo) => {
    await installToastRecorder(page)
    const serverMark = leapmuxServer.output.mark()
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

    // A failing test gets the dev instance's recent output. The hub and worker
    // run out of process, so their errors are otherwise invisible and a
    // worker-side failure surfaces only as a timeout on an unrelated locator.
    //
    // Attached as a FILE, not a body: the list reporter truncates an inline
    // attachment to its first line, which is the startup banner and nothing
    // else. A path lands the whole tail under test-results/ where it can
    // actually be read.
    if (testInfo.status !== testInfo.expectedStatus) {
      const logPath = testInfo.outputPath('server-log.txt')
      writeFileSync(logPath, leapmuxServer.output.since(serverMark))
      await testInfo.attach('server-log', { path: logPath, contentType: 'text/plain' })
    }
  }, { auto: true }],

  // Workspace fixture: creates workspace via API + opens initial agent, provides ID and URL
  workspace: async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      `e2e-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
    )
    // Open an initial agent — workspace creation on the hub no longer
    // auto-creates one (that was the old worker behavior).
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    await use({ workspaceId })

    // Teardown (best effort): stop the workspace's agents on the worker, THEN
    // soft-delete on the hub -- the same two-step cascade the browser app runs.
    // The cleanup must precede the delete because it names the workspace's tabs
    // to the worker, and the hub can only list them while the workspace still
    // exists. Without the cleanup step, the worker keeps
    // each test's Claude CLI subprocess alive and they accumulate across the suite,
    // starving resources and flaking later settings-menu interactions.
    try {
      await cleanupWorkspaceViaAPI(hubUrl, adminToken, workerId, workspaceId)
    }
    catch {
      // Best effort
    }
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
    await openWorkspace(page, workspace.workspaceId)

    await use(workspace)
    // Teardown handled by workspace fixture
  },
})

export { expect }
