import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { deferred, settle } from '~/test-support/async'
import { testWorkerKeyBundle as bundle, seedKeyPin, testKeyPinHex } from '~/test-support/keyPins'
import { useTestStorage } from '~/test-support/persistentStorage'
import * as browserStorage from './browserStorage'
import { readKeyPinState } from './browserStorage'
import {
  clearAllKeyPins,
  clearKeyPin,
  KeyPinMissingReviewError,
  KeyPinRejectedError,
  KeyPinStore,
  listKeyPins,
  onKeyPinsChanged,
  readWorkerKeyPin,
} from './keyPinStore'

describe('keyPinStore', () => {
  useTestStorage()

  beforeEach(async () => {
    await clearAllKeyPins()
  })

  afterEach(async () => {
    await clearAllKeyPins()
  })

  it('first-use resolve returns a commit that writes the pin', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const commit = await store.resolve('w1', bundle(1))
    expect((await readKeyPinState()).pins).toEqual({})
    await commit.commit()
    const state = await readKeyPinState()
    expect(Object.keys(state.pins)).toEqual(['w1'])
    expect(state.pins.w1?.publicKeyHex.length).toBeGreaterThan(0)
  })

  it('returns a validating no-write commit for a matching pin', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const commit1 = await store.resolve('w1', bundle(1))
    await commit1.commit()
    const before = await readWorkerKeyPin('w1')
    const confirmKeyPin = vi.fn()
    store.setConfirmKeyPin(confirmKeyPin)
    const commit2 = await store.resolve('w1', bundle(1))
    await commit2.commit()
    expect(confirmKeyPin).not.toHaveBeenCalled()
    // Nothing was rewritten: the record is byte-for-byte the one committed.
    expect(await readWorkerKeyPin('w1')).toEqual(before)
  })

  it('fails a matching commit when the observed pin was removed since the decision', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    await (await store.resolve('w1', bundle(1))).commit()
    const commit = await store.resolve('w1', bundle(1))
    // Another tab removes the pin while the channel proves itself.
    await clearKeyPin('w1')
    await expect(commit.commit()).rejects.toBeInstanceOf(KeyPinMissingReviewError)
    expect((await readKeyPinState()).pins).toEqual({})
  })

  it('fails a first-contact commit when a different key was installed since the decision', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    })
    const commit = await store.resolve('w1', bundle(1))
    await seedKeyPin('w1', {
      publicKeyHex: testKeyPinHex('09'),
      firstSeen: 5,
    })
    await expect(commit.commit()).rejects.toThrow(/different key was already recorded/)
    expect((await readKeyPinState()).pins.w1?.publicKeyHex).toBe(testKeyPinHex('09'))
  })

  it('converges concurrent same-key first contacts on one record', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    })
    const commit1 = await store.resolve('w1', bundle(1))
    const commit2 = await store.resolve('w1', bundle(1))
    await commit1.commit()
    const firstSeen = (await readWorkerKeyPin('w1')).pin?.firstSeen
    expect(firstSeen).toBeDefined()
    // The second decision observed the same absent state; its commit keeps the
    // record the first one installed rather than stamping a new firstSeen.
    await commit2.commit()
    expect((await readWorkerKeyPin('w1')).pin?.firstSeen).toBe(firstSeen)
  })

  it('fails a replacement commit when another tab replaced the observed pin', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1))).commit()
    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    await seedKeyPin('w1', {
      publicKeyHex: testKeyPinHex('08'),
      firstSeen: 7,
    })
    await expect(commit.commit()).rejects.toThrow(/changed since the decision/)
    expect((await readKeyPinState()).pins.w1?.publicKeyHex).toBe(testKeyPinHex('08'))
  })

  it('mismatch accept updates the pin via commit', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1))).commit()
    const before = (await readWorkerKeyPin('w1')).pin?.publicKeyHex
    expect(before).toBeTruthy()

    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    expect(confirmKeyPin).toHaveBeenCalledWith('w1', expect.any(String), expect.any(String), expect.any(AbortSignal))
    // Deferred-commit fence: the pin stays on the old key until the caller
    // proves the session and runs commit (open-time Ping contract).
    expect((await readWorkerKeyPin('w1')).pin?.publicKeyHex).toBe(before)
    await commit.commit()
    const after = (await readWorkerKeyPin('w1')).pin?.publicKeyHex
    expect(after).not.toBe(before)
  })

  it('mismatch reject throws and auto-rejects the next resolve', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('reject' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1))).commit()

    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(confirmKeyPin).toHaveBeenCalledOnce()

    // Session auto-reject: no second prompt.
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(confirmKeyPin).toHaveBeenCalledOnce()
  })

  /**
   * The prompt registration belongs to a UI mount; this store is a module-level
   * singleton that outlives it. Without a disposer the singleton keeps a closure
   * over the dead mount's dialog, which nothing renders — so its `resolve` is
   * never called, and `KeyPinStore.resolve` awaits that with no timeout while
   * `enqueueConfirm` queues every later prompt behind it. Failing closed is the
   * only correct answer when there is no UI to ask.
   */
  it('restores the fail-closed default when the registration is disposed', async () => {
    const mounted = vi.fn().mockResolvedValue('accept' as const)
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const dispose = store.setConfirmKeyPin(mounted)
    await (await store.resolve('w1', bundle(1))).commit()

    dispose()

    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(mounted, 'a disposed prompt must never be asked').not.toHaveBeenCalled()
  })

  it('a stale disposer does not clobber a newer registration', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const first = vi.fn().mockResolvedValue('reject' as const)
    const second = vi.fn().mockResolvedValue('accept' as const)
    const disposeFirst = store.setConfirmKeyPin(first)
    store.setConfirmKeyPin(second)

    // The old mount tears down AFTER the new one registered — the ordinary
    // remount interleaving. It must not drag the live prompt back to the stub.
    disposeFirst()

    await (await store.resolve('w1', bundle(1))).commit()
    await (await store.resolve('w1', bundle(9))).commit()
    expect(second, 'the live mount still owns the prompt').toHaveBeenCalledOnce()
    expect(first).not.toHaveBeenCalled()
  })

  it('does not clear the session reject set when clearKeyPin runs', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('reject' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1))).commit()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(confirmKeyPin).toHaveBeenCalledOnce()

    await clearKeyPin('w1')
    // First-use after clear may re-pin; the session reject set must still
    // short-circuit a later mismatch without prompting again.
    await (await store.resolve('w1', bundle(1))).commit()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(confirmKeyPin).toHaveBeenCalledOnce()
  })

  it('concurrent commits to different workers do not drop pins', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    })
    const c1 = await store.resolve('w1', bundle(1))
    const c2 = await store.resolve('w2', bundle(2))
    // Interleave commits the way openChannel used to: both decisions taken
    // before either write. Strict commits serialize without losing either pin.
    await Promise.all([
      c1.commit(),
      c2.commit(),
    ])
    expect(Object.keys((await readKeyPinState()).pins).sort()).toEqual(['w1', 'w2'])
  })

  it('removes stored pins through clearKeyPin and clearAllKeyPins', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    })
    await (await store.resolve('w1', bundle(1))).commit()
    await (await store.resolve('w2', bundle(2))).commit()
    await clearKeyPin('w1')
    expect(Object.keys((await readKeyPinState()).pins)).toEqual(['w2'])
    await clearKeyPin('w2')
    expect((await readKeyPinState()).pins).toEqual({})
    await (await store.resolve('w3', bundle(3))).commit()
    await clearAllKeyPins()
    expect((await readKeyPinState()).pins).toEqual({})
  })

  it('clears the session reject set in setConfirmKeyPin', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    await (await store.resolve('w1', bundle(1))).commit()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)

    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    store.setConfirmKeyPin(confirmKeyPin)
    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    await commit.commit()
  })

  it('cancels a displayed prompt before a new registration displays', async () => {
    let releaseFirst!: (d: 'accept' | 'reject') => void
    let firstStarted = false
    const firstPrompt = new Promise<'accept' | 'reject'>((resolve) => {
      releaseFirst = resolve
    })
    const store = new KeyPinStore({
      confirmKeyPin: async () => {
        firstStarted = true
        return firstPrompt
      },
    })
    await (await store.resolve('w1', bundle(1))).commit()

    const pending = store.resolve('w1', bundle(9))
    const cancelled = expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    await vi.waitFor(() => expect(firstStarted).toBe(true))
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    store.setConfirmKeyPin(confirmKeyPin)
    releaseFirst('reject')
    await cancelled

    // Stale reject must not poison the new UI — next mismatch prompts again.
    const commit = await store.resolve('w1', bundle(8))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    await commit.commit()
  })

  it('retires a displayed prompt when the same callback is registered again', async () => {
    const displayed = deferred<void>()
    const answer = deferred<'accept' | 'reject'>()
    const confirmKeyPin = vi.fn()
      .mockImplementationOnce(async () => {
        displayed.resolve()
        return answer.promise
      })
      .mockResolvedValue('accept')
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const disposeOld = store.setConfirmKeyPin(confirmKeyPin)
    await (await store.resolve('w1', bundle(1))).commit()

    const pending = store.resolve('w1', bundle(2))
    const settled = pending.then(() => 'accepted', err => err instanceof Error && err.name)
    await displayed.promise
    store.setConfirmKeyPin(confirmKeyPin)
    disposeOld()
    const outcome = await Promise.race([
      settled,
      settle().then(() => 'still waiting'),
    ])
    answer.resolve('accept')
    await settled
    expect(outcome).toBe('AbortError')
    await (await store.resolve('w1', bundle(3))).commit()
    expect(confirmKeyPin).toHaveBeenCalledTimes(2)
  })

  it('cancels a queued prompt with its identity and lets the successor confirm the same Worker', async () => {
    const outgoing = new AbortController()
    const successor = new AbortController()
    const displayed = deferred<void>()
    const answer = deferred<'accept' | 'reject'>()
    const confirmKeyPin = vi.fn()
      .mockImplementationOnce(() => {
        displayed.resolve()
        return answer.promise
      })
      .mockResolvedValue('accept')
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1))).commit()
    await (await store.resolve('w2', bundle(2))).commit()
    const outgoingScope = { identitySignal: outgoing.signal, signal: outgoing.signal }
    const current = store.resolve('w1', bundle(3), outgoingScope)
    const currentCancelled = expect(current).rejects.toMatchObject({ name: 'AbortError' })
    await displayed.promise

    const queuedRead = deferred<void>()
    const read = browserStorage.readKeyPinState
    const readSpy = vi.spyOn(browserStorage, 'readKeyPinState').mockImplementationOnce(async (scope) => {
      const state = await read(scope)
      queuedRead.resolve()
      return state
    })
    try {
      const queued = store.resolve('w2', bundle(4), outgoingScope)
      const queuedCancelled = expect(queued).rejects.toMatchObject({ name: 'AbortError' })
      await queuedRead.promise
      await settle()
      outgoing.abort()
      await Promise.all([currentCancelled, queuedCancelled])

      const fresh = await store.resolve('w2', bundle(5), { identitySignal: successor.signal, signal: successor.signal })
      await fresh.commit()
      expect(confirmKeyPin.mock.calls.map(call => call[0])).toEqual(['w1', 'w2'])
      expect((await readWorkerKeyPin('w2')).pin?.publicKeyHex).toBe(testKeyPinHex('05'))
    }
    finally {
      answer.resolve('reject')
      readSpy.mockRestore()
    }
  })

  it('serializes concurrent mismatch prompts', async () => {
    const firstDisplayed = deferred<void>()
    const answerFirst = deferred<'accept'>()
    const confirmKeyPin = vi.fn()
      .mockImplementationOnce(() => {
        firstDisplayed.resolve()
        return answerFirst.promise
      })
      .mockResolvedValue('accept')
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1))).commit()
    await (await store.resolve('w2', bundle(2))).commit()

    const first = store.resolve('w1', bundle(9))
    await firstDisplayed.promise
    const secondEnqueued = deferred<void>()
    const queue = store as unknown as {
      enqueueConfirm: (signal: AbortSignal, confirm: () => Promise<void>) => Promise<void>
    }
    const enqueue = queue.enqueueConfirm.bind(store)
    const enqueueSpy = vi.spyOn(queue, 'enqueueConfirm').mockImplementationOnce((signal, confirm) => {
      const queued = enqueue(signal, confirm)
      secondEnqueued.resolve()
      return queued
    })
    const second = store.resolve('w2', bundle(8))
    const decisions = Promise.all([first, second])
    try {
      await secondEnqueued.promise
      expect(confirmKeyPin.mock.calls.map(call => call[0])).toEqual(['w1'])
    }
    finally {
      answerFirst.resolve('accept')
      enqueueSpy.mockRestore()
    }
    const [c1, c2] = await decisions
    expect(confirmKeyPin.mock.calls.map(call => call[0])).toEqual(['w1', 'w2'])
    await c1.commit()
    await c2.commit()
    expect((await readKeyPinState()).pins.w1?.publicKeyHex).toBe(testKeyPinHex('09'))
    expect((await readKeyPinState()).pins.w2?.publicKeyHex).toBe(testKeyPinHex('08'))
  })
})

