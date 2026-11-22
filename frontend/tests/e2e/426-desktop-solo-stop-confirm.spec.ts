import type { Page } from '@playwright/test'
import type { SoloBusyWork } from '../../src/api/platformBridge'
import { expect } from '@playwright/test'
import { TAURI_EVENT_APP_CONFIRM_QUIT } from '../../src/generated/contracts/desktop'
import { test } from './fixtures'
import { installDesktopBridgeMock } from './helpers/desktopProxyMock'
import { loginViaToken, openWorkspace } from './helpers/ui'

interface SoloStopHarness {
  busyWork: SoloBusyWork
  ackedPromptIds: number[]
  resolutions: Array<{ promptId: number, confirm: boolean }>
  switchModeCalls: number
  emitQuitConfirm: (promptId: number, busyWork: SoloBusyWork) => void
  setBusyWork: (busyWork: SoloBusyWork) => void
}

const SAMPLE_BUSY_WORK: SoloBusyWork = {
  agents: [
    {
      tabId: 'tab-agent-1',
      title: 'Refactor auth',
      activeBackgroundTasks: ['Run backend tests'],
      activeChildAgents: ['Explore repository'],
    },
  ],
  terminals: [
    {
      tabId: 'tab-term-1',
      title: 'dev-shell',
      processes: [
        { pid: 4242, name: 'sleep' },
        { pid: 4243, name: 'vite' },
      ],
      totalCount: 2,
    },
  ],
}

const EMPTY_BUSY_WORK: SoloBusyWork = {
  agents: [],
  terminals: [],
}

/**
 * Installs a mock Tauri Solo-mode bridge before page load so the desktop shell
 * UI, quit-confirmation host, and switch-mode menu item run under Playwright.
 */
async function addSoloDesktopMockInitScript(page: Page, initialBusyWork: SoloBusyWork) {
  await installDesktopBridgeMock(page)
  await page.addInitScript(({ quitEvent, initialBusyWork }) => {
    const win = window as any
    let connected = true
    let shellMode: 'solo' | 'launcher' = 'solo'

    const harness: SoloStopHarness = {
      busyWork: initialBusyWork,
      ackedPromptIds: [],
      resolutions: [],
      switchModeCalls: 0,
      emitQuitConfirm: (promptId, busyWork) => {
        win.__TAURI_INTERNALS__.emitEvent(quitEvent, { promptId, busyWork })
      },
      setBusyWork: (busyWork) => {
        harness.busyWork = busyWork
      },
    }
    win.__soloStopHarness = harness

    win.__TAURI_INVOKE__ = (cmd: string, args?: any) => {
      switch (cmd) {
        case 'get_runtime_state':
          return Promise.resolve({
            shellMode,
            connected,
            hubUrl: connected
              ? window.location.origin
              : '',
            capabilities: {
              mode: 'tauri-desktop-solo',
              tunnels: true,
              appControl: true,
              windowControl: true,
              systemPermissions: true,
              localSolo: true,
            },
          })
        case 'get_startup_info':
          return Promise.resolve({
            config: {
              mode: 'solo',
              hub_url: '',
              window_width: 1280,
              window_height: 800,
              window_mode: 'normal',
            },
            build_info: {
              version: '0.0.0-e2e',
              commit_hash: 'e2e',
              commit_time: '2026-01-01T00:00:00Z',
              build_time: '2026-01-01T00:00:00Z',
              branch: 'main',
            },
            launch_visibility: 'normal',
          })
        case 'check_full_disk_access':
          return Promise.resolve(true)
        case 'begin_switch_mode': {
          const isEmpty = harness.busyWork.agents.length === 0 && harness.busyWork.terminals.length === 0
          if (isEmpty)
            return Promise.resolve({ kind: 'proceed', switchId: 1 })
          return Promise.resolve({
            kind: 'confirm',
            promptId: 1,
            busyWork: harness.busyWork,
          })
        }
        case 'resolve_switch_mode_confirmation':
          return Promise.resolve(args.confirm ? 1 : null)
        case 'cancel_switch_mode':
          return Promise.resolve()
        case 'ack_quit_confirmation':
          harness.ackedPromptIds.push(args.promptId)
          return Promise.resolve(true)
        case 'resolve_quit_confirmation':
          harness.resolutions.push({
            promptId: args.promptId,
            confirm: args.confirm,
          })
          return Promise.resolve()
        case 'switch_mode':
          harness.switchModeCalls++
          connected = false
          shellMode = 'launcher'
          return Promise.resolve()
        case 'list_tunnels':
          return Promise.resolve({ tunnels: [], channelEpoch: 'epoch-1' })
        case 'reset_tunnels':
        case 'set_desktop_behavior':
        case 'save_window_geometry':
          return Promise.resolve()
        case 'list_external_apps':
          return Promise.resolve([])
        default:
          return Promise.reject(new Error(`unhandled Tauri invoke: ${cmd}`))
      }
    }
  }, {
    quitEvent: TAURI_EVENT_APP_CONFIRM_QUIT,
    initialBusyWork,
  })
}

