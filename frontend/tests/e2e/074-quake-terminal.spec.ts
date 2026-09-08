import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { getTerminalText, waitForTerminalReady } from './helpers/terminal'
import { loginViaToken, openWorkspace } from './helpers/ui'

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
  await openAgentViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId, process.cwd())
  await loginViaToken(page, server.adminToken)
  await openWorkspace(page, workspaceId)
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]:visible').first()).toBeVisible()
  return workspaceId
}

/** Run one command in the quake shell and wait for its output. */
async function runInQuake(page: Page, command: string, expected: string) {
  await page.locator(`${PANEL} .xterm`).click()
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

    await page.locator(`${PANEL} .xterm`).click()
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
    const workspaceId = await openAgentTab(page, leapmuxServer, 'Quake Reload')

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
    const clip = page.locator(PANEL).locator('xpath=..')
    await expect(clip).toHaveAttribute('data-quake-orientation', 'top')

    const centre = page.locator('[data-testid="resize-handle"]').first()
    const clipBox = await clip.boundingBox()
    const centreBox = await centre.boundingBox()
    expect(clipBox).not.toBeNull()
    expect(centreBox).not.toBeNull()
    // Anchored to the top of the centre band, not to the window.
    expect(Math.abs(clipBox!.y - centreBox!.y)).toBeLessThan(4)
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
})
