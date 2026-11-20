import type { Locator, Page } from '@playwright/test'
import type { TunnelConfig, TunnelInfo, TunnelKeyMismatch } from '../../src/api/platformBridge'
import type { StorageAccount } from '../../src/lib/browserStorage'
import { expect } from '@playwright/test'
import { decodeProxyResponse } from '~/api/platformBridge'
import { EVENT_TUNNELS_CHANGED } from '../../src/generated/contracts/desktop'
import { accountStorageKey, KEY_KEY_PINS, storageAccountFor } from '../../src/lib/browserStorage'
import { keyFingerprintHex } from '../../src/lib/fingerprint'
import { test } from './fixtures'
import { encodeDesktopProxyResponse, encodeDesktopRelayRead } from './helpers/desktopProxyMock'
import { readEntry, writeEntry } from './helpers/storage'
import { expandSidebarSection, loginViaToken, openWorkspace, sidebarSectionHeader } from './helpers/ui'

type RelayDeliveryForEncoding = {
  cursor: string
  data: number[]
} | {
  cursor: string
  close: { code: number, reason: string, wasClean: boolean }
}

// The tunnel feature requires desktop capabilities, which aren't available in
// normal browser E2E runs. These tests inject a mock desktop bridge before page
// load so the UI renders the tunnel entrypoints at initial render time.

type NativeCreate = TunnelConfig & { expectedWorkerKeysHex: string, expectedUserId: string, channelEpoch: string }

interface DesktopTunnelHarness {
  creates: NativeCreate[]
  failNextCreate: (failure: string | TunnelKeyMismatch) => void
  stopTunnel: (tunnelId: string, mismatch: TunnelKeyMismatch) => void
}

interface StoredKeyPin {
  publicKeyHex: string
  firstSeen: number
}

async function readPin(page: Page, account: StorageAccount, workerId: string): Promise<StoredKeyPin | undefined> {
  const row = await readEntry(page, accountStorageKey(account, KEY_KEY_PINS))
  return (row?.v as Record<string, StoredKeyPin> | undefined)?.[workerId]
}

async function changePin(page: Page, account: StorageAccount, workerId: string, publicKeyHex?: string): Promise<void> {
  const key = accountStorageKey(account, KEY_KEY_PINS)
  const row = await readEntry(page, key)
  if (!row)
    throw new Error('The browser pin store is empty')
  const pins = { ...row.v as Record<string, StoredKeyPin> }
  if (publicKeyHex) {
    pins[workerId] = {
      publicKeyHex,
      firstSeen: Date.now(),
    }
  }
  else {
    delete pins[workerId]
  }
  await writeEntry(page, key, pins, row.e)
}

function createArguments(page: Page): Promise<NativeCreate[]> {
  return page.evaluate(() => (window as unknown as { __desktopTunnels: DesktopTunnelHarness }).__desktopTunnels.creates)
}

/**
 * Add a page.addInitScript that sets up a mock Tauri desktop environment.
 * The bridge answers the tunnel methods, `proxy_http` for hub RPC, and the
 * channel and user-events relays, because under Tauri the page routes hub
 * traffic through the bridge.
 */