async function readHarnessState(page: Page) {
  return page.evaluate(() => {
    const h = (window as unknown as { __soloStopHarness: SoloStopHarness }).__soloStopHarness
    return {
      ackedPromptIds: [...h.ackedPromptIds],
      resolutions: [...h.resolutions],
      switchModeCalls: h.switchModeCalls,
    }
  })
}

async function emitQuitConfirm(page: Page, promptId: number, busyWork: SoloBusyWork) {
  await page.evaluate(
    ({ promptId, busyWork }) => {
      const h = (window as unknown as { __soloStopHarness: SoloStopHarness }).__soloStopHarness
      h.emitQuitConfirm(promptId, busyWork)
    },
    { promptId, busyWork },
  )
}

test.describe('desktop Solo stop confirmation', () => {
  test.beforeEach(async ({ page, emptyWorkspace, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await addSoloDesktopMockInitScript(page, SAMPLE_BUSY_WORK)
    await openWorkspace(page, emptyWorkspace.workspaceId)
    await expect(page.getByTestId('app-menu-trigger')).toBeVisible()
  })

  test('displays busy agent and terminal details for quit events, ignores Enter, and cancels cleanly', async ({ page }) => {
    await emitQuitConfirm(page, 101, SAMPLE_BUSY_WORK)

    const dialog = page.getByTestId('solo-stop-confirm-dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole('heading', { name: 'Quit LeapMux' })).toBeVisible()
    await expect(dialog).toContainText('Quitting stops work in 2 tabs:')
    await expect(dialog).toContainText('Refactor auth')
    await expect(dialog).toContainText('Run backend tests')
    await expect(dialog).toContainText('Explore repository')
    await expect(dialog).toContainText('dev-shell')
    await expect(dialog).toContainText('sleep')
    await expect(dialog).toContainText('vite')

    // Enter must not bypass the two-click danger button.
    await page.keyboard.press('Enter')
    await expect(dialog).toBeVisible()
    expect(await readHarnessState(page)).toEqual({
      ackedPromptIds: [101],
      resolutions: [],
      switchModeCalls: 0,
    })

    await dialog.getByTestId('solo-stop-confirm-cancel').click()
    await expect(dialog).not.toBeVisible()
    expect(await readHarnessState(page)).toEqual({
      ackedPromptIds: [101],
      resolutions: [
        { promptId: 101, confirm: false },
      ],
      switchModeCalls: 0,
    })
  })

  test('requires two clicks on Quit anyway before resolving the quit confirmation as true', async ({ page }) => {
    await emitQuitConfirm(page, 102, SAMPLE_BUSY_WORK)

    const dialog = page.getByTestId('solo-stop-confirm-dialog')
    await expect(dialog).toBeVisible()

    const confirmBtn = dialog.getByTestId('solo-stop-confirm-confirm')
    await expect(confirmBtn).toHaveAccessibleName('Quit anyway')

    await confirmBtn.click()
    await expect(confirmBtn).toHaveAccessibleName('Confirm?')
    expect(await readHarnessState(page)).toEqual({
      ackedPromptIds: [102],
      resolutions: [],
      switchModeCalls: 0,
    })

    await confirmBtn.click()
    await expect(dialog).not.toBeVisible()
    expect(await readHarnessState(page)).toEqual({
      ackedPromptIds: [102],
      resolutions: [
        { promptId: 102, confirm: true },
      ],
      switchModeCalls: 0,
    })
  })

  test('confirms Switch mode when solo work is active before calling switch_mode', async ({ page }) => {
    await page.getByTestId('app-menu-trigger').click()
    await page.getByRole('menuitem', { name: 'Switch mode...' }).click()

    const dialog = page.getByTestId('solo-stop-confirm-dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole('heading', { name: 'Switch mode' })).toBeVisible()
    await expect(dialog).toContainText('Switching mode stops work in 2 tabs:')

    // Cancelling leaves no fade overlay and skips switch_mode.
    await dialog.getByTestId('solo-stop-confirm-cancel').click()
    await expect(dialog).not.toBeVisible()
    expect(await page.locator('[style*="2147483647"]').count()).toBe(0)
    expect((await readHarnessState(page)).switchModeCalls).toBe(0)

    // Repeat and two-click confirm.
    await page.getByTestId('app-menu-trigger').click()
    await page.getByRole('menuitem', { name: 'Switch mode...' }).click()
    await expect(dialog).toBeVisible()

    const confirmBtn = dialog.getByTestId('solo-stop-confirm-confirm')
    await expect(confirmBtn).toHaveAccessibleName('Switch mode anyway')
    await confirmBtn.click()
    await expect(confirmBtn).toHaveAccessibleName('Confirm?')
    expect((await readHarnessState(page)).switchModeCalls).toBe(0)

    await confirmBtn.click()
    await expect(dialog).not.toBeVisible()
    await expect.poll(async () => (await readHarnessState(page)).switchModeCalls).toBe(1)
  })

  test('calls switch_mode directly without a dialog when solo work is idle', async ({ page }) => {
    await page.evaluate((idleWork) => {
      const h = (window as unknown as { __soloStopHarness: SoloStopHarness }).__soloStopHarness
      h.setBusyWork(idleWork)
    }, EMPTY_BUSY_WORK)

    await page.getByTestId('app-menu-trigger').click()
    await page.getByRole('menuitem', { name: 'Switch mode...' }).click()

    await expect(page.getByTestId('solo-stop-confirm-dialog')).toHaveCount(0)
    await expect.poll(async () => (await readHarnessState(page)).switchModeCalls).toBe(1)
  })

  test('covers an already-open About dialog with inert and restores it on cancel', async ({ page }) => {
    await page.getByTestId('app-menu-trigger').click()
    await page.getByRole('menuitem', { name: 'About LeapMux Desktop...' }).click()

    const aboutDialog = page.getByRole('dialog', { name: 'About' })
    await expect(aboutDialog).toBeVisible()
    await expect(aboutDialog).not.toHaveAttribute('inert')

    await emitQuitConfirm(page, 104, SAMPLE_BUSY_WORK)

    const quitDialog = page.getByTestId('solo-stop-confirm-dialog')
    await expect(quitDialog).toBeVisible()
    await expect(aboutDialog).toHaveAttribute('inert', '')
    await expect(quitDialog).not.toHaveAttribute('inert')

    await quitDialog.getByTestId('solo-stop-confirm-cancel').click()
    await expect(quitDialog).not.toBeVisible()
    await expect(aboutDialog).not.toHaveAttribute('inert')

    // Underlying About dialog is interactive again and closes normally.
    await aboutDialog.getByRole('button', { name: 'Close' }).click()
    await expect(aboutDialog).not.toBeVisible()
  })
})
