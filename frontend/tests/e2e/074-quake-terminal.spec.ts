import type { Locator, Page } from '@playwright/test'
import type { ServerInfo } from './fixtures'
import type { WorkspaceFixture } from './helpers/workspace'
import { test as base, expect } from './fixtures'
import { listWorkspacesViaAPI, openAgentViaAPI } from './helpers/api'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { createTestDirectory } from './helpers/runDirectory'
import { getTerminalText, waitForTerminalReady } from './helpers/terminal'
import { loginViaToken, openTerminalViaUI, openWorkspace, setInitialBrowserPref, waitForActiveTabContext } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

declare global {
  interface Window {
    __quakeStartupOverlayAlpha?: number | null
    __getTerminalTextById?: (terminalId: string) => string
  }
}

interface QuakeServer extends ServerInfo, WorkspaceFixture {}

const test = base.extend<{ quakeServer: QuakeServer }>({
  quakeServer: async ({ leapmuxServer }, use, testInfo) => {
    await withTestWorkspace(leapmuxServer, `Quake-${testInfo.title}`, workspace => use({ ...leapmuxServer, ...workspace }))
  },
})

/**
 * The quake terminal supplies one shell for a working directory on one Worker.
 * Its panel slides over the centre area.
 *
 * After creation, the panel stays mounted and uses a transform to slide.
 * Playwright considers the open and closed panel visible.
 * Use `toBeInViewport` to check its position, as the mobile drawer tests do.
 */
const PANEL = '[data-testid="quake-panel"]'

const MOD = process.platform === 'darwin' ? 'Meta' : 'Control'
// The quake shortcut uses Control on every platform.
// macOS reserves Command+` to switch between application windows.
// Playwright presses the physical Backquote key.
// PHYSICAL_KEY_ALIASES expands `grave` to that key in `~/lib/shortcuts/keybindings`.

async function toggleQuake(page: Page) {
  // The shortcut uses the active tab's working directory.
  // A projected tab lacks that directory until the Worker returns its agent or terminal metadata.
  // `workingDir` comes from tab metadata. The shared tab record does not carry it.
  // Without that metadata, `quakeKeyForTab` returns undefined and the shortcut handler takes no action.
  // The handler retries nothing and shows no message, so the panel does not appear.
  // Wait for the metadata before pressing the shortcut.
  await waitForActiveTabContext(page)
  await page.keyboard.press('Control+Backquote')
}

const panel = (page: Page) => page.locator(PANEL)

/**
 * Resolve an element's CSS background to four 8-bit channels:
 * red, green, blue, and alpha.
 */
async function backgroundPixel(locator: Locator): Promise<number[]> {
  return locator.evaluate((el) => {
    const canvas = document.createElement('canvas')
    canvas.width = 1
    canvas.height = 1
    const context = canvas.getContext('2d')
    if (!context)
      throw new Error('the browser did not supply a 2D canvas context')
    context.fillStyle = getComputedStyle(el).backgroundColor
    context.fillRect(0, 0, 1, 1)
    return [...context.getImageData(0, 0, 1, 1).data]
  })
}

/**
 * Create a directory for this test only.
 * A quake terminal belongs to one Worker and directory.
 * The Worker fixture serves multiple tests.
 * Private directories keep file contents separate between tests.
 * Multiple directories in one test must receive separate quake terminals.
 */
function freshDir() {
  return createTestDirectory('leapmux-quake-')
}

/** Open an agent tab and select it as the initial state for each case. */
async function openAgentTab(page: Page, server: QuakeServer) {
  const workingDir = freshDir()
  const { workspaceId } = server
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, workingDir)
  await loginViaToken(page, server.adminToken)
  await openWorkspace(page, workspaceId)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()
  return { workspaceId, agentId, workingDir }
}

/**
 * Set one quake preference in this account's browser overrides.
 * Reload the workspace so the app reads that value during startup.
 *
 * The browser override takes precedence over the account setting from the Hub.
 * `setInitialBrowserPref` writes the value before reload so IndexedDB holds it when the app reads it.
 */
async function withQuakePref(page: Page, server: ServerInfo, field: string, value: unknown, workspaceId: string) {
  await setInitialBrowserPref(page, server.adminUserId, field, value)
  await page.reload()
  await openWorkspace(page, workspaceId)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()
}