async function addTunnelMockInitScript(page: Page, expectedUserId: string) {
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
  return page.addInitScript(({ encoderBinding, relayEncoderBinding, expectedUserId, tunnelEvent }) => {
    const win = window as any
    const tunnels: Array<{ expectedUserId: string, info: TunnelInfo }> = []
    let epochNumber = 1
    let nextCreateFailure: string | TunnelKeyMismatch | undefined
    const currentEpoch = () => `epoch-${epochNumber}`
    const harness: DesktopTunnelHarness = {
      creates: [],
      failNextCreate: (failure) => { nextCreateFailure = failure },
      stopTunnel: (tunnelId, mismatch) => {
        const entry = tunnels.find(tunnel => tunnel.info.id === tunnelId)
        if (!entry)
          throw new Error(`Unknown tunnel ${tunnelId}`)
        entry.info = { ...entry.info, keyMismatch: mismatch }
        win.__TAURI_INTERNALS__.emitEvent(tunnelEvent, null)
      },
    }
    win.__desktopTunnels = harness
    let nextId = 1
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
        case 'create_tunnel': {
          const config = args.config as NativeCreate
          harness.creates.push(config)
          if (config.expectedUserId !== expectedUserId || config.channelEpoch !== currentEpoch()
            || !/^[a-f0-9]{3328}$/.test(config.expectedWorkerKeysHex)) {
            return Promise.reject(new Error('invalid tunnel admission'))
          }
          const failure = nextCreateFailure
          nextCreateFailure = undefined
          if (typeof failure === 'string')
            return Promise.reject(failure)
          if (failure)
            return Promise.resolve({ kind: 'workerKeyMismatch', keyMismatch: failure })
          const info: TunnelInfo = {
            id: `tunnel-${nextId++}`,
            workerId: config.workerId,
            type: config.type,
            bindAddr: config.bindAddr,
            bindPort: config.bindPort,
            targetAddr: config.targetAddr,
            targetPort: config.targetPort,
          }
          tunnels.push({ expectedUserId: config.expectedUserId, info })
          return Promise.resolve({ kind: 'created', info })
        }
        case 'delete_tunnel': {
          if (args.channelEpoch !== currentEpoch())
            return Promise.reject(new Error('stale tunnel epoch'))
          const idx = tunnels.findIndex(t => t.info.id === args.tunnelId)
          if (idx >= 0)
            tunnels.splice(idx, 1)
          return Promise.resolve()
        }
        case 'list_tunnels':
          if (!args.expectedUserId)
            return Promise.reject(new Error('missing tunnel account'))
          return Promise.resolve({
            tunnels: tunnels.filter(t => t.expectedUserId === args.expectedUserId).map(t => t.info),
            channelEpoch: currentEpoch(),
          })
        case 'reset_tunnels':
          if (args.channelEpoch === currentEpoch()) {
            tunnels.length = 0
            epochNumber++
          }
          return Promise.resolve()
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
        // metadata rides InvokeOptions headers, and the reply is a raw
        // ProxyHttpResponse protobuf byte array.
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
  }, { encoderBinding, relayEncoderBinding, expectedUserId, tunnelEvent: EVENT_TUNNELS_CHANGED })
}

/** Open the worker context menu in the Workers sidebar section. */
async function openWorkerMenu(page: Page) {
  const workersSection = sidebarSectionHeader(page, 'workers')
  await expandSidebarSection(workersSection)

  await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()

  const menuButton = workersSection.locator('[aria-expanded]').first()
  await menuButton.click()
  return workersSection
}

/** Open the "Add tunnel" dialog via the worker context menu. */
async function openAddTunnelDialog(page: Page) {
  const workersSection = await openWorkerMenu(page)
  await page.getByRole('menuitem', { name: 'Add tunnel...' }).click()
  const dialog = page.getByTestId('add-tunnel-dialog')
  await expect(dialog).toBeVisible()
  return { workersSection, dialog }
}

async function acceptWorkerKeyReview(prompt: Locator): Promise<void> {
  const accept = prompt.getByTestId('key-pin-accept')
  await expect(accept).toHaveAccessibleName('Accept')
  await accept.click()
  await expect(accept).toHaveAccessibleName('Confirm?')
  await accept.click()
  await expect(prompt).not.toBeVisible()
}

