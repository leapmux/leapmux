import type { BrowserContext, Page, ViewportSize } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { WorkspaceFixture } from './helpers/workspace'
import { writeFileSync } from 'node:fs'
import { basename } from 'node:path'
import { test as base, expect } from '@playwright/test'
import { isResizeObserverLoopError } from '~/lib/ignorableErrorEvents'
import {
  closeTestChannels,
  deleteAllWorkspacesViaAPI,
  openPinnedModeAgentViaAPI,
  resetAllUserSettingsViaAPI,
} from './helpers/api'
import { finishCleanup } from './helpers/cleanup'
import { closeAllUserEventsSubscriptions } from './helpers/crdt'
import { readMockModelDiagnostics } from './helpers/mockModelScenario'
import { runModelScriptFixture } from './helpers/modelScriptFixture'
import { getGlobalState } from './helpers/server'
import { markSuiteServerLog, readSuiteServerLog } from './helpers/suiteServerLog'
import { clearRecordedToasts, getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

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
  dataDir: string
  serverLogPath: string
  mockModelUrl: string
  piAgentDir: string
  agentEnv: Record<string, string>
}

interface SharedPageState {
  context: BrowserContext
  page: Page
}

interface ActivePageState {
  context: BrowserContext
  page: Page
}

const DEFAULT_VIEWPORT = { width: 1280, height: 720 } as const
const DEFAULT_PERMISSIONS = ['clipboard-read', 'clipboard-write']
const PAGE_EVENTS_WITH_TEST_LISTENERS = ['console', 'dialog', 'pageerror', 'request', 'response', 'websocket'] as const
const ISOLATED_CONTEXT_SPECS = new Set([
  '005-first-admin-setup.spec.ts',
  '006-passkey.spec.ts',
  '007-account-recovery.spec.ts',
  '027-tunnel-ui.spec.ts',
  '080-turn-end-sound-preferences.spec.ts',
  '106-codex-turn-end-sound.spec.ts',
  '161-watch-stream-continuity.spec.ts',
])

/**
 * The media emulation a test starts from, with EVERY key stated.
 *
 * `emulateMedia` overrides only the keys it is given and leaves the rest as
 * they are, so on a shared tab one spec's emulation outlives it. The reset used
 * to pass `colorScheme` alone, and `074`, `186` and `196` each emulate
 * `reducedMotion: 'reduce'` -- so every spec after one of those ran with motion
 * reduced, which suppresses the transitions some of them assert on. `196`'s
 * `forcedColors: 'active'` leaked the same way. The failure names the feature
 * under test, never the spec that left the override behind.
 *
 * Each value is EXPLICIT rather than `null`. A `null` restores the machine's own
 * setting, which would make the suite fail on a developer box that has Reduce
 * Motion or Increase Contrast turned on in the OS.
 *
 * `Required<>` is what keeps this complete: a Playwright release that adds a
 * sixth media feature fails the type check here until this function states it,
 * so no key can be forgotten back into the leak above.
 */
function mediaEmulationReset(
  colorScheme: 'dark' | 'light' | 'no-preference' | null,
): Required<NonNullable<Parameters<Page['emulateMedia']>[0]>> {
  return {
    colorScheme: colorScheme ?? 'light',
    contrast: 'no-preference',
    forcedColors: 'none',
    media: 'screen',
    reducedMotion: 'no-preference',
  }
}

async function newSharedPage(context: BrowserContext): Promise<Page> {
  const page = await context.newPage()
  await installToastRecorder(page)
  return page
}

