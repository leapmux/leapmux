import { randomUUID } from 'node:crypto'
import { BroadcastChannel as NativeBroadcastChannel } from 'node:worker_threads'
import { IDBFactory } from 'fake-indexeddb'
import { describe, expect, it, vi } from 'vitest'
import { flushStorageWrites, KEY_CHANNEL_RELAY_SEQ, localStorageGet, localStorageSet, onStorageChanged, PREFIX_FILES_SHOW_HIDDEN, resetBrowserStorageForTests, setStorageAccountForTests, storedKeyFor } from '~/lib/browserStorage'
import { readKvRow } from '~/lib/browserStorageDb'
import { createPersistedSeq } from '~/lib/persistedSeq'
import { installBroadcastChannelIsolation } from './broadcastChannelIsolation'
import { TEST_USER_ID } from './crdtBridge'

describe('installBroadcastChannelIsolation', () => {
  it.each(['', undefined])('refuses an absent file path: %s', (path) => {
    const original = globalThis.BroadcastChannel
    expect(() => installBroadcastChannelIsolation(path)).toThrow('Test broadcasts require a nonempty test file path.')
    expect(globalThis.BroadcastChannel).toBe(original)
  })

  it.each(['', 'shared:channel'])('keeps native delivery local to each file scope for channel %j', async (name) => {
    const original = Object.getOwnPropertyDescriptor(globalThis, 'BroadcastChannel')!
    const channels: BroadcastChannel[] = []
    try {
      installBroadcastChannelIsolation('first test file')
      const first = new BroadcastChannel(name)
      const firstPeer = new BroadcastChannel(name)
      channels.push(first, firstPeer)
      installBroadcastChannelIsolation('second test file')
      const second = new BroadcastChannel(name)
      const secondPeer = new BroadcastChannel(name)
      channels.push(second, secondPeer)

      const firstValues: unknown[] = []
      const secondValues: unknown[] = []
      const firstDelivered = new Promise<void>((resolve) => {
        first.onmessage = (event) => {
          firstValues.push(event.data)
          resolve()
        }
      })
      const secondDelivered = new Promise<void>((resolve) => {
        second.onmessage = (event) => {
          secondValues.push(event.data)
          resolve()
        }
      })
      firstPeer.postMessage('first')
      secondPeer.postMessage('second')
      await Promise.all([firstDelivered, secondDelivered])

      expect(first.name).toBe(name)
      expect(second.name).toBe(name)
      expect(firstValues).toEqual(['first'])
      expect(secondValues).toEqual(['second'])
    }
    finally {
      for (const channel of channels)
        channel.close()
      Object.defineProperty(globalThis, 'BroadcastChannel', original)
    }
  })
})

describe('broadcast channel isolation between test files', () => {
  it.each([false, true])('keeps a foreign database out of sequence repair after restoring globals=%s', async (restoreGlobals) => {
    if (restoreGlobals) {
      vi.stubGlobal('BroadcastChannel', undefined)
      vi.unstubAllGlobals()
    }
    vi.stubGlobal('indexedDB', new IDBFactory())
    resetBrowserStorageForTests()
    setStorageAccountForTests(TEST_USER_ID)

    const topic = 'leapmux:browser-storage'
    // Native channels represent another test file's independent database.
    const foreignSender = new NativeBroadcastChannel(topic)
    const foreignObserver = new NativeBroadcastChannel(topic)
    const localSender = new BroadcastChannel(topic)
    const localObserver = new BroadcastChannel(topic)
    const foreignId = randomUUID()
    const foreignBarrier = randomUUID()
    const localBarrier = randomUUID()
    const marker = storedKeyFor(`${PREFIX_FILES_SHOW_HIDDEN}broadcast-isolation:/barrier-${localBarrier}`)!
    const seen: string[] = []
    let finishApplied!: () => void
    const applied = new Promise<void>((resolve) => {
      finishApplied = resolve
    })
    const stop = onStorageChanged((keys) => {
      if (keys?.has(marker))
        finishApplied()
    })
    try {
      const poison = localStorageSet(KEY_CHANNEL_RELAY_SEQ, Number.POSITIVE_INFINITY)
      await expect(poison.durable).resolves.toBe(true)
      await flushStorageWrites()

      const delivered = new Promise<void>((resolve) => {
        foreignObserver.onmessage = (event) => {
          if (event.data?.from === foreignBarrier) {
            localSender.postMessage({
              from: localBarrier,
              changes: [{ k: marker, v: true, e: Date.now() + 60_000 }],
            })
          }
        }
        localObserver.onmessage = (event) => {
          const from: unknown = event.data?.from
          if (from === foreignId || from === foreignBarrier || from === localBarrier)
            seen.push(from)
          if (from === localBarrier)
            resolve()
        }
      })
      // The foreign barrier reaches the native bus before the local barrier is
      // sent. No sleep decides whether the foreign write reached its observer.
      foreignSender.postMessage({
        from: foreignId,
        changes: [{ k: 'leapmux:channel-relay-seq', v: 7, e: Date.now() + 60_000 }],
      })
      foreignSender.postMessage({ from: foreignBarrier, changes: [] })
      await Promise.all([delivered, applied])

      const next = createPersistedSeq(KEY_CHANNEL_RELAY_SEQ)
      expect(Number.isSafeInteger(next())).toBe(true)
      await flushStorageWrites()

      expect.soft(localStorageGet(KEY_CHANNEL_RELAY_SEQ)).toBe(1)
      expect.soft(await readKvRow('leapmux:channel-relay-seq')).toMatchObject({ v: 1 })
      expect.soft(seen).toEqual([localBarrier])
      expect(localObserver.name).toBe(topic)
    }
    finally {
      stop()
      foreignSender.close()
      foreignObserver.close()
      localSender.close()
      localObserver.close()
      resetBrowserStorageForTests()
      vi.unstubAllGlobals()
    }
  })
})