test.describe('Tunnel UI', () => {
  // Inject the desktop capability mock before navigation so the tunnel UI is
  // available on first render.
  test.beforeEach(async ({ page, workspace, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await addTunnelMockInitScript(page, leapmuxServer.adminUserId)
    await openWorkspace(page, workspace.workspaceId)
  })

  test('add tunnel menu item appears when tunnel API is available', async ({ page }) => {
    await openWorkerMenu(page)
    await expect(page.getByRole('menuitem', { name: 'Add tunnel...' })).toBeVisible()
  })

  test('add tunnel dialog opens and has correct fields', async ({ page }) => {
    const { dialog } = await openAddTunnelDialog(page)

    await expect(dialog.getByTestId('target-addr')).toBeVisible()
    await expect(dialog.getByTestId('target-port')).toBeVisible()
    await expect(dialog.getByTestId('bind-addr')).toBeVisible()
    await expect(dialog.getByTestId('bind-port')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-create')).toBeVisible()
    await expect(dialog.getByTestId('tunnel-cancel')).toBeVisible()
  })

  test('add tunnel dialog validates required fields', async ({ page }) => {
    const { dialog } = await openAddTunnelDialog(page)

    await expect(dialog.getByTestId('tunnel-create')).toBeDisabled()
    await dialog.getByTestId('target-port').fill('3000')
    await expect(dialog.getByTestId('tunnel-create')).toBeEnabled()
  })

  test('proves first contact and submits the durable pin and native epoch', async ({ page, leapmuxServer }) => {
    const { adminUserId, hubUrl, workerId } = leapmuxServer
    const account = storageAccountFor(new URL(hubUrl).origin, adminUserId)
    await expect.poll(() => readPin(page, account, workerId)).toBeDefined()
    await changePin(page, account, workerId)
    expect(await readPin(page, account, workerId)).toBeUndefined()

    const { workersSection, dialog } = await openAddTunnelDialog(page)
    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    await expect(dialog).not.toBeVisible()
    await expect(workersSection.getByText(/127\.0\.0\.1:3000/)).toBeVisible()
    const trusted = await readPin(page, account, workerId)
    expect(trusted?.publicKeyHex).toMatch(/^[a-f0-9]{3328}$/)
    expect(await createArguments(page)).toEqual([
      {
        workerId,
        type: 'port_forward',
        targetAddr: '127.0.0.1',
        targetPort: 3000,
        bindAddr: '127.0.0.1',
        bindPort: 3000,
        expectedWorkerKeysHex: trusted?.publicKeyHex,
        expectedUserId: adminUserId,
        channelEpoch: 'epoch-1',
      },
    ])
  })

  test('shows an ordinary create failure without closing the form', async ({ page }) => {
    await page.evaluate(() => {
      const { __desktopTunnels } = window as unknown as { __desktopTunnels: DesktopTunnelHarness }
      __desktopTunnels.failNextCreate('bind 127.0.0.1:3000: address already in use')
    })

    const replyBytes = await page.evaluate(async () => {
      const reply: number[] = await (window as any).__TAURI_INTERNALS__.invoke(
        'proxy_http',
        new TextEncoder().encode('{}'),
        {
          headers: {
            'method': 'POST',
            'path': '/leapmux.v1.SystemService/GetSystemInfo',
            'request-id': crypto.randomUUID(),
            'headers-b64': btoa(JSON.stringify({
              'Content-Type': 'application/json',
              'Connect-Protocol-Version': '1',
            })),
          },
        },
      )
      return reply
    })
    const reply = decodeProxyResponse(Uint8Array.from(replyBytes))
    expect(reply.status).toBe(200)
    expect(reply.headers['content-type']?.[0]).toContain('application/json')
    expect(JSON.parse(new TextDecoder().decode(reply.body))).toHaveProperty('build')

    const { dialog } = await openAddTunnelDialog(page)
    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    await expect(dialog.getByTestId('tunnel-create-error')).toContainText('address already in use')
    await expect(dialog).toBeVisible()
  })

  test('shows the native fingerprints when an initial creation is refused', async ({ page }) => {
    await page.evaluate(() => {
      const { __desktopTunnels } = window as unknown as { __desktopTunnels: DesktopTunnelHarness }
      __desktopTunnels.failNextCreate({ expectedFingerprint: 'able-arch-bird-clay', presentedFingerprint: 'fern-golf-harp-iris' })
    })
    const { workersSection, dialog } = await openAddTunnelDialog(page)
    await dialog.getByTestId('target-port').fill('3000')
    await dialog.getByTestId('tunnel-create').click()

    await expect(dialog.getByTestId('tunnel-key-mismatch')).toContainText('The Hub reported a different Worker key.')
    await expect(dialog.getByTestId('tunnel-expected-fingerprint')).toHaveText('able-arch-bird-clay')
    await expect(dialog.getByTestId('tunnel-presented-fingerprint')).toHaveText('fern-golf-harp-iris')
    await expect(dialog.getByTestId('tunnel-create')).toBeDisabled()
    await expect(dialog.getByTestId('tunnel-verify-key')).toBeVisible()
    await expect(workersSection.getByText(/127\.0\.0\.1:3000/)).toHaveCount(0)
  })

  test('reviews a stopped tunnel and explicitly recreates its original config', async ({ page, leapmuxServer }) => {
    const { adminUserId, hubUrl, workerId } = leapmuxServer
    const account = storageAccountFor(new URL(hubUrl).origin, adminUserId)
    const { workersSection, dialog } = await openAddTunnelDialog(page)
    await dialog.getByTestId('target-addr').fill('internal.example')
    await dialog.getByTestId('target-port').fill('8443')
    await dialog.getByTestId('bind-addr').fill('::1')
    await dialog.getByTestId('bind-port').fill('9443')
    await dialog.getByTestId('tunnel-create').click()
    await expect(dialog).not.toBeVisible()

    const original = (await createArguments(page))[0]!
    const realFingerprint = keyFingerprintHex(original.expectedWorkerKeysHex)
    const firstAnnouncedKey = 'aa'.repeat(original.expectedWorkerKeysHex.length / 2)
    const firstMismatch = {
      expectedFingerprint: realFingerprint,
      presentedFingerprint: keyFingerprintHex(firstAnnouncedKey),
    }
    await changePin(page, account, workerId, firstAnnouncedKey)
    await page.evaluate((mismatch) => {
      const { __desktopTunnels } = window as unknown as { __desktopTunnels: DesktopTunnelHarness }
      __desktopTunnels.stopTunnel('tunnel-1', mismatch)
    }, firstMismatch)

    const stopped = workersSection.getByTestId('tunnel-stopped')
    await expect(stopped.getByTestId('tunnel-expected-fingerprint')).toHaveText(firstMismatch.expectedFingerprint)
    await expect(stopped.getByTestId('tunnel-presented-fingerprint')).toHaveText(firstMismatch.presentedFingerprint)
    const verify = stopped.getByTestId('tunnel-verify-key')
    const prompt = page.getByTestId('key-pin-mismatch-dialog')
    await verify.click()
    await expect(prompt).toBeVisible()
    await prompt.getByTestId('key-pin-reject').click()
    await expect(stopped.getByTestId('tunnel-verify-error')).toContainText('key was rejected')
    await verify.click()
    await acceptWorkerKeyReview(prompt)

    await expect(dialog).toBeVisible()
    await expect(dialog.getByTestId('target-addr')).toHaveValue('internal.example')
    await expect(dialog.getByTestId('target-port')).toHaveValue('8443')
    await expect(dialog.getByTestId('bind-addr')).toHaveValue('::1')
    await expect(dialog.getByTestId('bind-port')).toHaveValue('9443')
    expect(await createArguments(page)).toEqual([original])
    expect((await readPin(page, account, workerId))?.publicKeyHex).toBe(original.expectedWorkerKeysHex)

    const changedAfterApproval = 'bb'.repeat(original.expectedWorkerKeysHex.length / 2)
    await changePin(page, account, workerId, changedAfterApproval)
    await dialog.getByTestId('tunnel-create').click()
    await expect(dialog.getByTestId('tunnel-create-error')).toHaveText('The approved Worker key changed or was removed. Verify the Worker key again.')
    await expect(dialog.getByTestId('tunnel-create')).toBeDisabled()
    expect(await createArguments(page)).toEqual([original])
    expect((await readPin(page, account, workerId))?.publicKeyHex).toBe(changedAfterApproval)

    await dialog.getByTestId('tunnel-verify-key').click()
    await expect(prompt.getByTestId('expected-fingerprint')).toHaveText(keyFingerprintHex(changedAfterApproval))
    await acceptWorkerKeyReview(prompt)
    await expect(dialog.getByTestId('tunnel-create')).toBeEnabled()
    expect(await createArguments(page)).toEqual([original])
    expect((await readPin(page, account, workerId))?.publicKeyHex).toBe(original.expectedWorkerKeysHex)
    await dialog.getByTestId('tunnel-create').click()
    await expect(dialog).not.toBeVisible()
    expect(await createArguments(page)).toEqual([original, original])
    await expect(workersSection.getByText('::1:9443 → internal.example:8443', { exact: true })).toHaveCount(2)

    const laterAnnouncedKey = 'cc'.repeat(original.expectedWorkerKeysHex.length / 2)
    const laterMismatch = {
      expectedFingerprint: realFingerprint,
      presentedFingerprint: keyFingerprintHex(laterAnnouncedKey),
    }
    expect(laterMismatch.presentedFingerprint).not.toBe(firstMismatch.presentedFingerprint)
    await changePin(page, account, workerId, laterAnnouncedKey)
    await page.evaluate((mismatch) => {
      const { __desktopTunnels } = window as unknown as { __desktopTunnels: DesktopTunnelHarness }
      __desktopTunnels.stopTunnel('tunnel-2', mismatch)
    }, laterMismatch)
    const laterStopped = workersSection.getByTestId('tunnel-stopped').filter({ hasText: laterMismatch.presentedFingerprint })
    await expect(laterStopped.getByTestId('tunnel-expected-fingerprint')).toHaveText(realFingerprint)
    await expect(laterStopped.getByTestId('tunnel-presented-fingerprint')).toHaveText(laterMismatch.presentedFingerprint)
    await laterStopped.getByTestId('tunnel-verify-key').click()
    await expect(prompt.getByTestId('expected-fingerprint')).toHaveText(laterMismatch.presentedFingerprint)
    await expect(prompt.getByTestId('actual-fingerprint')).toHaveText(realFingerprint)
    await acceptWorkerKeyReview(prompt)
    await expect(dialog.getByTestId('tunnel-create')).toBeEnabled()
    expect(await createArguments(page)).toEqual([original, original])
  })

  test('cancel button closes dialog without creating tunnel', async ({ page }) => {
    const { workersSection, dialog } = await openAddTunnelDialog(page)

    await dialog.getByTestId('tunnel-cancel').click()
    await expect(dialog).not.toBeVisible()
    await expect(workersSection.getByText(/127\.0\.0\.1:\d+\s*\u2192/)).not.toBeVisible()
  })
})
