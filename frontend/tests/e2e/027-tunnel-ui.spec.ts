import { expect, test } from './fixtures'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

// The tunnel feature requires desktop capabilities, which aren't available in
// normal browser E2E runs. These tests inject a mock desktop bridge before page
// load so the UI renders the tunnel entrypoints at initial render time.

/**
 * Add a page.addInitScript that sets up a mock Tauri desktop environment with
 * tunnel methods only. Hub transport remains browser-native in the test page.
 */
function addTunnelMockInitScript(page: import('@playwright/test').Page) {
  return page.addInitScript(() => {
    const win = window as any
    const tunnels: Array<{
      id: string
      workerId: string
      type: string
      bindAddr: string
      bindPort: number
      targetAddr: string
      targetPort: number
    }> = []
    let nextId = 1
    let orgEventsSocket: WebSocket | undefined
    const intentionallyClosedSockets = new WeakSet<WebSocket>()

    const closeOrgEventsSocket = () => {
      if (!orgEventsSocket)
        return
      intentionallyClosedSockets.add(orgEventsSocket)
      orgEventsSocket.close()
      orgEventsSocket = undefined
    }

    const bytesToBase64 = (data: ArrayBuffer) => {
      const bytes = new Uint8Array(data)
      let binary = ''
      for (const byte of bytes)
        binary += String.fromCharCode(byte)
      return btoa(binary)
    }

    win.__TAURI_INVOKE__ = (cmd: string, args?: any) => {
      switch (cmd) {
        case 'create_tunnel': {
          const config = args.config
          const tunnel = {
            id: `tunnel-${nextId++}`,
            workerId: config.workerId,
            type: config.type,
            bindAddr: config.bindAddr || '127.0.0.1',
            bindPort: config.bindPort || (config.type === 'socks5' ? 1080 : config.targetPort),
            targetAddr: config.targetAddr || '',
            targetPort: config.targetPort || 0,
          }
          tunnels.push(tunnel)
          return Promise.resolve(tunnel)
        }
        case 'delete_tunnel': {
          const idx = tunnels.findIndex(t => t.id === args.tunnelId)
          if (idx >= 0)
            tunnels.splice(idx, 1)
          return Promise.resolve()
        }
        case 'list_tunnels':
          return Promise.resolve([...tunnels])
        case 'get_runtime_state':
          return Promise.resolve({
            shellMode: 'distributed',
            connected: true,
            hubUrl: window.location.origin,
            capabilities: {
              mode: 'tauri-desktop-distributed',
              hubTransport: 'direct',
              tunnels: true,
              appControl: true,
              windowControl: true,
              systemPermissions: true,
              localSolo: false,
            },
          })
        case 'open_orgevents_relay': {
          closeOrgEventsSocket()
          const params = new URLSearchParams({ org_id: args.orgId })
          for (const workspaceId of args.workspaceIds ?? [])
            params.append('workspace_ids', workspaceId)
          const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
          const socket = new WebSocket(
            `${protocol}//${window.location.host}/ws/orgevents?${params}`,
            ['orgevents-relay'],
          )
          orgEventsSocket = socket
          socket.binaryType = 'arraybuffer'
          socket.addEventListener('message', (event) => {
            win.__TAURI_INTERNALS__.emitEvent('orgevents:message', bytesToBase64(event.data))
          })
          socket.addEventListener('close', () => {
            if (!intentionallyClosedSockets.has(socket))
              win.__TAURI_INTERNALS__.emitEvent('orgevents:close', undefined)
          })
          return new Promise<void>((resolve, reject) => {
            socket.addEventListener('open', () => resolve(), { once: true })
            socket.addEventListener('error', () => reject(new Error('org-events relay failed')), { once: true })
          })
        }
        case 'close_orgevents_relay':
          closeOrgEventsSocket()
          return Promise.resolve()
        default:
          return Promise.reject(new Error(`unhandled Tauri invoke: ${cmd}`))
      }
    }

    // Tauri v2 routes all APIs through __TAURI_INTERNALS__. The callback and
    // window metadata are required before app.tsx registers native listeners
    // and restores window geometry; mocking only the legacy
    // __TAURI_INVOKE__ hook leaves the app on the launcher screen.
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
      invoke: (cmd: string, args: any = {}) => {
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
        return win.__TAURI_INVOKE__(cmd, args)
      },
    }
    win.__TAURI_EVENT_PLUGIN_INTERNALS__ = { unregisterListener }
  })
}

