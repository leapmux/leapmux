import type { ChildProcess } from 'node:child_process'
import type { ServerOutput } from './helpers/serverOutput'
/* eslint-disable no-console */
import type { WorkspaceFixture } from './helpers/workspace'
import { execFile } from 'node:child_process'
import { rmSync, writeFileSync } from 'node:fs'
import process from 'node:process'
import { promisify } from 'node:util'
import { test as base, expect } from '@playwright/test'
import { isResizeObserverLoopError } from '~/lib/ignorableErrorEvents'
import {
  closeTestChannels,
  elevateSessionViaAPI,
  getUserId,
  getWorkerId,
  loginViaAPI,
  openPinnedModeAgentViaAPI,
  signUpViaAPI,
  TEST_ADMIN_DISPLAY_NAME,
  TEST_ADMIN_PASSWORD,
  TEST_ADMIN_USERNAME,
} from './helpers/api'
import { finishCleanup } from './helpers/cleanup'
import { closeAllUserEventsSubscriptions } from './helpers/crdt'
import { stopProcess } from './helpers/process'
import { spawnTestProcess } from './helpers/processRegistry'
import { createTestDirectory } from './helpers/runDirectory'
import { findFreePort, getGlobalState, hubDataDir, hubSpawnEnv, waitForServer } from './helpers/server'
import { createServerOutput, reportStartupFailure } from './helpers/serverOutput'
import { getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'
import { realAgentEnv } from './realAgentSettings'

export interface ServerInfo {
  hubUrl: string
  adminToken: string
  /**
   * The administrator user ID.
   * Browser storage requires an account ID before addInitScript can set a preference for a page that did not sign in yet.
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
 * Create the first administrator offline before the hub opens its database.
 * leapmux recover bootstrap create-admin refuses the operation if an administrator exists.
 * Dev mode places this database in <dataDir>/hub, the same directory that devModeTokenSource supplies.
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

export const test = base.extend<
  {
    toastRecorder: void
    pageErrorRecorder: void
    emptyWorkspace: WorkspaceFixture
    authenticatedEmptyWorkspace: WorkspaceFixture
    workspace: WorkspaceFixture
    authenticatedWorkspace: WorkspaceFixture
  },
  {
    leapmuxServer: ServerInfo
  }
>({
  // Share one dev instance per Playwright worker. Multiple files can use the same hub.
  // A test must account for state that earlier files left in that hub.
  // eslint-disable-next-line no-empty-pattern
  leapmuxServer: [async ({}, use) => {
    const globalState = getGlobalState()
    const dataDir = createTestDirectory('leapmux-e2e-dev-')
    const port = await findFreePort()
    const hubUrl = `http://localhost:${port}`

    // Create the first administrator before starting the hub.
    // Offline bootstrap refuses an existing administrator. Later online signup cannot claim the reserved admin username.
    await bootstrapFirstAdmin(hubDataDir(dataDir))

    console.log(`[e2e] Starting dev instance on port ${port}...`)

    const proc = spawnTestProcess(globalState.binaryPath, [
      'dev',
      '-listen',
      `:${port}`,
      '-data-dir',
      dataDir,
    ], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: hubSpawnEnv({ ...realAgentEnv(), LEAPMUX_WORKER_NAME: 'Local' }),
    })

    // Drain server output to prevent backpressure. Keep recent output for failure attachments.
    const output = createServerOutput()
    output.capture(proc)

    // Startup runs outside a test, so no test attachment exists yet.
    // Print recent server output on startup failure instead of reporting only a readiness timeout.
    try {
      let adminToken: string
      let adminUserId: string
      let workerId: string
      let newuserToken: string
      try {
        await waitForServer(hubUrl)
        console.log(`[e2e] Dev instance ready on port ${port}`)

        // Log in over HTTP with the administrator that offline bootstrap created. Fixtures use the returned session cookie.
        adminToken = await loginViaAPI(hubUrl, TEST_ADMIN_USERNAME, TEST_ADMIN_PASSWORD)
        // Elevate the shared administrator session once. Every hub-settings write requires elevation.
        // Otherwise, panel tests fail during setup with failed_precondition before they reach the panel.
        // Tests 006, 009, and 143 create separate sessions when they need to check an unelevated session.
        await elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD)
        adminUserId = await getUserId(hubUrl, adminToken)
        workerId = await getWorkerId(hubUrl, adminToken)

        // Create newuser for sharing tests
        newuserToken = await signUpViaAPI(hubUrl, 'newuser', 'password123', 'New User', 'new@test.com')
      }
      catch (err) {
        reportStartupFailure(output, `dev instance on port ${port}`, err)
      }

      await use({ hubUrl, adminToken, adminUserId, workerId, newuserToken, serverProc: proc, dataDir, output })
    }
    finally {
      await finishCleanup([closeAllUserEventsSubscriptions(), closeTestChannels(hubUrl), stopProcess(proc)])
      rmSync(dataDir, { recursive: true, force: true })
    }
  }, { scope: 'worker' }],

  baseURL: async ({ leapmuxServer }, use) => {
    await use(leapmuxServer.hubUrl)
  },

  // Record page errors for every test that inherits this fixture. Do not fail the test here.
  // An uncaught app exception can otherwise appear only as a timeout on an unrelated locator.
  // Inspect page-errors attachments before converting this recorder into a suite-wide assertion.
  // Distinguish browser errors from deliberate test failures and app defects. ignorableErrorEvents holds browser exclusions.
  // Tests that extend @playwright/test directly or use process-control-fixtures do not inherit this recorder.
  // A suite-wide assertion requires those fixtures to participate also.
  pageErrorRecorder: [async ({ page }, use, testInfo) => {
    const pageErrors: string[] = []
    page.on('pageerror', error => pageErrors.push(error.stack ?? error.message))

    await use()

    // The chat virtualizer can exceed the ResizeObserver delivery loop.
    // Use ignorableErrorEvents to classify that browser error. Do not duplicate its regular expression here.
    const real = pageErrors.filter(message => !isResizeObserverLoopError(message))
    if (real.length > 0) {
      await testInfo.attach('page-errors', {
        body: real.join('\n\n'),
        contentType: 'text/plain',
      })
    }
  }, { auto: true }],

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

    // Attach recent server output when a test fails. Otherwise, an out-of-process error can appear only as a locator timeout.
    // Use a file attachment because the list reporter truncates inline attachments to the first line.
    // The file under test-results retains all recent output.
    if (testInfo.status !== testInfo.expectedStatus) {
      const logPath = testInfo.outputPath('server-log.txt')
      writeFileSync(logPath, leapmuxServer.output.since(serverMark))
      await testInfo.attach('server-log', { path: logPath, contentType: 'text/plain' })
    }
  }, { auto: true }],

  emptyWorkspace: async ({ leapmuxServer }, use) => {
    await withTestWorkspace(leapmuxServer, 'e2e', use)
  },

  workspace: async ({ leapmuxServer, emptyWorkspace }, use) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, emptyWorkspace.workspaceId)
    await use(emptyWorkspace)
  },

  authenticatedEmptyWorkspace: async ({ page, emptyWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, emptyWorkspace.workspaceId)
    await use(emptyWorkspace)
  },

  // Authenticated workspace: logs in via token + navigates to workspace
  authenticatedWorkspace: async ({ page, workspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspace.workspaceId)

    await use(workspace)
    // The workspace fixture handles the teardown.
  },
})

export { expect }
