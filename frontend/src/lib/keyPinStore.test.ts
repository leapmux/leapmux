import type { KeyPinKeyBundle } from './keyPinStore'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_KEY_PINS, localStorageGet } from './browserStorage'
import { clearAllKeyPins, clearKeyPin, KeyPinRejectedError, KeyPinStore } from './keyPinStore'

function bundle(fill: number): KeyPinKeyBundle {
  return {
    x25519PublicKey: new Uint8Array(32).fill(fill),
    mlkemPublicKey: new Uint8Array(0),
    slhdsaPublicKey: new Uint8Array(0),
  }
}

describe('keyPinStore', () => {
  beforeEach(() => {
    clearAllKeyPins()
  })

  afterEach(() => {
    clearAllKeyPins()
  })

  it('first-use resolve returns a commit that writes the pin', async () => {
    const store = new KeyPinStore({ confirmKeyPin: async () => 'reject' })
    const commit = await store.resolve('w1', bundle(1))
    expect(localStorageGet(KEY_KEY_PINS) ?? {}).toEqual({})
    commit()
    const pins = localStorageGet<Record<string, { publicKeyHex: string }>>(KEY_KEY_PINS) ?? {}
    expect(Object.keys(pins)).toEqual(['w1'])
    expect(pins.w1.publicKeyHex.length).toBeGreaterThan(0)
  })

  it('matching pin returns a no-op commit', async () => {
    const store = new KeyPinStore({ confirmKeyPin: async () => 'reject' })
    const commit1 = await store.resolve('w1', bundle(1))
    commit1()
    const confirmKeyPin = vi.fn()
    store.setConfirmKeyPin(confirmKeyPin)
    const commit2 = await store.resolve('w1', bundle(1))
    commit2()
    expect(confirmKeyPin).not.toHaveBeenCalled()
    expect(Object.keys(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {})).toEqual(['w1'])
  })

  it('mismatch accept updates the pin via commit', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    ;(await store.resolve('w1', bundle(1)))()
    const before = localStorageGet<Record<string, { publicKeyHex: string }>>(KEY_KEY_PINS)!.w1.publicKeyHex

    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    expect(confirmKeyPin).toHaveBeenCalledWith('w1', expect.any(String), expect.any(String))
    // Deferred-commit fence: the pin stays on the old key until the caller
    // proves the session and runs commit (open-time Ping contract).
    expect(localStorageGet<Record<string, { publicKeyHex: string }>>(KEY_KEY_PINS)!.w1.publicKeyHex).toBe(before)
    commit()
    const after = localStorageGet<Record<string, { publicKeyHex: string }>>(KEY_KEY_PINS)!.w1.publicKeyHex
    expect(after).not.toBe(before)
  })

  it('mismatch reject throws and auto-rejects the next resolve', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('reject' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    ;(await store.resolve('w1', bundle(1)))()

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
    const store = new KeyPinStore({ confirmKeyPin: async () => 'reject' })
    const dispose = store.setConfirmKeyPin(mounted)
    ;(await store.resolve('w1', bundle(1)))()

    dispose()

    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(mounted, 'a disposed prompt must never be asked').not.toHaveBeenCalled()
  })

  it('a stale disposer does not clobber a newer registration', async () => {
    const store = new KeyPinStore({ confirmKeyPin: async () => 'reject' })
    const first = vi.fn().mockResolvedValue('reject' as const)
    const second = vi.fn().mockResolvedValue('accept' as const)
    const disposeFirst = store.setConfirmKeyPin(first)
    store.setConfirmKeyPin(second)

    // The old mount tears down AFTER the new one registered — the ordinary
    // remount interleaving. It must not drag the live prompt back to the stub.
    disposeFirst()

    ;(await store.resolve('w1', bundle(1)))()
    ;(await store.resolve('w1', bundle(9)))()
    expect(second, 'the live mount still owns the prompt').toHaveBeenCalledOnce()
    expect(first).not.toHaveBeenCalled()
  })

  it('clearKeyPin does not clear the session reject set', async () => {
    const confirmKeyPin = vi.fn().mockResolvedValue('reject' as const)
    const store = new KeyPinStore({ confirmKeyPin })
    ;(await store.resolve('w1', bundle(1)))()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(confirmKeyPin).toHaveBeenCalledOnce()

    clearKeyPin('w1')
    // First-use after clear may re-pin; the session reject set must still
    // short-circuit a later mismatch without prompting again.
    ;(await store.resolve('w1', bundle(1)))()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)
    expect(confirmKeyPin).toHaveBeenCalledOnce()
  })

  it('concurrent commits to different workers do not drop pins', async () => {
    const store = new KeyPinStore({ confirmKeyPin: async () => 'accept' })
    const c1 = await store.resolve('w1', bundle(1))
    const c2 = await store.resolve('w2', bundle(2))
    // Interleave commits the way openChannel used to: both decisions taken before either write.
    c1()
    c2()
    expect(Object.keys(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {}).sort()).toEqual(['w1', 'w2'])
  })

  it('clearKeyPin and clearAllKeyPins remove stored pins', async () => {
    const store = new KeyPinStore({ confirmKeyPin: async () => 'accept' })
    ;(await store.resolve('w1', bundle(1)))()
    ;(await store.resolve('w2', bundle(2)))()
    clearKeyPin('w1')
    expect(Object.keys(localStorageGet<Record<string, unknown>>(KEY_KEY_PINS) ?? {})).toEqual(['w2'])
    clearKeyPin('w2')
    expect(localStorageGet(KEY_KEY_PINS) ?? {}).toEqual({})
    ;(await store.resolve('w3', bundle(3)))()
    clearAllKeyPins()
    expect(localStorageGet(KEY_KEY_PINS) ?? {}).toEqual({})
  })

  it('setConfirmKeyPin clears the session reject set', async () => {
    const store = new KeyPinStore({ confirmKeyPin: async () => 'reject' })
    ;(await store.resolve('w1', bundle(1)))()
    await expect(store.resolve('w1', bundle(9))).rejects.toBeInstanceOf(KeyPinRejectedError)

    const confirmKeyPin = vi.fn().mockResolvedValue('accept' as const)
    store.setConfirmKeyPin(confirmKeyPin)
    const commit = await store.resolve('w1', bundle(9))
    expect(confirmKeyPin).toHaveBeenCalledOnce()
    commit()
  })

  it('setConfirmKeyPin drops in-flight rejects from the prior prompt epoch', async () => {
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
    ;(await store.resolve('w1', bundle(1)))()

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
    commit()
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
    ;(await store.resolve('w1', bundle(1)))()
    ;(await store.resolve('w2', bundle(2)))()

    const p1 = store.resolve('w1', bundle(9))
    const p2 = store.resolve('w2', bundle(8))
    // Second prompt must not start until the first settles.
    await Promise.resolve()
    expect(secondStarted).toBe(false)
    releaseFirst('accept')
    const [c1, c2] = await Promise.all([p1, p2])
    expect(secondStarted).toBe(true)
    c1()
    c2()
    expect(confirmKeyPin).toHaveBeenCalledTimes(2)
  })
})