/** Open the worker context menu in the Workers sidebar section. */
async function openWorkerMenu(page: import('@playwright/test').Page) {
  const workersSection = page.getByTestId('section-header-workers')
  await expect(workersSection).toBeVisible()
  const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
  if (!isOpen)
    await workersSection.locator('> [role="button"]').click()

  await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()

  const menuButton = workersSection.locator('[aria-expanded]').first()
  await menuButton.click()
  return workersSection
}

/** Open the "Add tunnel" dialog via the worker context menu. */
async function openAddTunnelDialog(page: import('@playwright/test').Page) {
  const workersSection = await openWorkerMenu(page)
  await page.getByRole('menuitem', { name: 'Add tunnel...' }).click()
  const dialog = page.getByTestId('add-tunnel-dialog')
  await expect(dialog).toBeVisible()
  return { workersSection, dialog }
}

test.describe('Tunnel UI', () => {
  // Inject the desktop capability mock before navigation so the tunnel UI is
  // available on first render.
  test.beforeEach(async ({ page, workspace, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await addTunnelMockInitScript(page)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)
  })

  test('add tunnel menu item appears when tunnel API is available', async ({ page, workspace }) => {
    await openWorkerMenu(page)
    await expect(page.getByRole('menuitem', { name: 'Add tunnel...' })).toBeVisible()
  })

  test('add tunnel dialog opens and has correct fields', async ({ page, workspace }) => {
    const { dialog } = await openAddTunnelDialog(page)

    await expect(dialog.getByTestId('target-addr')).toBeVisible()
    await expect(dialog.getByTestId('target-port')).toBeVisible()
    await expect(dialog.getByTestId('bind-addr')).toBeVisible()
    await expect(dialog.getByTestId('bind-port')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-create')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-cancel')).toBeVisible()
  })

  test('add tunnel dialog validates required fields', async ({ page, workspace }) => {
    const { dialog } = await openAddTunnelDialog(page)

    await expect(dialog.getByTestId('tunnel-create')).toBeDisabled()
    await dialog.getByTestId('target-port').fill('3000')
    await expect(dialog.getByTestId('tunnel-create')).toBeEnabled()
  })

  test('tunnel creation adds tunnel to sidebar', async ({ page, workspace }) => {
    const { workersSection, dialog } = await openAddTunnelDialog(page)

    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    await expect(dialog).not.toBeVisible()
    await expect(workersSection.getByText(/127\.0\.0\.1:3000/)).toBeVisible()
  })

  test('tunnel creation error is displayed in dialog', async ({ page, workspace }) => {
    // Override CreateTunnel to reject with an error.
    await page.evaluate(() => {
      ;(window as any).__TAURI_INVOKE__ = (cmd: string) => {
        if (cmd === 'create_tunnel')
          return Promise.reject(new Error('bind 127.0.0.1:3000: address already in use'))
        if (cmd === 'delete_tunnel')
          return Promise.resolve()
        if (cmd === 'list_tunnels')
          return Promise.resolve([])
        if (cmd === 'get_runtime_state') {
          return Promise.resolve({
            shellMode: 'distributed',
            connected: true,
            hubUrl: window.location.origin,
            capabilities: {
              mode: 'tauri-desktop-distributed',
              hubTransport: 'direct',
              tunnels: true,
              appControl: true,
              windowControl: true,
              systemPermissions: true,
              localSolo: false,
            },
          })
        }
        return Promise.reject(new Error(`unhandled Tauri invoke: ${cmd}`))
      }
    })

    const { dialog } = await openAddTunnelDialog(page)
    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    await expect(dialog.getByText('address already in use')).toBeVisible()
    // Dialog should remain open so the user can retry.
    await expect(dialog).toBeVisible()
  })

  test('cancel button closes dialog without creating tunnel', async ({ page, workspace }) => {
    const { workersSection, dialog } = await openAddTunnelDialog(page)

    await dialog.getByTestId('tunnel-cancel').click()
    await expect(dialog).not.toBeVisible()
    await expect(workersSection.getByText(/127\.0\.0\.1:\d+\s*\u2192/)).not.toBeVisible()
  })
})
