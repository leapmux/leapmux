import { expect } from '@playwright/test'
import { test } from './fixtures'
import { expandSidebarSection, loginViaToken, openWorkspace, sidebarSectionHeader } from './helpers/ui'

// The tunnel feature requires desktop capabilities, which aren't available in
// normal browser E2E runs. These tests inject a mock desktop bridge before page
// load so the UI renders the tunnel entrypoints at initial render time.

/**
 * Add a page.addInitScript that sets up a mock Tauri desktop environment.
 * The bridge answers the tunnel methods, `proxy_http` for hub RPC, and the
 * channel and user-events relays, because under Tauri the page routes hub
 * traffic through the bridge.
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
    interface RelaySocket {
      relayId: number
      socket: WebSocket
    }
    let userEventsRelay: RelaySocket | undefined
    let channelRelay: RelaySocket | undefined
    const intentionallyClosedSockets = new WeakSet<WebSocket>()

    const closeRelay = (relay: RelaySocket | undefined, relayId = relay?.relayId) => {
      if (!relay || relay.relayId !== relayId)
        return relay
      intentionallyClosedSockets.add(relay.socket)
      relay.socket.close()
      return undefined
    }

    const attachRelayChannel = (socket: WebSocket, channelId: number) => {
      let index = 0
      socket.binaryType = 'arraybuffer'
      socket.addEventListener('message', (event) => {
        win.__TAURI_INTERNALS__.runCallback(channelId, { index: index++, message: event.data })
      })
      socket.addEventListener('close', (event) => {
        if (!intentionallyClosedSockets.has(socket)) {
          win.__TAURI_INTERNALS__.runCallback(channelId, {
            index: index++,
            message: { code: event.code, reason: event.reason, wasClean: event.wasClean },
          })
        }
        win.__TAURI_INTERNALS__.runCallback(channelId, { index, end: true })
      })
    }

    win.__TAURI_INVOKE__ = (cmd: string, args?: any, options?: any) => {
      switch (cmd) {
        case 'create_tunnel': {
          const config = args.config
          const tunnel = {
            id: `tunnel-${nextId++}`,
            workerId: config.workerId,
            type: config.type,
            bindAddr: config.bindAddr || '127.0.0.1',
            bindPort: config.bindPort || (config.type === 'socks5'
              ? 1080
              : config.targetPort),
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
              tunnels: true,
              appControl: true,
              windowControl: true,
              systemPermissions: true,
              localSolo: false,
            },
          })
        // The raw-byte contract: the request body rides the invoke body, the
        // metadata rides InvokeOptions headers, and the reply is the raw
        // envelope `[u32 LE meta_len][meta JSON][body]` (see
        // decodeProxyResponseEnvelope in ~/api/platformBridge).
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
            const metaBytes = new TextEncoder().encode(JSON.stringify({
              status: response.status,
              headers: responseHeaders,
            }))
            const bodyBytes = new Uint8Array(await response.arrayBuffer())
            const out = new Uint8Array(4 + metaBytes.byteLength + bodyBytes.byteLength)
            new DataView(out.buffer).setUint32(0, metaBytes.byteLength, true)
            out.set(metaBytes, 4)
            out.set(bodyBytes, 4 + metaBytes.byteLength)
            return out.buffer
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
          channelRelay = { relayId: args.relayId, socket }
          attachRelayChannel(socket, args.channel.id)
          return new Promise<void>((resolve, reject) => {
            socket.addEventListener('open', () => resolve(), { once: true })
            socket.addEventListener('error', () => reject(new Error('channel relay failed')), { once: true })
          })
        }
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
          userEventsRelay = { relayId: args.relayId, socket }
          attachRelayChannel(socket, args.channel.id)
          return new Promise<void>((resolve, reject) => {
            socket.addEventListener('open', () => resolve(), { once: true })
            socket.addEventListener('error', () => reject(new Error('userevents relay failed')), { once: true })
          })
        }
        case 'close_userevents_relay':
          userEventsRelay = closeRelay(userEventsRelay, args.relayId)
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
  })
}

/** Open the worker context menu in the Workers sidebar section. */
async function openWorkerMenu(page: import('@playwright/test').Page) {
  const workersSection = sidebarSectionHeader(page, 'workers')
  await expandSidebarSection(workersSection)

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
    await openWorkspace(page, workspace.workspaceId)
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
    // Only CreateTunnel fails. Forward the invoke options alongside the raw
    // arguments so the proxy still receives the Hub method and headers.
    await page.evaluate(() => {
      const win = window as any
      const previousInvoke = win.__TAURI_INVOKE__
      win.__TAURI_INVOKE__ = (cmd: string, args?: any, options?: any) => {
        if (cmd === 'create_tunnel')
          return Promise.reject(new Error('bind 127.0.0.1:3000: address already in use'))
        return previousInvoke(cmd, args, options)
      }
    })

    const reply = await page.evaluate(async () => {
      const reply: ArrayBuffer = await (window as any).__TAURI_INTERNALS__.invoke(
        'proxy_http',
        new TextEncoder().encode('{}'),
        {
          headers: {
            'method': 'POST',
            'path': '/leapmux.v1.SystemService/GetSystemInfo',
            'headers-b64': btoa(JSON.stringify({
              'Content-Type': 'application/json',
              'Connect-Protocol-Version': '1',
            })),
          },
        },
      )
      const metadataEnd = 4 + new DataView(reply).getUint32(0, true)
      const decoder = new TextDecoder()
      const metadata = JSON.parse(decoder.decode(reply.slice(4, metadataEnd)))
      return {
        status: metadata.status,
        contentType: metadata.headers['content-type']?.[0],
        body: decoder.decode(reply.slice(metadataEnd)),
      }
    })
    expect(reply.status).toBe(200)
    expect(reply.contentType).toContain('application/json')
    expect(JSON.parse(reply.body)).toHaveProperty('build')

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