/** The panel's parent clip determines its containing area and size. */
const clipOf = (page: Page) => page.locator(PANEL).locator('xpath=..')

/**
 * Read the centre area that contains the panel.
 *
 * The absolutely positioned clip uses the centre area as its `offsetParent`.
 * The centre area is its nearest positioned ancestor.
 * A resize handle cannot supply this measurement.
 * That handle spans the area's height but measures only a few pixels wide, so a width comparison would always pass.
 */
async function centreBox(page: Page) {
  return clipOf(page).evaluate((el) => {
    const parent = (el as HTMLElement).offsetParent as HTMLElement | null
    if (!parent)
      throw new Error('the quake clip has no positioned ancestor to anchor to')
    const rect = parent.getBoundingClientRect()
    return { x: rect.x, y: rect.y, width: rect.width, height: rect.height }
  })
}

/**
 * Select the visible quake terminal's xterm instance.
 *
 * The panel holds one container for each directory that this client opens.
 * It hides the other containers with `visibility: hidden`.
 * Use `data-active` because tabs in two directories create two xterm nodes inside the same panel.
 */
const quakeXterm = (page: Page) => page.locator(`${PANEL} [data-terminal-id][data-active="true"] .xterm`)

/**
 * Read the quake panel's own shell text.
 *
 * `getTerminalText` uses the module's last active terminal ID.
 * A terminal tab behind the panel creates a second mounted view.
 * Either view can update that ID, so an unscoped read can return the other view's text.
 * The panel's active terminal ID selects the correct shell.
 */
async function getQuakeTerminalText(page: Page): Promise<string> {
  const id = await page.locator(`${PANEL} [data-terminal-id][data-active="true"]`)
    .first()
    .getAttribute('data-terminal-id')
  if (!id)
    return ''
  return page.evaluate((terminalId) => {
    if (!window.__getTerminalTextById)
      throw new Error('The terminal text test hook is not registered.')
    return window.__getTerminalTextById(terminalId)
  }, id)
}

/** Run one command in the quake shell and wait for its output. */
async function runInQuake(page: Page, command: string, expected: string) {
  await quakeXterm(page).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await getQuakeTerminalText(page)).includes(expected)).toBe(true)
}