describe('readWorkerKeyPin', () => {
  useTestStorage()

  it('reads the durable pin with its removal revision, and null when unpinned', async () => {
    expect(await readWorkerKeyPin('w1')).toEqual({ pin: null, removalRevision: 0 })
    await seedKeyPin('w1', {
      publicKeyHex: testKeyPinHex('aa'),
      firstSeen: 1000,
    })
    const snapshot = await readWorkerKeyPin('w1')
    expect(snapshot.pin).toEqual({
      publicKeyHex: testKeyPinHex('aa'),
      firstSeen: 1000,
    })
    expect(snapshot.removalRevision).toBe(0)
    expect(await readWorkerKeyPin('w2')).toEqual({ pin: null, removalRevision: 0 })
  })
})

describe('listKeyPins', () => {
  useTestStorage()

  it('lists pinned workers oldest first', async () => {
    await seedKeyPin('worker-b', {
      publicKeyHex: testKeyPinHex('bb'),
      firstSeen: 2000,
    })
    await seedKeyPin('worker-a', {
      publicKeyHex: testKeyPinHex('aa'),
      firstSeen: 1000,
    })
    expect(await listKeyPins()).toEqual([
      {
        workerId: 'worker-a',
        publicKeyHex: testKeyPinHex('aa'),
        firstSeen: 1000,
      },
      {
        workerId: 'worker-b',
        publicKeyHex: testKeyPinHex('bb'),
        firstSeen: 2000,
      },
    ])
  })

  it('rejects on a failed read rather than reporting no pins', async () => {
    await seedKeyPin('worker-a', {
      publicKeyHex: testKeyPinHex('aa'),
      firstSeen: 1000,
    })
    // Storage goes away entirely: "unpinned" and "cannot read" are different
    // trust decisions, and only the first may look like an empty list.
    vi.unstubAllGlobals()
    await expect(listKeyPins()).rejects.toThrow('Browser storage is unavailable.')
  })
})

