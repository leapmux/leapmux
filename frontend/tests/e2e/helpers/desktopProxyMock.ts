import type { Page } from '@playwright/test'
import { create, toBinary } from '@bufbuild/protobuf'
import { HeaderValuesSchema, ProxyHttpResponseSchema, RelayCloseSchema, RelayReadResponseSchema } from '../../../src/generated/proto/leapmux/desktop/v1/frame_pb'

type RelayDeliveryForEncoding = {
  cursor: string
  data: number[]
} | {
  cursor: string
  close: { code: number, reason: string, wasClean: boolean }
}

export function encodeDesktopProxyResponse(response: {
  status: number
  headers: Record<string, string[]>
  body: ArrayBuffer | Uint8Array
}): ArrayBuffer {
  const bytes = toBinary(ProxyHttpResponseSchema, create(ProxyHttpResponseSchema, {
    status: response.status,
    headers: Object.fromEntries(
      Object.entries(response.headers).map(([name, values]) => [
        name,
        create(HeaderValuesSchema, { values }),
      ]),
    ),
    body: response.body instanceof Uint8Array
      ? response.body
      : new Uint8Array(response.body),
  }))
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}

export function encodeDesktopRelayRead(
  cursor: bigint,
  item: { data: Uint8Array } | { close: { code: number, reason: string, wasClean: boolean } },
): ArrayBuffer {
  const value = 'data' in item
    ? { case: 'data' as const, value: item.data }
    : {
        case: 'close' as const,
        value: create(RelayCloseSchema, item.close),
      }
  const bytes = toBinary(RelayReadResponseSchema, create(RelayReadResponseSchema, {
    cursor,
    item: value,
  }))
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}

/**
 * Installs the shared Tauri v2 desktop bridge mock (`__TAURI_INTERNALS__`,
 * `__TAURI_EVENT_PLUGIN_INTERNALS__`, `proxy_http`, and the channel and
 * user-events WebSocket relays) before page load.
 *
 * Spec-specific Tauri commands delegate to `window.__TAURI_INVOKE__`, which
 * each spec registers in its own init script.
 */
export async function installDesktopBridgeMock(page: Page): Promise<void> {
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

  await page.addInitScript(({ encoderBinding, relayEncoderBinding }) => {
    const win = window as any

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

    const handleSharedCommand = (cmd: string, args: any, options?: any): Promise<unknown> | null => {
      switch (cmd) {
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
          return null
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
        const shared = handleSharedCommand(cmd, args, options)
        if (shared !== null)
          return shared
        if (typeof win.__TAURI_INVOKE__ === 'function')
          return win.__TAURI_INVOKE__(cmd, args, options)
        return Promise.reject(new Error(`unhandled Tauri invoke: ${cmd}`))
      },
    }
    win.__TAURI_EVENT_PLUGIN_INTERNALS__ = { unregisterListener }
  }, {
    encoderBinding,
    relayEncoderBinding,
  })
}