test.describe('Quake-mode terminal', () => {
  let initialWorkspaceIds: string[] | undefined
  test.beforeAll(async ({ leapmuxServer }) => {
    initialWorkspaceIds = (await listWorkspacesViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)).map(workspace => workspace.id).sort()
  })

  // This file must leave no workspace of its own behind. A workspace that existed before the
  // file started can vanish while it runs: an earlier file can leave an empty workspace that the
  // app removes when the first test of this file loads it. That is no leak of this file.
  test.afterAll(async ({ leapmuxServer }) => {
    if (!initialWorkspaceIds)
      return
    const workspaces = await listWorkspacesViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken)
    const leaked = workspaces.map(workspace => workspace.id).filter(id => !initialWorkspaceIds!.includes(id)).sort()
    expect(leaked).toEqual([])
  })

  test('nothing exists until the shortcut is pressed', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)
    // Wait until the tab metadata makes the shortcut usable.
    // Without this wait, incomplete page setup can satisfy the absence check even if the feature is deleted.
    await waitForActiveTabContext(page)

    // The first open creates the panel, xterm instance, and remote procedure call (RPC).
    await expect(panel(page)).toHaveCount(0)

    // Require the same page to open the panel from the shortcut.
    // This proves that the first open creates it and that delayed page setup did not satisfy the absence check.
    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
  })

  test('opens a shell over the centre area and runs a command', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await waitForTerminalReady(page)

    await runInQuake(page, 'echo quake-hello', 'quake-hello')
  })

  // A toggle hides the panel and keeps its shell alive.
  // Retained scrollback proves that the pseudo-terminal (PTY) and xterm instance survive.
  test('keeps the shell and its scrollback across a toggle', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo before-toggle', 'before-toggle')

    await toggleQuake(page)
    await expect(panel(page)).not.toBeInViewport()

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getTerminalText(page)).includes('before-toggle')).toBe(true)

    // Require new shell output. A restored screenshot cannot produce it.
    await runInQuake(page, 'echo after-toggle', 'after-toggle')
  })

  // Exit ends the quake terminal. The next open creates a shell with no retained scrollback.
  // An ordinary terminal tab stays visible and offers Enter to restart its shell.
  test('ends the shell on exit, and opens a fresh one next time', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo before-exit', 'before-exit')

    await quakeXterm(page).click()
    await page.keyboard.type('exit')
    await page.keyboard.press('Enter')
    await expect(panel(page)).not.toBeInViewport()

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await waitForTerminalReady(page)
    await expect.poll(async () => (await getTerminalText(page)).includes('before-exit')).toBe(false)
  })

  // Close the last tab while the panel is open.
  // Its directory's quake shell must stop because no tab can reach it afterward.
  //
  // Use the keyboard to close the tab.
  // The open panel at the top edge covers the tab strip, so a click on its close control would reach the panel instead.
  test('goes away with the last tab in its directory', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await expect(panel(page)).toBeInViewport()

    await page.keyboard.press(`${MOD}+KeyW`)

    // A running agent can require close confirmation.
    // Use the dialog's two-click ConfirmButton when that dialog appears.
    const busy = page.locator('dialog[data-testid="busy-tab-close-dialog"]')
    if (await busy.isVisible()) {
      await page.getByTestId('busy-tab-close-confirm').click()
      await page.getByRole('button', { name: 'Confirm?' }).click()
    }

    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible')).toHaveCount(0)
    await expect(panel(page)).toHaveCount(0)
  })

  // The Worker owns the shell, so reload attaches to the same shell.
  // The panel starts closed because its open state belongs to this client and is not persisted.
  test('re-attaches to the same shell after a reload', async ({ page, quakeServer }) => {
    const { workspaceId } = await openAgentTab(page, quakeServer)

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo survives-reload', 'survives-reload')

    await page.reload()
    await openWorkspace(page, workspaceId)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()
    await expect(panel(page)).toHaveCount(0)

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getTerminalText(page)).includes('survives-reload')).toBe(true)
  })

  test('slides in from the configured edge, at the configured size', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)
    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()

    // The default panel opens from the top edge and covers 65% of the centre area's height.
    await expect(panel(page)).toHaveAttribute('data-quake-orientation', 'top')

    // Poll the complete geometry assertion while the panel's transform changes.
    // `toBeInViewport` succeeds when the first pixel enters the viewport.
    // One subsequent `boundingBox()` call can measure an arbitrary transition frame.
    await expect(async () => {
      const centre = await centreBox(page)
      const panelBox = (await panel(page).boundingBox())!
      // Require the panel at the centre area's top edge with the full width of that area.
      expect(Math.abs(panelBox.y - centre.y)).toBeLessThan(4)
      expect(Math.abs(panelBox.width - centre.width)).toBeLessThan(4)
      expect(Math.abs(panelBox.height - centre.height * 0.65)).toBeLessThan(4)
    }).toPass()
  })

  /**
   * Check the other three edges and their size axes.
   *
   * One attribute selects both the panel's edge and size axis.
   * Require the panel at that edge and at the configured size on that axis.
   * An attribute-only check would accept a rule that positions the panel correctly but sizes the wrong axis.
   *
   * Measure the panel itself.
   * The clip spans the full centre area for every orientation so it can display the panel's shadow.
   */
  for (const [orientation, axis] of [['bottom', 'y'], ['left', 'x'], ['right', 'x']] as const) {
    test(`slides in from the ${orientation} edge`, async ({ page, quakeServer }) => {
      const { workspaceId } = await openAgentTab(page, quakeServer)
      await withQuakePref(page, quakeServer, 'quakeOrientation', orientation, workspaceId)

      await toggleQuake(page)
      await expect(panel(page)).toBeInViewport()

      await expect(panel(page)).toHaveAttribute('data-quake-orientation', orientation)

      // Poll the complete geometry assertion, as the default-edge case does.
      await expect(async () => {
        const centre = await centreBox(page)
        const panelBox = (await panel(page).boundingBox())!
        if (axis === 'y') {
          // Require the panel at the bottom edge of the centre area.
          // Require full width and 65% of that area's height.
          expect(Math.abs((panelBox.y + panelBox.height) - (centre.y + centre.height))).toBeLessThan(4)
          expect(Math.abs(panelBox.width - centre.width)).toBeLessThan(4)
          expect(Math.abs(panelBox.height - centre.height * 0.65)).toBeLessThan(4)
        }
        else {
          // Require the panel at the selected side edge of the centre area.
          // Require full height and 65% of that area's width.
          expect(Math.abs(panelBox.height - centre.height)).toBeLessThan(4)
          expect(Math.abs(panelBox.width - centre.width * 0.65)).toBeLessThan(4)
          if (orientation === 'left')
            expect(Math.abs(panelBox.x - centre.x)).toBeLessThan(4)
          else
            expect(Math.abs((panelBox.x + panelBox.width) - (centre.x + centre.width))).toBeLessThan(4)
        }
      }).toPass()
    })
  }

  /**
   * The background applies the configured opacity. The terminal text stays opaque.
   *
   * Read the computed background's alpha to verify the complete preference path:
   * - The app parses the preference.
   * - The custom property supplies the percentage to `color-mix`.
   * - The xterm instance leaves the panel's background visible.
   *
   * Browsers serialize `color-mix` differently.
   * Accept both supported spellings and assert only alpha.
   */
  test('paints its background at the configured opacity', async ({ page, quakeServer }) => {
    const { workspaceId } = await openAgentTab(page, quakeServer)
    await withQuakePref(page, quakeServer, 'quakeBackgroundOpacity', 0.5, workspaceId)

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()

    await expect
      .poll(async () => clipOf(page).evaluate(el => getComputedStyle(el).getPropertyValue('--quake-opacity').trim()))
      .toBe('50%')

    const alpha = await panel(page).evaluate((el) => {
      const bg = getComputedStyle(el).backgroundColor
      const rgba = bg.match(/^rgba?\(([^)]*)\)$/)
      if (rgba) {
        const rawParts = rgba[1]
        if (rawParts === undefined)
          return 1
        const parts = rawParts.split(/[,/]/).map(part => part.trim()).filter(Boolean)
        return parts.length === 4 ? Number(parts[3]) : 1
      }
      const srgb = bg.match(/\/\s*([\d.]+)\s*\)$/)
      return srgb ? Number(srgb[1]) : 1
    })
    expect(alpha).toBeCloseTo(0.5, 2)
  })

  test('keeps the configured opacity while the terminal starts', async ({ page, quakeServer }) => {
    const { workspaceId } = await openAgentTab(page, quakeServer)
    await withQuakePref(page, quakeServer, 'quakeBackgroundOpacity', 0.5, workspaceId)

    await page.evaluate(() => {
      const recordStartupAlpha = () => {
        const overlay = document.querySelector(
          '[data-testid="quake-panel"] [data-testid="terminal-startup-overlay"]',
        )
        if (!(overlay instanceof HTMLElement))
          return false

        const canvas = document.createElement('canvas')
        canvas.width = 1
        canvas.height = 1
        const context = canvas.getContext('2d')
        if (!context)
          throw new Error('The browser did not supply a 2D canvas context')
        context.fillStyle = getComputedStyle(overlay).backgroundColor
        context.fillRect(0, 0, 1, 1)
        const alpha = context.getImageData(0, 0, 1, 1).data[3]
        if (alpha === undefined)
          throw new Error('The canvas returned no alpha byte.')
        window.__quakeStartupOverlayAlpha = alpha
        return true
      }

      window.__quakeStartupOverlayAlpha = null
      const observer = new MutationObserver(() => {
        if (recordStartupAlpha())
          observer.disconnect()
      })
      observer.observe(document.body, { childList: true, subtree: true })
    })

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await expect
      .poll(() => page.evaluate(() => window.__quakeStartupOverlayAlpha))
      .toBe(0)
  })

  test('uses the terminal theme background when the UI theme differs', async ({ page, quakeServer }) => {
    const { workspaceId } = await openAgentTab(page, quakeServer)
    await setInitialBrowserPref(page, quakeServer.adminUserId, 'theme', { name: 'catppuccin', mode: 'light' })
    await setInitialBrowserPref(page, quakeServer.adminUserId, 'terminalTheme', { name: 'nord', mode: 'dark' })
    await setInitialBrowserPref(page, quakeServer.adminUserId, 'quakeBackgroundOpacity', 1)
    await page.reload()
    await openWorkspace(page, workspaceId)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()

    await openTerminalViaUI(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]:visible')).toHaveCount(1)
    await waitForTerminalReady(page)
    const ordinaryBackground = await backgroundPixel(
      page.locator('[data-terminal-id][data-active="true"] .xterm-scrollable-element'),
    )

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await expect.poll(() => backgroundPixel(panel(page)))
      .toEqual(ordinaryBackground)
  })

  // Keep the closed panel mounted so its shell survives a toggle.
  // Remove the closed panel from accessibility navigation and tab order.
  // Otherwise, keyboard focus can enter a terminal outside the viewport.
  test('keeps a hidden panel out of the tab order', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await toggleQuake(page)

    await expect(panel(page)).not.toBeInViewport()
    await expect(panel(page)).toHaveAttribute('aria-hidden', 'true')
    await expect(panel(page)).toHaveAttribute('inert', '')
  })

  test('appears at once when the system asks for reduced motion', async ({ page, quakeServer }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await openAgentTab(page, quakeServer)

    await toggleQuake(page)

    // Reduced motion disables the transition, so the panel reaches its position on the next frame.
    await expect(panel(page)).toBeInViewport()
  })

  /**
   * Two agent tabs in one directory share one shell and its scrollback.
   * A build that a user starts from one tab remains visible from the other tab.
   *
   * Switch tabs with the keyboard.
   * The open panel at the top edge covers the tab strip.
   */
  test('shares one shell between two agent tabs in one directory', async ({ page, quakeServer }) => {
    const { hubUrl, adminToken, workerId } = quakeServer
    const dir = freshDir()
    const { workspaceId } = quakeServer
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, dir)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, dir)
    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible')).toHaveCount(2)

    await page.keyboard.press('Alt+Digit1')
    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo from-tab-one', 'from-tab-one')

    // Select the second tab without another toggle.
    // Both tabs use this directory's shell, so the panel must stay open in the same position.
    await page.keyboard.press('Alt+Digit2')
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getTerminalText(page)).includes('from-tab-one')).toBe(true)

    // Require output from the second tab after selecting the first tab again.
    // That output proves that both tabs share one PTY.
    await runInQuake(page, 'echo from-tab-two', 'from-tab-two')
    await page.keyboard.press('Alt+Digit1')
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getTerminalText(page)).includes('from-tab-two')).toBe(true)
  })

  /**
   * Two directories have separate shells and separate scrollback.
   * Switching directories selects the corresponding terminal without disposing it.
   */
  test('keeps a separate shell for each directory', async ({ page, quakeServer }) => {
    const { hubUrl, adminToken, workerId } = quakeServer
    const dirOne = freshDir()
    const dirTwo = freshDir()
    const { workspaceId } = quakeServer
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, dirOne)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, dirTwo)
    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible')).toHaveCount(2)

    await page.keyboard.press('Alt+Digit1')
    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo shell-one', 'shell-one')

    await page.keyboard.press('Alt+Digit2')
    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo shell-two', 'shell-two')
    expect(await getTerminalText(page)).not.toContain('shell-one')

    // Select the first directory again.
    // Its panel must stay open and retain its own scrollback.
    // A shared or recreated terminal cannot preserve both directories' separate output.
    await page.keyboard.press('Alt+Digit1')
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getTerminalText(page)).includes('shell-one')).toBe(true)
    expect(await getTerminalText(page)).not.toContain('shell-two')
  })

  /**
   * A terminal tab in the same directory opens the same quake shell as an agent tab.
   * The shortcut's `when` condition must permit both tab types.
   */
  test('opens the same shell over a terminal tab', async ({ page, quakeServer }) => {
    await openAgentTab(page, quakeServer)

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo from-the-agent-tab', 'from-the-agent-tab')
    // Hide the panel before opening the terminal tab.
    // The open panel covers the new-terminal button.
    await toggleQuake(page)
    await expect(panel(page)).not.toBeInViewport()

    // The new-terminal button uses the active tab's working directory.
    await openTerminalViaUI(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]:visible')).toHaveCount(1)

    // Require the shortcut to work from the terminal tab.
    // Require the same shell output that the agent tab produced.
    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getQuakeTerminalText(page)).includes('from-the-agent-tab')).toBe(true)
  })

  /**
   * Devices share the shell and keep separate panel open states.
   *
   * The Worker uses `is_quake` and a unique partial index over `working_dir` to reuse the directory's live quake terminal.
   * The second browser attaches to that terminal instead of starting another shell.
   */
  test('shares one shell between two browsers on the same account', async ({ page, browser, quakeServer }) => {
    const { workspaceId } = await openAgentTab(page, quakeServer)
    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo from-browser-a', 'from-browser-a')

    const context = await browser.newContext({ baseURL: quakeServer.hubUrl })
    const second = await context.newPage()
    try {
      await loginViaToken(second, quakeServer.adminToken)
      await openWorkspace(second, workspaceId)
      await expect(second.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()

      // The open state is not persisted, so the second browser starts with no panel.
      await expect(second.locator(PANEL)).toHaveCount(0)

      await toggleQuake(second)
      await expect(second.locator(PANEL)).toBeInViewport()
      // Retained output proves that this browser attaches to the first browser's shell.
      await expect.poll(async () => (await getTerminalText(second)).includes('from-browser-a')).toBe(true)

      // Require output from the second browser in the first browser to verify shared PTY input.
      await runInQuake(second, 'echo from-browser-b', 'from-browser-b')
      await expect.poll(async () => (await getTerminalText(page)).includes('from-browser-b')).toBe(true)

      // Hiding the panel in the first browser must keep the second browser's panel and shared shell available.
      await toggleQuake(page)
      await expect(panel(page)).not.toBeInViewport()
      await expect(second.locator(PANEL)).toBeInViewport()
      await runInQuake(second, 'echo still-alive', 'still-alive')
    }
    finally {
      await context.close()
    }
  })

  /**
   * `leapmux control terminal quake ...` sends a panel command to connected browsers.
   *
   * The command addresses a working directory.
   * `--working-dir` defaults to $LEAPMUX_CONTROL_WORKING_DIR, so the bare command works inside the panel that it controls.
   *
   * The Worker sends a transient event and stores no panel state.
   * Each browser keeps its own active tab and processes the event independently.
   */
  test('moves the panel from the Control CLI, in every open browser', async ({ page, browser, quakeServer }) => {
    const { workspaceId, workingDir } = await openAgentTab(page, quakeServer)
    const cli = await mintCLITokenForAdmin(quakeServer)

    const context = await browser.newContext({ baseURL: quakeServer.hubUrl })
    const second = await context.newPage()
    try {
      await loginViaToken(second, quakeServer.adminToken)
      await openWorkspace(second, workspaceId)
      await expect(second.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()
      // Open and toggle commands use `findTabInWorkingDir` to locate a tab with the requested directory.
      // A tab without hydrated metadata cannot match that directory.
      // The event is transient and has no retry, so both pages need that metadata before these commands arrive.
      // Close uses the directory key directly and does not require a matching tab.
      await waitForActiveTabContext(page)
      await waitForActiveTabContext(second)

      await runCLI(cli, ['terminal', 'quake', 'open', '--worker-id', quakeServer.workerId, '--working-dir', workingDir])
      await expect(panel(page)).toBeInViewport()
      await expect(second.locator(PANEL)).toBeInViewport()

      await runCLI(cli, ['terminal', 'quake', 'close', '--worker-id', quakeServer.workerId, '--working-dir', workingDir])
      await expect(panel(page)).not.toBeInViewport()
      await expect(second.locator(PANEL)).not.toBeInViewport()

      await runCLI(cli, ['terminal', 'quake', 'toggle', '--worker-id', quakeServer.workerId, '--working-dir', workingDir])
      await expect(panel(page)).toBeInViewport()
      await expect(second.locator(PANEL)).toBeInViewport()
    }
    finally {
      await context.close()
    }
  })
})