describe('clearKeyPin', () => {
  useTestStorage()

  it('advances the removal revision on every successful Remove', async () => {
    await seedKeyPin('w1', {
      publicKeyHex: testKeyPinHex('aa'),
      firstSeen: 1,
    })
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const pending = await store.resolve('absent', bundle(1))

    await clearKeyPin('absent')
    await expect(pending.commit()).rejects.toThrow(/revision changed/)
    expect(await readKeyPinState()).toEqual({
      pins: {
        w1: {
          publicKeyHex: testKeyPinHex('aa'),
          firstSeen: 1,
        },
      },
      removalRevision: 1,
    })

    await clearKeyPin('w1')
    await clearKeyPin('w1')
    expect(await readKeyPinState()).toEqual({ pins: {}, removalRevision: 3 })
  })

  it('invalidates pending first contact when Remove all finds no pins', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const pending = await store.resolve('w1', bundle(1))

    await clearAllKeyPins()
    await expect(pending.commit()).rejects.toThrow(/revision changed/)
    expect(await readKeyPinState()).toEqual({ pins: {}, removalRevision: 1 })

    await seedKeyPin('w1', {
      publicKeyHex: testKeyPinHex('aa'),
      firstSeen: 1,
    })
    await clearAllKeyPins()
    expect(await readKeyPinState()).toEqual({ pins: {}, removalRevision: 2 })
  })
})

describe('onKeyPinsChanged', () => {
  useTestStorage()

  it('fires on a committed change and on an account move, and stops on unsubscribe', async () => {
    const listener = vi.fn()
    const off = onKeyPinsChanged(listener)
    await seedKeyPin('w1', {
      publicKeyHex: testKeyPinHex('aa'),
      firstSeen: 1,
    })
    await settle()
    expect(listener).toHaveBeenCalled()

    listener.mockClear()
    // An unrelated account change is still a "the state you show may not be
    // yours" signal for a settings surface.
    const { setStorageAccountForTests } = await import('./browserStorage')
    setStorageAccountForTests('another-account')
    await settle()
    expect(listener).toHaveBeenCalled()

    listener.mockClear()
    off()
    await clearAllKeyPins()
    await settle()
    expect(listener).not.toHaveBeenCalled()
  })
})
