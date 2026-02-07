import type { WatchEventsRequest, WatchEventsResponse } from '~/generated/leapmux/v1/workspace_pb'
import { fromBinary, toBinary } from '@bufbuild/protobuf'
import { WatchEventsRequestSchema, WatchEventsResponseSchema } from '~/generated/leapmux/v1/workspace_pb'
import { clearToken } from './transport'

/** WebSocket close code sent by the server when the auth token is invalid. */
const WS_CLOSE_UNAUTHORIZED = 4001

export interface WatchEventsOptions {
  signal: AbortSignal
}

/**
 * Opens a WebSocket connection to the Hub's /ws/watch-events endpoint,
 * sends the auth token and WatchEventsRequest, and yields
 * WatchEventsResponse messages as they arrive.
 *
 * Protocol:
 *  1. Open WebSocket with subprotocol "leapmux.watch-events.v1"
 *  2. Send auth token as text frame
 *  3. Send WatchEventsRequest as protobuf binary frame
 *  4. Receive WatchEventsResponse protobuf binary frames
 */
export async function* watchEventsViaWebSocket(
  token: string,
  request: WatchEventsRequest,
  options: WatchEventsOptions,
): AsyncGenerator<WatchEventsResponse> {
  const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const wsUrl = `${wsProtocol}//${window.location.host}/ws/watch-events`

  const ws = new WebSocket(wsUrl, ['leapmux.watch-events.v1'])
  ws.binaryType = 'arraybuffer'

  // Promise-based queue for incoming messages.
  const queue: Array<WatchEventsResponse | Error> = []
  let resolve: (() => void) | null = null
  // Using a container so the linter recognizes async mutations.
  const state = { done: false }

  const wake = () => {
    if (resolve) {
      resolve()
      resolve = null
    }
  }

  const enqueue = (item: WatchEventsResponse | Error) => {
    queue.push(item)
    wake()
  }

  ws.onopen = () => {
    // Send auth token as text frame.
    ws.send(token)
    // Send request as protobuf binary frame.
    ws.send(toBinary(WatchEventsRequestSchema, request))
  }

  ws.onmessage = (event: MessageEvent) => {
    try {
      const data = new Uint8Array(event.data as ArrayBuffer)
      const response = fromBinary(WatchEventsResponseSchema, data)
      enqueue(response)
    }
    catch (err) {
      enqueue(err instanceof Error ? err : new Error(String(err)))
    }
  }

  ws.onerror = () => {
    enqueue(new Error('WebSocket error'))
  }

  ws.onclose = (event: CloseEvent) => {
    state.done = true
    if (event.code === WS_CLOSE_UNAUTHORIZED) {
      clearToken()
    }
    wake()
  }

  // Handle abort signal.
  const onAbort = () => {
    state.done = true
    if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
      ws.close(1000, 'aborted')
    }
    wake()
  }
  options.signal.addEventListener('abort', onAbort)

  try {
    while (!state.done || queue.length > 0) {
      if (queue.length === 0 && !state.done) {
        await new Promise<void>(r => resolve = r)
      }

      while (queue.length > 0) {
        const item = queue.shift()!
        if (item instanceof Error) {
          throw item
        }
        yield item
      }
    }
  }
  finally {
    options.signal.removeEventListener('abort', onAbort)
    if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
      ws.close(1000, 'done')
    }
  }
}
