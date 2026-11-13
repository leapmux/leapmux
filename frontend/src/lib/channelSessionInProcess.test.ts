import type { SessionEvent } from './channelSessionProtocol'
import { describe, expect, it, vi } from 'vitest'
import { EncryptionMode } from '~/generated/proto/leapmux/v1/channel_pb'
import { blankHybridHandshakeState, createTestSession } from '~/test-support/channelSessionDoubles'
import { createInProcessSessionHost } from './channelSessionInProcess'
import { yieldToEventLoop } from './workSlices'

const keys = {
  x25519PublicKey: new Uint8Array(32),
  mlkemPublicKey: new Uint8Array(1568),
  slhdsaPublicKey: new Uint8Array(64),
}

/** A host whose endpoint runs the mock handshakes, and a port spawned from it that records each event. */
function spawnHarness() {
  const pair = createTestSession()
  const host = createInProcessSessionHost({
    handshake1: () => ({
      message1: new Uint8Array(1616),
      handshakeState: blankHybridHandshakeState(),
    }),
    handshake2: async () => pair.initiator,
    idleRekeyIntervalMs: 0,
  })
  const events: SessionEvent[] = []
  const failure = vi.fn()
  const port = host.spawn()
  port.connect({
    message: event => events.push(event),
    failure,
  })
  return { host, port, events, failure, pair }
}

/** Yield to the event loop until `done` holds, for at most 100 rounds. */
async function until(done: () => boolean): Promise<void> {
  for (let round = 0; round < 100; round++) {
    if (done())
      return
    await yieldToEventLoop()
  }
  throw new Error('the condition never held')
}

/** Register the session `ch-1` through the port. */
async function openSession(h: ReturnType<typeof spawnHarness>): Promise<void> {
  h.port.postMessage({
    type: 'beginHandshake',
    requestId: 1,
    mode: EncryptionMode.POST_QUANTUM,
    keys,
  }, [])
  h.port.postMessage({
    type: 'finishHandshake',
    requestId: 2,
    handshakeId: 1,
    channelId: 'ch-1',
    payload: new Uint8Array(1),
    maxReassembledMessageSize: 1 << 20,
  }, [])
  await until(() => h.events.some(event => event.type === 'requestDone'))
}

describe('createInProcessSessionHost', () => {
  it('delivers a command to the endpoint over a real port, and the event back through connect', async () => {
    const h = spawnHarness()

    h.port.postMessage({
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    }, [])
    // Delivery is asynchronous, as from a Worker.
    expect(h.events).toEqual([])
    await until(() => h.events.length > 0)

    expect(h.events).toHaveLength(1)
    expect(h.events[0]).toMatchObject({ type: 'handshakeBegun', requestId: 1 })
    h.port.terminate()
  })

  it('detaches a transferred send plaintext on the sender', async () => {
    const h = spawnHarness()
    await openSession(h)
    const plaintext = new Uint8Array([1, 2, 3])

    h.port.postMessage({
      type: 'send',
      channelId: 'ch-1',
      correlationId: 2,
      plaintext,
      startsRequest: true,
    }, [plaintext.buffer])

    expect(plaintext.byteLength).toBe(0)
    await until(() => h.events.some(event => event.type === 'wire'))
    h.port.terminate()
  })

  it('counts a message in flight until its handler returns', async () => {
    const h = spawnHarness()
    expect(h.host.inFlight()).toBe(0)

    h.port.postMessage({
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    }, [])

    expect(h.host.inFlight()).toBeGreaterThan(0)
    await until(() => h.events.length > 0 && h.host.inFlight() === 0)
    expect(h.host.inFlight()).toBe(0)
    h.port.terminate()
  })

  // A closed port never delivers what it still held, so a count that kept those
  // messages would hold every caller that waits for an idle host forever.
  it('counts the messages of every live spawn, and drops the count of a spawn that terminates with messages in flight', async () => {
    const h = spawnHarness()
    const other = h.host.spawn()
    other.connect({
      message: () => {},
      failure: () => {},
    })
    const command = {
      type: 'beginHandshake',
      requestId: 1,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    } as const

    h.port.postMessage(command, [])
    other.postMessage(command, [])
    expect(h.host.inFlight()).toBe(2)

    h.port.terminate()

    expect(h.host.inFlight()).toBe(1)
    await until(() => h.host.inFlight() === 0)
    expect(h.events).toEqual([])
    other.terminate()
  })

  it('returns the endpoint of the latest spawn, and none before the first', async () => {
    const host = createInProcessSessionHost()
    expect(host.endpoint()).toBeUndefined()

    const first = host.spawn()
    const firstEndpoint = host.endpoint()
    const second = host.spawn()

    expect(firstEndpoint).toBeDefined()
    expect(host.endpoint()).not.toBe(firstEndpoint)
    first.terminate()
    second.terminate()
  })

  it('disposes the endpoint and closes the ports on terminate, so a later post delivers nothing', async () => {
    const h = spawnHarness()
    await openSession(h)
    const endpoint = h.host.endpoint()!
    expect(endpoint.sessionState('ch-1')).toBeDefined()
    const before = h.events.length

    h.port.terminate()

    expect(endpoint.sessionState('ch-1')).toBeUndefined()
    expect(() => h.pair.initiator.send.encrypt(new Uint8Array(1))).toThrow(/destroyed/)
    h.port.postMessage({
      type: 'beginHandshake',
      requestId: 3,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    }, [])
    expect(h.host.inFlight()).toBe(0)
    for (let round = 0; round < 5; round++)
      await yieldToEventLoop()
    expect(h.events).toHaveLength(before)
  })

  it('closes the ports on a shutdown command', async () => {
    const h = spawnHarness()
    await openSession(h)
    const before = h.events.length

    h.port.postMessage({ type: 'shutdown' }, [])
    await until(() => h.host.inFlight() === 0)
    h.port.postMessage({
      type: 'beginHandshake',
      requestId: 3,
      mode: EncryptionMode.POST_QUANTUM,
      keys,
    }, [])
    // A live port counts a post at once. A closed one counts nothing, so this
    // tells a closed port from an endpoint that only ignores the command.
    expect(h.host.inFlight()).toBe(0)
    for (let round = 0; round < 5; round++)
      await yieldToEventLoop()

    expect(h.host.inFlight()).toBe(0)
    expect(h.events).toHaveLength(before)
    expect(h.host.endpoint()!.sessionState('ch-1')).toBeUndefined()
  })

  it('reports an endpoint that throws as a worker failure', async () => {
    const h = spawnHarness()
    const endpoint = h.host.endpoint()!
    vi.spyOn(endpoint, 'handleCommand').mockImplementation(() => {
      throw new Error('endpoint bug')
    })

    h.port.postMessage({ type: 'checkRekey' }, [])
    await until(() => h.failure.mock.calls.length > 0)

    expect(h.failure).toHaveBeenCalledWith('endpoint bug')
    expect(h.host.inFlight()).toBe(0)
    h.port.terminate()
  })
})
