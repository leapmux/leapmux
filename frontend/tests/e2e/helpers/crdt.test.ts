import type { MessageInitShape } from '@bufbuild/protobuf'
import { create, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '../../../src/generated/proto/leapmux/v1/common_pb'
import { WatchUserEventSchema } from '../../../src/generated/proto/leapmux/v1/user_ops_pb'
import { frameBytes } from '../../../src/lib/channelFraming'
import { closeAllUserEventsSubscriptions, getUserEventsSubscription } from './crdt'

const hubUrl = 'http://crdt-hub.test'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

/** A `/ws/userevents` socket that the test feeds with framed events, as the hub sends them. */
class FakeUserEventsSocket {
  static opened: FakeUserEventsSocket[] = []
  binaryType = ''
  private readonly listeners = new Map<string, Array<(event: unknown) => void>>()

  constructor(readonly url: string) {
    FakeUserEventsSocket.opened.push(this)
  }

  addEventListener(type: string, listener: (event: unknown) => void): void {
    this.listeners.set(type, [
      ...(this.listeners.get(type) ?? []),
      listener,
    ])
  }

  close(): void {
    this.emit('close', {})
  }

  /** Deliver one framed event in a binary message. */
  send(event: MessageInitShape<typeof WatchUserEventSchema>): void {
    const bytes = frameBytes(toBinary(WatchUserEventSchema, create(WatchUserEventSchema, event)))
    this.emit('message', {
      data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength),
    })
  }

  private emit(type: string, event: unknown): void {
    for (const listener of this.listeners.get(type) ?? [])
      listener(event)
  }
}

describe('getUserEventsSubscription', () => {
  afterEach(async () => {
    await closeAllUserEventsSubscriptions()
    FakeUserEventsSocket.opened = []
  })

  it('reads the root node of a new workspace from the CreateWorkspace op of a broadcast batch', async () => {
    vi.stubGlobal('WebSocket', FakeUserEventsSocket)
    const opening = getUserEventsSubscription(hubUrl, 'leapmux-session=create-workspace')
    const socket = FakeUserEventsSocket.opened[0]
    if (!socket)
      throw new Error('The subscription opened no socket.')
    expect(socket.url).toBe('ws://crdt-hub.test/ws/userevents')
    socket.send({
      event: {
        case: 'initial',
        value: { currentEpoch: 3n },
      },
    })
    const subscription = await opening
    const root = subscription.awaitRootNodeId('ws-new')
    socket.send({
      event: {
        case: 'batch',
        value: {
          ops: [
            {
              body: {
                case: 'setTabRegister',
                value: {
                  tab: { tabType: TabType.AGENT, tabId: 'agent-1' },
                },
              },
            },
            {
              body: {
                case: 'createWorkspace',
                value: { workspaceId: 'ws-new', rootNodeId: 'root-new' },
              },
            },
          ],
        },
      },
    })
    await expect(root).resolves.toBe('root-new')
    expect(subscription.currentEpoch()).toBe(3n)
  })
})
