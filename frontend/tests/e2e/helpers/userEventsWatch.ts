import type { Page } from '@playwright/test'
import type { Buffer } from 'node:buffer'
import type { WatchUserEvent } from '../../../src/generated/proto/leapmux/v1/user_ops_pb'
import { fromBinary } from '@bufbuild/protobuf'
import { expect } from '@playwright/test'
import { WS_USER_EVENTS_ROUTE } from '../../../src/generated/contracts/wire'
import { WatchUserEventSchema } from '../../../src/generated/proto/leapmux/v1/user_ops_pb'
import { unframeBytes } from '../../../src/lib/channelFraming'
import { retryUntilPass } from './retryUntilPass'

/**
 * What one page received on its `/ws/userevents` stream: the identity that the hub gave the page, and the active
 * client of each workspace in the presence updates.
 *
 * The app keeps no trace of either in the DOM. The active-client gate of the turn-end sound reads both, so a test of
 * that gate waits on this watch: the page received the update, and the app applies it in the same task.
 */
export interface UserEventsWatch {
  /** The identity that the hub uses for this page in its presence updates, or '' before the first frame. */
  subscriberClientId: () => string
  /** The active client of `workspaceId` that the newest presence update named, or '' when none named one. */
  activeClient: (workspaceId: string) => string
}

/** The state of a {@link UserEventsWatch}. One document of the page holds one state, as the app holds one store. */
export interface UserEventsState {
  subscriberClientId: string
  activeClients: Map<string, string>
}

/**
 * Decode one frame of the `/ws/userevents` stream: a 4-byte big-endian length, then one `WatchUserEvent`.
 * Return null for a text frame or a frame that is not a whole event, as the app drops such a frame.
 */
export function decodeUserEventsFrame(payload: string | Buffer): WatchUserEvent | null {
  if (typeof payload === 'string')
    return null
  const framed = unframeBytes(new Uint8Array(payload.buffer, payload.byteOffset, payload.byteLength))
  if (!framed.ok)
    return null
  try {
    return fromBinary(WatchUserEventSchema, framed.payload)
  }
  catch {
    return null
  }
}

/** Apply one event to `state`, as the app applies it to its own stores. */
export function applyUserEvent(state: UserEventsState, event: WatchUserEvent): void {
  switch (event.event.case) {
    case 'initial':
    case 'delta':
      if (event.event.value.subscriberClientId)
        state.subscriberClientId = event.event.value.subscriberClientId
      return
    case 'presence': {
      const { workspaceId, activeClientId } = event.event.value
      if (activeClientId === '')
        state.activeClients.delete(workspaceId)
      else
        state.activeClients.set(workspaceId, activeClientId)
    }
  }
}

/**
 * Watch the `/ws/userevents` stream of `page` from now on. Call it before the page opens the app, because the stream
 * states the identity of the page only in its first frame. A new document starts a new state, as the app starts with
 * empty stores after a reload.
 */
export function watchUserEvents(page: Page): UserEventsWatch {
  let state: UserEventsState = { subscriberClientId: '', activeClients: new Map() }
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame())
      state = { subscriberClientId: '', activeClients: new Map() }
  })
  page.on('websocket', (socket) => {
    if (new URL(socket.url()).pathname !== WS_USER_EVENTS_ROUTE)
      return
    socket.on('framereceived', ({ payload }) => {
      const event = decodeUserEventsFrame(payload)
      if (event)
        applyUserEvent(state, event)
    })
  })
  return {
    subscriberClientId: () => state.subscriberClientId,
    activeClient: workspaceId => state.activeClients.get(workspaceId) ?? '',
  }
}

/** Wait until the hub gave `watch` an identity, and return it. */
export async function waitForSubscriberClientId(watch: UserEventsWatch): Promise<string> {
  return retryUntilPass(() => {
    const id = watch.subscriberClientId()
    expect(id, 'the hub names the identity of the page in the first frame of its stream').not.toBe('')
    return id
  })
}

/** Wait until every watch in `watches` received a presence update that names `clientId` the active client of `workspaceId`. */
export async function waitForActiveClient(watches: readonly UserEventsWatch[], workspaceId: string, clientId: string): Promise<void> {
  if (clientId === '')
    throw new Error('An active-client wait needs a client ID. An empty ID means that no client leads.')
  await retryUntilPass(() => {
    for (const [index, watch] of watches.entries())
      expect(watch.activeClient(workspaceId), `client ${index + 1} received the presence update`).toBe(clientId)
  })
}