async function resetSharedPage(
  state: SharedPageState,
  options: {
    hubUrl: string
    viewport: ViewportSize | null
    colorScheme: 'dark' | 'light' | 'no-preference' | null
    deviceScaleFactor: number
    hasTouch: boolean
    isMobile: boolean
  },
): Promise<Page> {
  if (state.page.isClosed())
    state.page = await newSharedPage(state.context)
  const page = state.page
  for (const other of state.context.pages()) {
    if (other !== page)
      await other.close()
  }
  for (const event of PAGE_EVENTS_WITH_TEST_LISTENERS)
    await page.removeAllListeners(event, { behavior: 'wait' })
  await Promise.all([
    page.unrouteAll({ behavior: 'wait' }),
    state.context.unrouteAll({ behavior: 'wait' }),
  ])
  await page.goto('about:blank')
  await state.context.setOffline(false)
  await state.context.setExtraHTTPHeaders({})
  await state.context.setGeolocation(null)
  await state.context.clearCookies()
  await state.context.clearPermissions()
  await state.context.grantPermissions(DEFAULT_PERMISSIONS)

  const origin = new URL(options.hubUrl).origin
  const viewport = options.viewport ?? DEFAULT_VIEWPORT
  await page.setViewportSize(viewport)
  await page.emulateMedia(mediaEmulationReset(options.colorScheme))
  const cdp = await state.context.newCDPSession(page)
  try {
    await cdp.send('Storage.clearDataForOrigin', { origin, storageTypes: 'all' })
    // DOMStorage needs an active frame for its security origin. A static asset
    // establishes that frame without starting the app or opening IndexedDB.
    await page.goto(`${origin}/favicon.ico`)
    for (const isLocalStorage of [true, false])
      await cdp.send('DOMStorage.clear', { storageId: { securityOrigin: origin, isLocalStorage } })
    await page.goto('about:blank')
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width: viewport.width,
      height: viewport.height,
      deviceScaleFactor: options.deviceScaleFactor,
      mobile: options.isMobile,
      screenWidth: viewport.width,
      screenHeight: viewport.height,
    })
    await cdp.send('Emulation.setTouchEmulationEnabled', {
      enabled: options.hasTouch,
      ...(options.hasTouch ? { maxTouchPoints: 1 } : {}),
    })
  }
  finally {
    await cdp.detach()
  }
  await clearRecordedToasts(page)
  return page
}

export const test = base.extend<
  {
    activePageState: ActivePageState
    hubStateReset: void
    modelScript: ModelScript
    toastRecorder: void
    pageErrorRecorder: void
    emptyWorkspace: WorkspaceFixture
    authenticatedEmptyWorkspace: WorkspaceFixture
    workspace: WorkspaceFixture
    authenticatedWorkspace: WorkspaceFixture
  },
  {
    sharedPageState: SharedPageState
    leapmuxServer: ServerInfo
  }
