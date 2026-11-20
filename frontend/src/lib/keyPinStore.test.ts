import type { KeyPinKeyBundle } from './keyPinStore'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readKeyPinState } from './browserStorage'
import {
  clearAllKeyPins,
  clearKeyPin,
  KeyPinRejectedError,
  KeyPinStore,
  listKeyPins,
  onKeyPinsChanged,
  readWorkerKeyPin,
} from './keyPinStore'
import { settle } from '~/test-support/async'
import { seedKeyPin } from '~/test-support/keyPins'
import { useTestStorage } from '~/test-support/persistentStorage'

function bundle(fill: number): KeyPinKeyBundle {
  return {
    x25519PublicKey: new Uint8Array(32).fill(fill),
    mlkemPublicKey: new Uint8Array(0),
    slhdsaPublicKey: new Uint8Array(0),
  }
}

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
    await commit()
    const state = await readKeyPinState()
    expect(Object.keys(state.pins)).toEqual(['w1'])
    expect(state.pins.w1?.publicKeyHex.length).toBeGreaterThan(0)
  })

  it('matching pin returns a commit that writes nothing but validates', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    const commit1 = await store.resolve('w1', bundle(1))
    await commit1()
    const before = await readWorkerKeyPin('w1')
    const confirmKeyPin = vi.fn()
    store.setConfirmKeyPin(confirmKeyPin)
    const commit2 = await store.resolve('w1', bundle(1))
    await commit2()
    expect(confirmKeyPin).not.toHaveBeenCalled()
    // Nothing was rewritten: the record is byte-for-byte the one committed.
    expect(await readWorkerKeyPin('w1')).toEqual(before)
  })

  it('a matching commit fails when the observed pin was removed since the decision', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    await (await store.resolve('w1', bundle(1)))()
    const commit = await store.resolve('w1', bundle(1))
    // Another tab removes the pin while the channel proves itself.
    await clearKeyPin('w1')
    await expect(commit()).rejects.toThrow(/changed since the decision/)
    expect((await readKeyPinState()).pins).toEqual({})
  })

  it('a first-contact commit fails when a different key was installed since the decision', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    })
    const commit = await store.resolve('w1', bundle(1))
    await seedKeyPin('w1', { publicKeyHex: 'a-different-key', firstSeen: 5 })
    await expect(commit()).rejects.toThrow(/different key was already recorded/)
    expect((await readKeyPinState()).pins.w1?.publicKeyHex).toBe('a-different-key')
  })

  it('converges concurrent same-key first contacts on one record', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    })
    const commit1 = await store.resolve('w1', bundle(1))
    const commit2 = await store.resolve('w1', bundle(1))
    await commit1()
    const firstSeen = (await readWorkerKeyPin('w1')).pin?.firstSeen
    expect(firstSeen).toBeDefined()
    // The second decision observed the same absent state; its commit keeps the
    // record the first one installed rather than stamping a new firstSeen.
    await commit2()
    expect((await readWorkerKeyPin('w1')).pin?.firstSeen).toBe(firstSeen)
  })

  it('a replacement commit fails when the observed pin was replaced in another tab', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1)))()
    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    await seedKeyPin('w1', { publicKeyHex: 'replaced-elsewhere', firstSeen: 7 })
    await expect(commit()).rejects.toThrow(/changed since the decision/)
    expect((await readKeyPinState()).pins.w1?.publicKeyHex).toBe('replaced-elsewhere')
  })

  it('mismatch accept updates the pin via commit', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1)))()
    const before = (await readWorkerKeyPin('w1')).pin?.publicKeyHex
    expect(before).toBeTruthy()

    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    expect(confirmKeyPin).toHaveBeenCalledWith('w1', expect.any(String), expect.any(String))
    // Deferred-commit fence: the pin stays on the old key until the caller
    // proves the session and runs commit (open-time Ping contract).
    expect((await readWorkerKeyPin('w1')).pin?.publicKeyHex).toBe(before)
    await commit()
    const after = (await readWorkerKeyPin('w1')).pin?.publicKeyHex
    expect(after).not.toBe(before)
  })

  it('mismatch reject throws and auto-rejects the next resolve', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('reject' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1)))()

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
    await (await store.resolve('w1', bundle(1)))()

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

    await (await store.resolve('w1', bundle(1)))()
    await (await store.resolve('w1', bundle(9)))()
    expect(second, 'the live mount still owns the prompt').toHaveBeenCalledOnce()
    expect(first).not.toHaveBeenCalled()
  })

  it('does not clear the session reject set when clearKeyPin runs', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('reject' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1)))()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(confirmKeyPin).toHaveBeenCalledOnce()

    await clearKeyPin('w1')
    // First-use after clear may re-pin; the session reject set must still
    // short-circuit a later mismatch without prompting again.
    await (await store.resolve('w1', bundle(1)))()
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
    await Promise.all([c1(), c2()])
    expect(Object.keys((await readKeyPinState()).pins).sort()).toEqual(['w1', 'w2'])
  })

  it('removes stored pins through clearKeyPin and clearAllKeyPins', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'accept',
    })
    await (await store.resolve('w1', bundle(1)))()
    await (await store.resolve('w2', bundle(2)))()
    await clearKeyPin('w1')
    expect(Object.keys((await readKeyPinState()).pins)).toEqual(['w2'])
    await clearKeyPin('w2')
    expect((await readKeyPinState()).pins).toEqual({})
    await (await store.resolve('w3', bundle(3)))()
    await clearAllKeyPins()
    expect((await readKeyPinState()).pins).toEqual({})
  })

  it('clears the session reject set in setConfirmKeyPin', async () => {
    const store = new KeyPinStore({
      confirmKeyPin: async () => 'reject',
    })
    await (await store.resolve('w1', bundle(1)))()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)

    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    store.setConfirmKeyPin(confirmKeyPin)
    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    await commit()
  })

  it('drops in-flight rejects from the prior prompt epoch in setConfirmKeyPin', async () => {
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
    await (await store.resolve('w1', bundle(1)))()

    const pending = store.resolve('w1', bundle(9))
    // The enqueueConfirm callback reads confirmKeyPin at run time — wait until
    // the OLD prompt is actually awaiting before swapping the UI.
    await vi.waitFor(() => expect(firstStarted).toBe(true))
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    store.setConfirmKeyPin(confirmKeyPin)
    releaseFirst('reject')
    await expect(pending).rejects.toBeInstanceOf(KeyPinRejectedError)

    // Stale reject must not poison the new UI — next mismatch prompts again.
    const commit = await store.resolve('w1', bundle(8))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    await commit()
  })

  it('serializes concurrent mismatch prompts', async () => {
    let releaseFirst!: (d: 'accept' | 'reject') => void
    const firstPrompt = new Promise<'accept' | 'reject'>((resolve) => {
      releaseFirst = resolve
    })
    let secondStarted = false
    const confirmKeyPin = vi.fn()
      .mockImplementationOnce(async () => firstPrompt)
      .mockImplementationOnce(async () => {
        secondStarted = true
        return 'accept'
      })
    const store = new KeyPinStore({ confirmKeyPin })
    await (await store.resolve('w1', bundle(1)))()
    await (await store.resolve('w2', bundle(2)))()

    const p1 = store.resolve('w1', bundle(9))
    const p2 = store.resolve('w2', bundle(8))
    // Second prompt must not start until the first settles.
    await Promise.resolve()
    expect(secondStarted).toBe(false)
    releaseFirst('accept')
    const [c1, c2] = await Promise.all([p1, p2])
    expect(secondStarted).toBe(true)
    await c1()
    await c2()
    expect(confirmKeyPin).toHaveBeenCalledTimes(2)
  })
})

