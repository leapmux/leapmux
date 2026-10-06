import type { MessageInitShape } from '@bufbuild/protobuf'
import type { Page } from '@playwright/test'
import type { UserEventsState, UserEventsWatch } from './userEventsWatch'
import { Buffer } from 'node:buffer'
import { EventEmitter } from 'node:events'
import { create, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { WS_USER_EVENTS_ROUTE } from '../../../src/generated/contracts/wire'
import { WatchUserEventSchema } from '../../../src/generated/proto/leapmux/v1/user_ops_pb'
import { frameBytes } from '../../../src/lib/channelFraming'
import { startWaitLimitForTests } from './testDeadline'
import { applyUserEvent, decodeUserEventsFrame, waitForActiveClient, waitForSubscriberClientId, watchUserEvents } from './userEventsWatch'

/** One framed `WatchUserEvent`, as the hub sends it. */
function frame(event: MessageInitShape<typeof WatchUserEventSchema>): Buffer {
  return Buffer.from(frameBytes(toBinary(WatchUserEventSchema, create(WatchUserEventSchema, event))))
}

const INITIAL = { event: { case: 'initial', value: { subscriberClientId: 'session:a' } } } as const
const DELTA = { event: { case: 'delta', value: { subscriberClientId: 'session:b' } } } as const
function presence(workspaceId: string, activeClientId: string) {
  return { event: { case: 'presence', value: { workspaceId, activeClientId } } } as const
}

function emptyState(): UserEventsState {
  return { subscriberClientId: '', activeClients: new Map() }
}

let endWaitLimit: (() => void) | undefined
afterEach(() => {
  endWaitLimit?.()
  endWaitLimit = undefined
})

describe('decodeUserEventsFrame', () => {
  it('decodes one framed event', () => {
    expect(decodeUserEventsFrame(frame(presence('ws-1', 'session:a')))?.event).toEqual({ case: 'presence', value: expect.objectContaining({ workspaceId: 'ws-1', activeClientId: 'session:a' }) })
  })

  it('decodes a frame that is a view into a larger buffer', () => {
    const framed = frame(INITIAL)
    const larger = Buffer.concat([Buffer.from([9, 9, 9]), framed])
    expect(decodeUserEventsFrame(larger.subarray(3))?.event.case).toBe('initial')
  })

  it.each([
    ['a text frame', 'hello'],
    ['a frame shorter than its length prefix', Buffer.from([0, 0])],
    ['a frame whose length prefix states another length', Buffer.from([0, 0, 0, 9, 1])],
    ['a frame that holds no event', Buffer.from([0, 0, 0, 2, 0xFF, 0xFF])],
  ])('drops %s', (_name, payload) => {
    expect(decodeUserEventsFrame(payload)).toBeNull()
  })
})

describe('applyUserEvent', () => {
  it('takes the identity from the first frame of a fresh or a resumed stream', () => {
    const state = emptyState()
    applyUserEvent(state, create(WatchUserEventSchema, INITIAL))
    expect(state.subscriberClientId).toBe('session:a')
    applyUserEvent(state, create(WatchUserEventSchema, DELTA))
    expect(state.subscriberClientId).toBe('session:b')
  })

  it('keeps the identity when a first frame names none', () => {
    const state = emptyState()
    applyUserEvent(state, create(WatchUserEventSchema, INITIAL))
    applyUserEvent(state, create(WatchUserEventSchema, { event: { case: 'initial', value: {} } }))
    expect(state.subscriberClientId).toBe('session:a')
  })

  it('keeps the newest active client of each workspace, and forgets a workspace that no client leads', () => {
    const state = emptyState()
    applyUserEvent(state, create(WatchUserEventSchema, presence('ws-1', 'session:a')))
    applyUserEvent(state, create(WatchUserEventSchema, presence('ws-2', 'session:b')))
    applyUserEvent(state, create(WatchUserEventSchema, presence('ws-1', 'session:b')))
    expect(state.activeClients).toEqual(new Map([['ws-1', 'session:b'], ['ws-2', 'session:b']]))
    applyUserEvent(state, create(WatchUserEventSchema, presence('ws-1', '')))
    expect(state.activeClients).toEqual(new Map([['ws-2', 'session:b']]))
  })

  it('ignores the other events', () => {
    const state = emptyState()
    applyUserEvent(state, create(WatchUserEventSchema, { event: { case: 'renamed', value: { workspaceId: 'ws-1' } } }))
    expect(state).toEqual(emptyState())
  })
})

describe('watchUserEvents', () => {
  /** A page that emits its own `websocket` and `framenavigated` events. */
  function fakePage() {
    const emitter = new EventEmitter()
    const mainFrame = {}
    const page = Object.assign(emitter, { mainFrame: () => mainFrame }) as unknown as Page
    const openSocket = (path: string) => {
      const socket = Object.assign(new EventEmitter(), { url: () => `ws://hub.test${path}?workspace_ids=ws-1` })
      emitter.emit('websocket', socket)
      return (payload: string | Buffer) => socket.emit('framereceived', { payload })
    }
    return { page, mainFrame, emitter, openSocket }
  }

  it('reads the identity and the presence from the user events stream only', () => {
    const { page, openSocket } = fakePage()
    const watch = watchUserEvents(page)
    const channel = openSocket('/ws/channel')
    channel(frame(INITIAL))
    expect(watch.subscriberClientId()).toBe('')
    const events = openSocket(WS_USER_EVENTS_ROUTE)
    events(frame(INITIAL))
    events('a text frame')
    events(frame(presence('ws-1', 'session:a')))
    expect(watch.subscriberClientId()).toBe('session:a')
    expect(watch.activeClient('ws-1')).toBe('session:a')
    expect(watch.activeClient('ws-2')).toBe('')
  })

  it('starts a new state for a new document, as the app does after a reload', () => {
    const { page, mainFrame, emitter, openSocket } = fakePage()
    const watch = watchUserEvents(page)
    const events = openSocket(WS_USER_EVENTS_ROUTE)
    events(frame(INITIAL))
    events(frame(presence('ws-1', 'session:a')))
    emitter.emit('framenavigated', {})
    expect(watch.activeClient('ws-1'), 'a child frame keeps the state').toBe('session:a')
    emitter.emit('framenavigated', mainFrame)
    expect(watch.subscriberClientId()).toBe('')
    expect(watch.activeClient('ws-1')).toBe('')
  })
})

describe('waitForActiveClient', () => {
  function watching(active: string): UserEventsWatch {
    return { subscriberClientId: () => 'session:a', activeClient: () => active }
  }

  it('returns once every watch received the update', async () => {
    await expect(waitForActiveClient([watching('session:a'), watching('session:a')], 'ws-1', 'session:a')).resolves.toBeUndefined()
  })

  it('names the client that did not receive the update', async () => {
    endWaitLimit = startWaitLimitForTests(300)
    await expect(waitForActiveClient([watching('session:a'), watching('')], 'ws-1', 'session:a')).rejects.toThrow('client 2 received the presence update')
  })

  it('refuses an empty client ID before it reads a watch', async () => {
    const activeClient = vi.fn(() => 'session:a')
    await expect(waitForActiveClient([{ subscriberClientId: () => '', activeClient }], 'ws-1', '')).rejects.toThrow('needs a client ID')
    expect(activeClient).not.toHaveBeenCalled()
  })
})

describe('waitForSubscriberClientId', () => {
  it('returns the identity that the hub gave the page', async () => {
    await expect(waitForSubscriberClientId({ subscriberClientId: () => 'session:a', activeClient: () => '' })).resolves.toBe('session:a')
  })

  it('fails when no first frame arrives', async () => {
    endWaitLimit = startWaitLimitForTests(300)
    await expect(waitForSubscriberClientId({ subscriberClientId: () => '', activeClient: () => '' })).rejects.toThrow('the hub names the identity of the page')
  })
})
