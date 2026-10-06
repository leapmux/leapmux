import type { BrowserContext, Page, ViewportSize } from '@playwright/test'
import type { ModelScriptFixtures } from './helpers/modelScriptFixture'
import type { SuiteServerState } from './helpers/suiteServer'
import type { AgentWorkspaceFixture, WorkspaceFixture } from './helpers/workspace'
import { writeFileSync } from 'node:fs'
import { basename, relative } from 'node:path'
import { test as base } from '@playwright/test'
import { isResizeObserverLoopError } from '~/lib/ignorableErrorEvents'
import {
  closeTestChannels,
  deleteAllWorkspacesViaAPI,
  elevateSessionViaAPI,
  openPinnedModeAgentViaAPI,
  resetAllUserSettingsViaAPI,
  resetHubSettingsViaAPI,
  TEST_ADMIN_PASSWORD,
} from './helpers/api'
import { finishCleanup } from './helpers/cleanup'
import { closeAllUserEventsSubscriptions } from './helpers/crdt'
import { readMockModelDiagnostics } from './helpers/mockModelScenario'
import { modelScriptFixtures } from './helpers/modelScriptFixture'
import { getGlobalState } from './helpers/server'
import { attachServerLog } from './helpers/serverOutput'
import { markSuiteServerLog, readSuiteServerLog } from './helpers/suiteServerLog'
import { attachToastLog, clearRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { agentWorkspaceFixture, withTestWorkspace } from './helpers/workspace'

/** The shared hub of the run, as global setup started it. */
export type ServerInfo = SuiteServerState

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
  'claude-code/background-tasks-sidebar.spec.ts',
  'turn-end-sound.spec.ts',
  '005-first-admin-setup.spec.ts',
  '006-passkey.spec.ts',
  '007-account-recovery.spec.ts',
  '027-tunnel-ui.spec.ts',
  '080-turn-end-sound-preferences.spec.ts',
  '161-watch-stream-continuity.spec.ts',
  // These install the Playwright clock, which no API removes from its context again.
  '047-chat-scroll-rail.spec.ts',
  '047b-chat-scroll-rail-autohide.spec.ts',
])

/**
 * Reset every media-emulation key before the next test uses the shared page.
 *
 * Playwright retains each key that emulateMedia omits. A previous reduced-motion
 * setting can suppress transitions. A previous forced-colors setting can change
 * the next test's styles. Both failures can point at an unrelated feature.
 *
 * Use explicit values. Null restores the machine's preference, which can enable
 * Reduce Motion or Increase Contrast and change the test result.
 *
 * Required makes each media key mandatory. When Playwright adds a key, the type
 * check requires this function to supply its reset value.
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
  ModelScriptFixtures & {
    activePageState: ActivePageState
    hubStateReset: void
    toastRecorder: void
    pageErrorRecorder: void
    emptyWorkspace: WorkspaceFixture
    authenticatedEmptyWorkspace: WorkspaceFixture
    /**
     * The working directory of the agent that the `workspace` fixture opens. Undefined gives the Worker's default
     * directory. A spec states it once: `test.use({ agentWorkingDir: frontendRoot })`, or a fixture function that
     * makes a directory for each test and removes it after the test.
     */
    agentWorkingDir: string | undefined
    workspace: AgentWorkspaceFixture
    authenticatedWorkspace: AgentWorkspaceFixture
  },
  {
    sharedPageState: SharedPageState
    leapmuxServer: ServerInfo
  }
>({
  ...modelScriptFixtures,

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
      // Touch changes hover controls and selection behavior for every shared test.
      // Tests that require touch use a separate context through activePageState.
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
    // Touch and device metrics belong to the browser context.
    // A test that changes either requires a separate context.
    const isolated = isMobile || hasTouch || (deviceScaleFactor ?? 1) !== 1
      || ISOLATED_CONTEXT_SPECS.has(basename(testInfo.file))
      || ISOLATED_CONTEXT_SPECS.has(relative(import.meta.dirname, testInfo.file).replaceAll('\\', '/'))
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

  // Reset the shared account and the shared hub before each test creates its workspace.
  // A failed test can leave extra workspaces, account preferences, or hub settings behind.
  // Extra workspaces change sidebar counts. Preferences can change controls
  // without a visible cause. For example, a custom terminal palette enables its
  // theme-mode control and can break a later disabled-control assertion.
  // A hub setting reaches every later test. For example, a broken SMTP setting makes a later signup fail closed.
  // A test can also drop the elevation of the shared session, as 083 does, and every hub-settings write through that
  // session then fails. So the reset elevates the session again, also when no setting needs a reset.
  // Each workspace fixture depends on this reset, so the reset cannot remove
  // a workspace that the current test creates.
  // The four steps are independent, so they run together, and the fixture reports every failure in one error.
  hubStateReset: [async ({ leapmuxServer }, use) => {
    const { hubUrl, adminToken, baselineHubSettings } = leapmuxServer
    await finishCleanup([
      elevateSessionViaAPI(hubUrl, adminToken, TEST_ADMIN_PASSWORD),
      deleteAllWorkspacesViaAPI(hubUrl, adminToken),
      resetAllUserSettingsViaAPI(hubUrl, adminToken),
      resetHubSettingsViaAPI(hubUrl, adminToken, { baseline: baselineHubSettings, password: TEST_ADMIN_PASSWORD }),
    ])
    await use()
  }, { auto: true }],

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

  // Record toasts for every test that uses this fixture.
  toastRecorder: [async ({ page, leapmuxServer }, use, testInfo) => {
    const serverMark = markSuiteServerLog(leapmuxServer.serverLogPath)
    await use()

    await attachToastLog(page, testInfo)
    if (testInfo.status !== testInfo.expectedStatus) {
      // An out-of-process error can otherwise appear only as a locator timeout.
      await attachServerLog(testInfo, readSuiteServerLog(leapmuxServer.serverLogPath, serverMark))

      // Attach actual model traffic and requests that reached no scenario.
      // A native provider failure includes its HTTP status and request body.
      const modelLogPath = testInfo.outputPath('model-server-log.json')
      writeFileSync(modelLogPath, JSON.stringify(await readMockModelDiagnostics(leapmuxServer.mockModelUrl), null, 2))
      await testInfo.attach('model-server-log', { path: modelLogPath, contentType: 'application/json' })
    }
  }, { auto: true }],

  emptyWorkspace: async ({ hubStateReset, leapmuxServer }, use) => {
    // The explicit dependency makes the reset finish before this workspace starts.
    void hubStateReset
    await withTestWorkspace(leapmuxServer, 'e2e', use)
  },

  agentWorkingDir: [undefined, { option: true }],

  workspace: async ({ leapmuxServer, emptyWorkspace, agentWorkingDir }, use) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const agentId = await openPinnedModeAgentViaAPI(hubUrl, adminToken, workerId, emptyWorkspace.workspaceId, agentWorkingDir)
    await use(agentWorkspaceFixture(emptyWorkspace, agentId, agentWorkingDir))
  },

  authenticatedEmptyWorkspace: async ({ page, emptyWorkspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, emptyWorkspace.workspaceId)
    await use(emptyWorkspace)
  },

  // Sign in with the token and open the workspace.
  authenticatedWorkspace: async ({ page, workspace, leapmuxServer }, use) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, workspace.workspaceId)

    await use(workspace)
    // The workspace fixture handles the teardown.
  },
})