describe('readWorkerKeyPin', () => {
  useTestStorage()

  it('reads the durable pin with its removal revision, and null when unpinned', async () => {
    expect(await readWorkerKeyPin('w1')).toEqual({ pin: null, removalRevision: 0 })
    await seedKeyPin('w1', { publicKeyHex: 'aa', firstSeen: 1000 })
    const snapshot = await readWorkerKeyPin('w1')
    expect(snapshot.pin).toEqual({ publicKeyHex: 'aa', firstSeen: 1000 })
    expect(snapshot.removalRevision).toBe(0)
    expect(await readWorkerKeyPin('w2')).toEqual({ pin: null, removalRevision: 0 })
  })
})

describe('listKeyPins', () => {
  useTestStorage()

  it('lists pinned workers oldest first', async () => {
    await seedKeyPin('worker-b', { publicKeyHex: 'bb', firstSeen: 2000 })
    await seedKeyPin('worker-a', { publicKeyHex: 'aa', firstSeen: 1000 })
    expect(await listKeyPins()).toEqual([
      { workerId: 'worker-a', publicKeyHex: 'aa', firstSeen: 1000 },
      { workerId: 'worker-b', publicKeyHex: 'bb', firstSeen: 2000 },
    ])
  })

  it('rejects on a failed read rather than reporting no pins', async () => {
    await seedKeyPin('worker-a', { publicKeyHex: 'aa', firstSeen: 1000 })
    // Storage goes away entirely: "unpinned" and "cannot read" are different
    // trust decisions, and only the first may look like an empty list.
    vi.unstubAllGlobals()
    await expect(listKeyPins()).rejects.toThrow()
  })
})

describe('clearKeyPin', () => {
  useTestStorage()

  it('advances the removal revision only when a pin was removed', async () => {
    await seedKeyPin('w1', { publicKeyHex: 'aa', firstSeen: 1 })
    await clearKeyPin('absent')
    expect((await readKeyPinState()).removalRevision).toBe(0)

    await clearKeyPin('w1')
    expect((await readKeyPinState()).removalRevision).toBe(1)

    // Idempotent: a second removal of the absent pin changes nothing.
    await clearKeyPin('w1')
    expect((await readKeyPinState()).removalRevision).toBe(1)
  })

  it('advances the removal revision on Remove all only when pins existed', async () => {
    await clearAllKeyPins()
    expect((await readKeyPinState()).removalRevision).toBe(0)
    await seedKeyPin('w1', { publicKeyHex: 'aa', firstSeen: 1 })
    await clearAllKeyPins()
    const state = await readKeyPinState()
    expect(state.removalRevision).toBe(1)
    expect(state.pins).toEqual({})
  })
})

describe('onKeyPinsChanged', () => {
  useTestStorage()

  it('fires on a committed change and on an account move, and stops on unsubscribe', async () => {
    const listener = vi.fn()
    const off = onKeyPinsChanged(listener)
    await seedKeyPin('w1', { publicKeyHex: 'aa', firstSeen: 1 })
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
