import type { Page } from '@playwright/test'
import type { ServerInfo } from './fixtures'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { hubDataDir } from './helpers/server'
import { getTerminalText, waitForTerminalReady } from './helpers/terminal'
import { loginViaToken, openWorkspace, setInitialBrowserPref } from './helpers/ui'

/**
 * The Quake terminal: a shell that slides over the centre area for one agent
 * tab.
 *
 * The panel is always mounted once it exists and slides by transform, so it is
 * "visible" to Playwright whether it is up or down — `toBeInViewport` is the
 * oracle, the same one the mobile drawers document.
 */
const PANEL = '[data-testid="quake-panel"]'

const MOD = process.platform === 'darwin' ? 'Meta' : 'Control'

async function toggleQuake(page: Page) {
  await page.keyboard.press(`${MOD}+KeyJ`)
}

const panel = (page: Page) => page.locator(PANEL)

/** Open an agent tab and land on it, which is the state every case starts in. */
async function openAgentTab(page: Page, server: { hubUrl: string, adminToken: string, workerId: string }, title: string) {
  const workspaceId = await createWorkspaceViaAPI(server.hubUrl, server.adminToken, title)
  const agentId = await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, process.cwd())
  await loginViaToken(page, server.adminToken)
  await openWorkspace(page, workspaceId)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()
  return { workspaceId, agentId }
}

/**
 * Seed one device-tier quake preference, then reload onto the workspace so the
 * app reads it at start-up.
 *
 * The device tier is what a spec can set without a round trip: the account
 * value is the fallback, and an override beats it. See `setInitialBrowserPref`
 * for why the seed has to land before the reload rather than as an init script.
 */
async function withQuakePref(page: Page, server: ServerInfo, field: string, value: unknown, workspaceId: string) {
  await setInitialBrowserPref(page, server.adminUserId, field, value)
  await page.reload()
  await openWorkspace(page, workspaceId)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()
}

/** The CLI credential source for this fixture's hub. Mirrors 141's helper. */
function cliTokenSource(server: ServerInfo) {
  return { hubUrl: server.hubUrl, adminToken: server.adminToken, dataDir: hubDataDir(server.dataDir) }
}

/** The clip is the panel's parent: it holds the anchoring and the size. */
const clipOf = (page: Page) => page.locator(PANEL).locator('xpath=..')

/**
 * The box the panel is anchored to: the centre area.
 *
 * Read as the clip's own `offsetParent`, which IS that element by definition --
 * the clip is absolutely positioned, and `center` is the nearest positioned
 * ancestor. A resize handle is not a substitute: it spans the band's height but
 * is a few pixels wide, so a width comparison against it always passes.
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
 * The xterm of the quake terminal that is SHOWING.
 *
 * Scoped by `data-active`, because the panel holds one container per companion
 * this client has open and hides the rest with `visibility: hidden` -- so a
 * user with two agent tabs has two `.xterm` nodes inside one panel.
 */
const quakeXterm = (page: Page) => page.locator(`${PANEL} [data-terminal-id][data-active="true"] .xterm`)

/** Run one command in the quake shell and wait for its output. */
async function runInQuake(page: Page, command: string, expected: string) {
  await quakeXterm(page).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect.poll(async () => (await getTerminalText(page)).includes(expected)).toBe(true)
}