>({
  // Global setup starts one dev instance for the complete run.
  // eslint-disable-next-line no-empty-pattern
  leapmuxServer: [async ({}, use) => {
    const globalState = getGlobalState()
    try {
      await use(globalState)
    }
    finally {
      await finishCleanup([closeAllUserEventsSubscriptions(), closeTestChannels(globalState.hubUrl)])
    }
  }, { scope: 'worker' }],

  sharedPageState: [async ({ browser, leapmuxServer }, use) => {
    const context = await browser.newContext({
      baseURL: leapmuxServer.hubUrl,
      viewport: DEFAULT_VIEWPORT,
      screen: DEFAULT_VIEWPORT,
      deviceScaleFactor: 1,
      // A DESKTOP context, and `hasTouch` is what makes it one. With touch on,
      // the app's coarse-pointer affordances apply to every test that shares
      // this context: a hover-revealed kebab, the tab overflow menu and the
      // selection popover all behave as they do on a phone, and a desktop test
      // that expects them fails for a reason nothing on screen explains. A test
      // that wants touch takes its own context instead — see `activePageState`.
      hasTouch: false,
      isMobile: false,
      permissions: DEFAULT_PERMISSIONS,
    })
    const state = { context, page: await newSharedPage(context) }
    await use(state)
    await context.close()
  }, { scope: 'worker' }],

  activePageState: async ({ browser, colorScheme, deviceScaleFactor, hasTouch, isMobile, leapmuxServer, sharedPageState, viewport }, use, testInfo) => {
    const metrics = viewport ?? DEFAULT_VIEWPORT
    // `hasTouch` and the device metrics belong to a browser CONTEXT, not to a
    // page, so a test that changes one cannot share the desktop context.
    const isolated = isMobile || hasTouch || (deviceScaleFactor ?? 1) !== 1
      || ISOLATED_CONTEXT_SPECS.has(basename(testInfo.file))
    if (isolated) {
      const context = await browser.newContext({
        baseURL: leapmuxServer.hubUrl,
        viewport: metrics,
        screen: metrics,
        deviceScaleFactor: deviceScaleFactor ?? 1,
        hasTouch,
        isMobile,
        colorScheme: colorScheme ?? 'light',
        permissions: DEFAULT_PERMISSIONS,
      })
      try {
        await use({ context, page: await newSharedPage(context) })
      }
      finally {
        await context.close()
      }
      return
    }

    const page = await resetSharedPage(sharedPageState, {
      hubUrl: leapmuxServer.hubUrl,
      viewport,
      colorScheme,
      deviceScaleFactor: deviceScaleFactor ?? 1,
      hasTouch,
      isMobile,
    })
    await use({ context: sharedPageState.context, page })
  },

  context: async ({ activePageState }, use) => {
    await use(activePageState.context)
  },

  page: async ({ activePageState }, use) => {
    await use(activePageState.page)
  },

  baseURL: async ({ leapmuxServer }, use) => {
    await use(leapmuxServer.hubUrl)
  },

  // Return the shared account to a known state before this test builds its own.
  //
  // One LeapMux process and ONE account serve the whole run, so both halves of
  // that state outlive the test that wrote them.
  //
  // A workspace outlives its test: a test that deletes its own can still be
  // preceded by one that failed before its cleanup ran, or by one that made a
  // second workspace it did not track. The sidebar then draws the extra rows,
  // and an assertion that counts them fails in the suite while it passes alone.
  //
  // An account SETTING outlives its test the same way, and reads worse -- there
  // is no extra row to see. `196-pill-group` asserts that the terminal theme
  // mode control is disabled, which the product does while the terminal theme
  // follows the app; an earlier test that gave the terminal its own palette left
  // that control enabled, and the failure named a disabled attribute rather than
  // the setting behind it.
  //
  // Both are made impossible here rather than chased at each source.
  //
  // This runs BEFORE the test body and before every workspace fixture, which
  // depends on it. It never removes a workspace the current test owns.
  hubStateReset: [async ({ leapmuxServer }, use) => {
    await deleteAllWorkspacesViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    await resetAllUserSettingsViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    await use()
  }, { auto: true }],

  // One model script for each test that declares it.
  //
  // The mock endpoint answers a marked prompt from this script alone, so a turn
  // this test did not queue fails THIS test. A test that sends a prompt without
  // `modelScript.prompt` reaches the ambient scenario instead, which answers a
  // provider's own title turn and refuses everything else.
  // eslint-disable-next-line no-empty-pattern
  modelScript: async ({}, use, testInfo) => runModelScriptFixture(use, testInfo),

  // Record page errors for every test that inherits this fixture. Do not fail the test here.
  // An uncaught app exception can otherwise appear only as a timeout on an unrelated locator.
  // Inspect page-errors attachments before converting this recorder into a suite-wide assertion.
  // Distinguish browser errors from deliberate test failures and app defects. ignorableErrorEvents holds browser exclusions.
  // Tests that extend @playwright/test directly or use process-control-fixtures do not inherit this recorder.
  // A suite-wide assertion requires those fixtures to participate also.
  pageErrorRecorder: [async ({ page }, use, testInfo) => {
    const pageErrors: string[] = []
    const recordPageError = (error: Error) => pageErrors.push(error.stack ?? error.message)
    page.on('pageerror', recordPageError)

    try {
      await use()
    }
    finally {
      page.off('pageerror', recordPageError)
    }

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
    const serverMark = markSuiteServerLog(leapmuxServer.serverLogPath)
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
      writeFileSync(logPath, readSuiteServerLog(leapmuxServer.serverLogPath, serverMark))
      await testInfo.attach('server-log', { path: logPath, contentType: 'text/plain' })

      // The mock endpoint's own view: the agent traffic it saw, the model
      // requests that reached no scenario, and the ambient scenario's record of
      // a turn no test scripted. A provider failure appears here as a 409 with
      // the body that arrived, rather than as a locator timeout.
      const modelLogPath = testInfo.outputPath('model-server-log.json')
      writeFileSync(modelLogPath, JSON.stringify(await readMockModelDiagnostics(leapmuxServer.mockModelUrl), null, 2))
      await testInfo.attach('model-server-log', { path: modelLogPath, contentType: 'application/json' })
    }
  }, { auto: true }],

  emptyWorkspace: async ({ hubStateReset, leapmuxServer }, use) => {
    // The dependency is what ORDERS the reset before this fixture creates a
    // workspace. An automatic fixture alone does not state that order.
    void hubStateReset
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
