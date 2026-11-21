import type { Page } from '@playwright/test'
import type { SoloBusyWork } from '../../src/api/platformBridge'
import { expect } from '@playwright/test'
import { TAURI_EVENT_APP_CONFIRM_QUIT } from '../../src/generated/contracts/desktop'
import { test } from './fixtures'
import { encodeDesktopProxyResponse, encodeDesktopRelayRead } from './helpers/desktopProxyMock'
import { loginViaToken, openWorkspace } from './helpers/ui'

type RelayDeliveryForEncoding = {
  cursor: string
  data: number[]
} | {
  cursor: string
  close: { code: number, reason: string, wasClean: boolean }
}

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
  const encoderBinding = `__leapmuxEncodeProxyResponse${crypto.randomUUID().replaceAll('-', '')}`
  const relayEncoderBinding = `__leapmuxEncodeRelayRead${crypto.randomUUID().replaceAll('-', '')}`
  await page.exposeFunction(encoderBinding, (response: {
    status: number
    headers: Record<string, string[]>
    body: number[]
  }) => Array.from(new Uint8Array(encodeDesktopProxyResponse({
    ...response,
    body: Uint8Array.from(response.body),
  }))))
  await page.exposeFunction(relayEncoderBinding, (delivery: RelayDeliveryForEncoding) => Array.from(new Uint8Array(encodeDesktopRelayRead(
    BigInt(delivery.cursor),
    'data' in delivery
      ? {
          data: Uint8Array.from(delivery.data),
        }
      : { close: delivery.close },
  ))))

  await page.addInitScript(({ encoderBinding, relayEncoderBinding, quitEvent, initialBusyWork }) => {
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

    interface RelaySocket {
      relayId: number
      socket: WebSocket
      nextCursor: bigint
      deliveries: Array<{
        cursor: bigint
        message: ArrayBuffer | { code: number, reason: string, wasClean: boolean }
      }>
      waiters: Set<() => void>
    }
    let userEventsRelay: RelaySocket | undefined
    let channelRelay: RelaySocket | undefined
    const intentionallyClosedSockets = new WeakSet<WebSocket>()

    const closeRelay = (relay: RelaySocket | undefined, relayId = relay?.relayId) => {
      if (!relay || relay.relayId !== relayId)
        return relay
      intentionallyClosedSockets.add(relay.socket)
      relay.socket.close()
      for (const waiter of relay.waiters)
        waiter()
      relay.waiters.clear()
      return undefined
    }

    const pushRelayMessage = (
      relay: RelaySocket,
      message: ArrayBuffer | { code: number, reason: string, wasClean: boolean },
    ) => {
      relay.deliveries.push({ cursor: relay.nextCursor++, message })
      for (const waiter of relay.waiters)
        waiter()
      relay.waiters.clear()
    }

    const attachRelaySocket = (relay: RelaySocket) => {
      relay.socket.binaryType = 'arraybuffer'
      relay.socket.addEventListener('message', (event) => {
        pushRelayMessage(relay, event.data)
      })
      relay.socket.addEventListener('close', (event) => {
        if (!intentionallyClosedSockets.has(relay.socket)) {
          pushRelayMessage(relay, {
            code: event.code,
            reason: event.reason,
            wasClean: event.wasClean,
          })
        }
      })
    }

    const readRelay = async (
      current: () => RelaySocket | undefined,
      relayId: number,
      afterCursor: string,
    ): Promise<number[]> => {
      const acknowledged = BigInt(afterCursor)
      while (true) {
        const relay = current()
        if (!relay || relay.relayId !== relayId)
          throw new Error('relay receiver is closed or superseded')
        while (true) {
          const first = relay.deliveries[0]
          if (!first || first.cursor > acknowledged)
            break
          relay.deliveries.shift()
        }
        const delivery = relay.deliveries[0]
        if (delivery) {
          return win[relayEncoderBinding](delivery.message instanceof ArrayBuffer
            ? {
                cursor: delivery.cursor.toString(),
                data: Array.from(new Uint8Array(delivery.message)),
              }
            : {
                cursor: delivery.cursor.toString(),
                close: delivery.message,
              })
        }
        await new Promise<void>(resolve => relay.waiters.add(resolve))
      }
    }

    win.__TAURI_INVOKE__ = (cmd: string, args?: any, options?: any) => {
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
              hubUrl: '',
              recentHubUrls: [],
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
        case 'inspect_solo_busy_work':
          return Promise.resolve(harness.busyWork)
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
        case 'proxy_http': {
          const method = options?.headers?.method ?? 'POST'
          const path = options?.headers?.path ?? '/'
          const headers = JSON.parse(atob(options?.headers?.['headers-b64'] ?? '') || '{}')
          const body: Uint8Array<ArrayBuffer> | null = args instanceof Uint8Array
            ? args as Uint8Array<ArrayBuffer>
            : args instanceof ArrayBuffer
              ? new Uint8Array(args)
              : null
          return fetch(window.location.origin + path, {
            method,
            headers,
            body: body && body.byteLength > 0
              ? body
              : null,
          }).then(async (response) => {
            const responseHeaders: Record<string, string[]> = {}
            response.headers.forEach((value, name) => {
              (responseHeaders[name] ??= []).push(value)
            })
            return win[encoderBinding]({
              status: response.status,
              headers: responseHeaders,
              body: Array.from(new Uint8Array(await response.arrayBuffer())),
            })
          })
        }
        case 'open_channel_relay': {
          closeRelay(channelRelay)
          const protocol = window.location.protocol === 'https:'
            ? 'wss:'
            : 'ws:'
          const socket = new WebSocket(
            `${protocol}//${window.location.host}/ws/channel`,
            ['channel-relay'],
          )
          channelRelay = {
            relayId: args.relayId,
            socket,
            nextCursor: 1n,
            deliveries: [],
            waiters: new Set(),
          }
          attachRelaySocket(channelRelay)
          return new Promise<void>((resolve, reject) => {
            socket.addEventListener('open', () => resolve(), { once: true })
            socket.addEventListener('error', () => reject(new Error('channel relay failed')), { once: true })
          })
        }
        case 'read_channel_relay':
          return readRelay(() => channelRelay, args.relayId, args.afterCursor)
        case 'send_channel_message':
          channelRelay?.socket.send(args instanceof Uint8Array
            ? args
            : new Uint8Array(args))
          return Promise.resolve()
        case 'close_channel_relay':
          channelRelay = closeRelay(channelRelay, args.relayId)
          return Promise.resolve()
        case 'open_userevents_relay': {
          closeRelay(userEventsRelay)
          const params = new URLSearchParams()
          for (const workspaceId of args.workspaceIds ?? [])
            params.append('workspace_ids', workspaceId)
          const protocol = window.location.protocol === 'https:'
            ? 'wss:'
            : 'ws:'
          const qs = params.toString()
          const socket = new WebSocket(
            `${protocol}//${window.location.host}/ws/userevents${qs
              ? `?${qs}`
              : ''}`,
            ['userevents-relay'],
          )
          userEventsRelay = {
            relayId: args.relayId,
            socket,
            nextCursor: 1n,
            deliveries: [],
            waiters: new Set(),
          }
          attachRelaySocket(userEventsRelay)
          return new Promise<void>((resolve, reject) => {
            socket.addEventListener('open', () => resolve(), { once: true })
            socket.addEventListener('error', () => reject(new Error('userevents relay failed')), { once: true })
          })
        }
        case 'read_userevents_relay':
          return readRelay(() => userEventsRelay, args.relayId, args.afterCursor)
        case 'close_userevents_relay':
          userEventsRelay = closeRelay(userEventsRelay, args.relayId)
          return Promise.resolve()
        default:
          return Promise.reject(new Error(`unhandled Tauri invoke: ${cmd}`))
      }
    }

    const callbacks = new Map<number, (payload: any) => unknown>()
    const eventListeners = new Map<string, Set<number>>()
    let nextCallbackId = 1
    const unregisterCallback = (id: number) => callbacks.delete(id)
    const unregisterListener = (event: string, id: number) => {
      eventListeners.get(event)?.delete(id)
      unregisterCallback(id)
    }
    const emitEvent = (event: string, payload: unknown) => {
      for (const id of eventListeners.get(event) ?? [])
        callbacks.get(id)?.({ event, id, payload })
    }

    win.__TAURI_INTERNALS__ = {
      callbacks,
      metadata: {
        currentWindow: { label: 'main' },
        currentWebview: { label: 'main', windowLabel: 'main' },
      },
      transformCallback: (callback: (payload: any) => unknown) => {
        const id = nextCallbackId++
        callbacks.set(id, callback)
        return id
      },
      unregisterCallback,
      runCallback: (id: number, payload: any) => callbacks.get(id)?.(payload),
      emitEvent,
      invoke: (cmd: string, args: any = {}, options?: any) => {
        if (cmd === 'plugin:event|listen') {
          const listeners = eventListeners.get(args.event) ?? new Set<number>()
          listeners.add(args.handler)
          eventListeners.set(args.event, listeners)
          return Promise.resolve(args.handler)
        }
        if (cmd === 'plugin:event|unlisten') {
          unregisterListener(args.event, args.eventId)
          return Promise.resolve()
        }
        if (cmd === 'plugin:event|emit') {
          emitEvent(args.event, args.payload)
          return Promise.resolve()
        }
        if (cmd === 'plugin:window|is_fullscreen' || cmd === 'plugin:window|is_maximized')
          return Promise.resolve(false)
        if (cmd.startsWith('plugin:window|'))
          return Promise.resolve()
        return win.__TAURI_INVOKE__(cmd, args, options)
      },
    }
    win.__TAURI_EVENT_PLUGIN_INTERNALS__ = { unregisterListener }
  }, {
    encoderBinding,
    relayEncoderBinding,
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

test.describe('Desktop Solo stop confirmation', () => {
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