test.describe('Quake-mode terminal', () => {
  test('nothing exists until the shortcut is pressed', async ({ page, leapmuxServer }) => {
    await openAgentTab(page, leapmuxServer, 'Quake Lazy')

    // Lazy: no panel, no xterm, no RPC for a user who never opens it.
    await expect(panel(page)).toHaveCount(0)
  })

  test('opens a shell over the centre area and runs a command', async ({ page, leapmuxServer }) => {
    await openAgentTab(page, leapmuxServer, 'Quake Open')

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await waitForTerminalReady(page)

    await runInQuake(page, 'echo quake-hello', 'quake-hello')
  })

  // The point of the feature: a toggle hides the panel, it does not end the
  // shell. The scrollback proves the PTY and the xterm both survived.
  test('keeps the shell and its scrollback across a toggle', async ({ page, leapmuxServer }) => {
    await openAgentTab(page, leapmuxServer, 'Quake Toggle')

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo before-toggle', 'before-toggle')

    await toggleQuake(page)
    await expect(panel(page)).not.toBeInViewport()

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getTerminalText(page)).includes('before-toggle')).toBe(true)

    // And it still answers, which a restored screenshot would not.
    await runInQuake(page, 'echo after-toggle', 'after-toggle')
  })

  // Exiting ENDS a quake terminal, unlike a terminal tab, which stays on screen
  // offering Enter to restart. The next open gets a shell with no history.
  test('ends the shell on exit, and opens a fresh one next time', async ({ page, leapmuxServer }) => {
    await openAgentTab(page, leapmuxServer, 'Quake Exit')

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

  // Closed while the panel is still OPEN, which is the interesting case: the
  // companion has to go with its owner rather than outlive it.
  //
  // Through the keyboard, not the tab strip's X. A top-anchored panel covers the
  // strip while it is open -- it spans the whole centre area by design -- so the
  // click would land on the terminal underneath instead.
  test('goes away with its agent tab', async ({ page, leapmuxServer }) => {
    await openAgentTab(page, leapmuxServer, 'Quake Close Owner')

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await expect(panel(page)).toBeInViewport()

    await page.keyboard.press(`${MOD}+KeyW`)

    // The agent is running, so the close asks first. Confirm through the
    // two-click ConfirmButton the dialog uses.
    const busy = page.locator('dialog[data-testid="busy-tab-close-dialog"]')
    if (await busy.isVisible()) {
      await page.getByTestId('busy-tab-close-confirm').click()
      await page.getByRole('button', { name: 'Confirm?' }).click()
    }

    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible')).toHaveCount(0)
    await expect(panel(page)).toHaveCount(0)
  })

  // The shell lives on the Worker, so a reload re-attaches to it rather than
  // starting another. The panel itself starts closed, because whether it shows
  // is per-device UI state that nothing persists.
  test('re-attaches to the same shell after a reload', async ({ page, leapmuxServer }) => {
    const { workspaceId } = await openAgentTab(page, leapmuxServer, 'Quake Reload')

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

  test('slides in from the configured edge, at the configured size', async ({ page, leapmuxServer }) => {
    await openAgentTab(page, leapmuxServer, 'Quake Geometry')
    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()

    // The default: the top edge, covering 65% of the centre area.
    const clip = clipOf(page)
    await expect(clip).toHaveAttribute('data-quake-orientation', 'top')

    const centre = await centreBox(page)
    const clipBox = (await clip.boundingBox())!
    // Anchored to the top of the centre band, not to the window, and spanning
    // it sideways.
    expect(Math.abs(clipBox.y - centre.y)).toBeLessThan(4)
    expect(Math.abs(clipBox.width - centre.width)).toBeLessThan(4)
    expect(Math.abs(clipBox.height - centre.height * 0.65)).toBeLessThan(4)
  })

  /**
   * The other three edges, and the axis each one drives.
   *
   * One attribute picks the anchoring AND the size axis, so the check that
   * matters is that the clip is flush with the edge it names and short (or
   * narrow) on the axis that edge implies. A rule that anchored correctly but
   * sized the wrong axis would still pass an attribute-only assertion.
   */
  for (const [orientation, axis] of [['bottom', 'y'], ['left', 'x'], ['right', 'x']] as const) {
    test(`slides in from the ${orientation} edge`, async ({ page, leapmuxServer }) => {
      const { workspaceId } = await openAgentTab(page, leapmuxServer, `Quake ${orientation}`)
      await withQuakePref(page, leapmuxServer, 'quakeOrientation', orientation, workspaceId)

      await toggleQuake(page)
      await expect(panel(page)).toBeInViewport()

      const clip = clipOf(page)
      await expect(clip).toHaveAttribute('data-quake-orientation', orientation)
      await expect(panel(page)).toHaveAttribute('data-quake-orientation', orientation)

      const centre = await centreBox(page)
      const clipBox = (await clip.boundingBox())!
      if (axis === 'y') {
        // Bottom-anchored: flush with the bottom of the centre band, full
        // width, and 65% of its height.
        expect(Math.abs((clipBox.y + clipBox.height) - (centre.y + centre.height))).toBeLessThan(4)
        expect(Math.abs(clipBox.width - centre.width)).toBeLessThan(4)
        expect(Math.abs(clipBox.height - centre.height * 0.65)).toBeLessThan(4)
      }
      else {
        // Side-anchored: full height, 65% of the width, and flush with the edge
        // it names.
        expect(Math.abs(clipBox.height - centre.height)).toBeLessThan(4)
        expect(Math.abs(clipBox.width - centre.width * 0.65)).toBeLessThan(4)
        if (orientation === 'left')
          expect(Math.abs(clipBox.x - centre.x)).toBeLessThan(4)
        else
          expect(Math.abs((clipBox.x + clipBox.width) - (centre.x + centre.width))).toBeLessThan(4)
      }
    })
  }

  /**
   * The background carries the opacity; the terminal text does not.
   *
   * The assertion reads the ALPHA of the computed background rather than the
   * declared `color-mix`, because that is the whole chain the setting has to
   * survive: the preference parse, the custom property, the `color-mix`, and
   * xterm leaving the surface alone. `color-mix` serializes differently across
   * engines, so both spellings are accepted and only the alpha is asserted.
   */
  test('paints its background at the configured opacity', async ({ page, leapmuxServer }) => {
    const { workspaceId } = await openAgentTab(page, leapmuxServer, 'Quake Opacity')
    await withQuakePref(page, leapmuxServer, 'quakeBackgroundOpacity', 0.5, workspaceId)

    await toggleQuake(page)
    await expect(panel(page)).toBeInViewport()

    await expect
      .poll(async () => clipOf(page).evaluate(el => getComputedStyle(el).getPropertyValue('--quake-opacity').trim()))
      .toBe('50%')

    const alpha = await panel(page).evaluate((el) => {
      const bg = getComputedStyle(el).backgroundColor
      const rgba = bg.match(/^rgba?\(([^)]*)\)$/)
      if (rgba) {
        const parts = rgba[1].split(/[,/]/).map(part => part.trim()).filter(Boolean)
        return parts.length === 4 ? Number(parts[3]) : 1
      }
      const srgb = bg.match(/\/\s*([\d.]+)\s*\)$/)
      return srgb ? Number(srgb[1]) : 1
    })
    expect(alpha).toBeCloseTo(0.5, 2)
  })

  // The panel is in the DOM while closed so the shell survives a toggle, which
  // means it must be out of the accessibility tree and out of the tab order --
  // otherwise Tab lands a keyboard user in a terminal that is off screen.
  test('keeps a hidden panel out of the tab order', async ({ page, leapmuxServer }) => {
    await openAgentTab(page, leapmuxServer, 'Quake Inert')

    await toggleQuake(page)
    await waitForTerminalReady(page)
    await toggleQuake(page)

    await expect(panel(page)).not.toBeInViewport()
    await expect(panel(page)).toHaveAttribute('aria-hidden', 'true')
    await expect(panel(page)).toHaveAttribute('inert', '')
  })

  test('appears at once when the system asks for reduced motion', async ({ page, leapmuxServer }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await openAgentTab(page, leapmuxServer, 'Quake Reduced Motion')

    await toggleQuake(page)

    // No animation to wait out: the panel is in place on the next frame.
    await expect(panel(page)).toBeInViewport()
  })

  /**
   * One shell PER AGENT TAB, and switching tabs switches panels with no
   * dispose: each terminal keeps its own xterm and its own scrollback.
   *
   * Tabs are switched from the keyboard, not the strip: a top-anchored panel
   * covers the strip while it is open, by design.
   */
  test('keeps a separate shell for each agent tab', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Quake Two Tabs')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, process.cwd())
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, process.cwd())
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

    // Back to the first: its panel is still open and its scrollback intact,
    // which a shared or re-created terminal could not manage.
    await page.keyboard.press('Alt+Digit1')
    await expect(panel(page)).toBeInViewport()
    await expect.poll(async () => (await getTerminalText(page)).includes('shell-one')).toBe(true)
    expect(await getTerminalText(page)).not.toContain('shell-two')
  })

  /**
   * The contract in one case: the SHELL is shared by every device, and whether
   * the panel SHOWS is not.
   *
   * `owner_agent_id` and its unique partial index are what make the second
   * browser adopt the first one's terminal instead of spawning a second.
   */
  test('shares one shell between two browsers on the same account', async ({ page, browser, leapmuxServer }) => {
    const { workspaceId } = await openAgentTab(page, leapmuxServer, 'Quake Shared')
    await toggleQuake(page)
    await waitForTerminalReady(page)
    await runInQuake(page, 'echo from-browser-a', 'from-browser-a')

    const context = await browser.newContext({ baseURL: leapmuxServer.hubUrl })
    const second = await context.newPage()
    try {
      await loginViaToken(second, leapmuxServer.adminToken)
      await openWorkspace(second, workspaceId)
      await expect(second.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()

      // Nothing persisted the open state, so the second device starts closed.
      await expect(second.locator(PANEL)).toHaveCount(0)

      await toggleQuake(second)
      await expect(second.locator(PANEL)).toBeInViewport()
      // Adopted, not spawned: the first browser's output is already there.
      await expect.poll(async () => (await getTerminalText(second)).includes('from-browser-a')).toBe(true)

      // And it is one PTY in both directions.
      await runInQuake(second, 'echo from-browser-b', 'from-browser-b')
      await expect.poll(async () => (await getTerminalText(page)).includes('from-browser-b')).toBe(true)

      // Closing on A is a per-device act: B keeps its panel and its shell.
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
   * `leapmux control agent quake ...` as a remote keystroke.
   *
   * It stores nothing -- the worker relays it as a transient event -- which is
   * what lets it move a panel while the active tab stays client-local. Both
   * open browsers therefore act on it.
   */
  test('moves the panel from the Control CLI, in every open browser', async ({ page, browser, leapmuxServer }) => {
    const { workspaceId, agentId } = await openAgentTab(page, leapmuxServer, 'Quake CLI')
    const cli = await mintCLITokenForAdmin(cliTokenSource(leapmuxServer))

    const context = await browser.newContext({ baseURL: leapmuxServer.hubUrl })
    const second = await context.newPage()
    try {
      await loginViaToken(second, leapmuxServer.adminToken)
      await openWorkspace(second, workspaceId)
      await expect(second.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()

      await runCLI(cli, ['agent', 'quake', 'open', '--tab-id', agentId])
      await expect(panel(page)).toBeInViewport()
      await expect(second.locator(PANEL)).toBeInViewport()

      await runCLI(cli, ['agent', 'quake', 'close', '--tab-id', agentId])
      await expect(panel(page)).not.toBeInViewport()
      await expect(second.locator(PANEL)).not.toBeInViewport()

      await runCLI(cli, ['agent', 'quake', 'toggle', '--tab-id', agentId])
      await expect(panel(page)).toBeInViewport()
      await expect(second.locator(PANEL)).toBeInViewport()
    }
    finally {
      await context.close()
    }
  })
})
