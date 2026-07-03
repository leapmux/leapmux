import type { Mock } from 'vitest'
import type { TerminalInstance } from './terminal'
import type { WebglTerminalPool } from './webglTerminalPool'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createWebglTerminalPool } from './webglTerminalPool'

// Drain all pending microtasks (the pool coalesces reconcile onto a microtask,
// and attach awaits `fontsReady`). A macrotask tick guarantees every chained
// microtask has run.
const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0))

function deferred<T = void>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

function fakeInstance(overrides: Partial<TerminalInstance> = {}): TerminalInstance {
  return {
    webglAllowed: true,
    fontsReady: Promise.resolve(),
    webglAddon: undefined,
    terminal: { element: { isConnected: true } },
    ...overrides,
  } as unknown as TerminalInstance
}

describe('createWebglTerminalPool', () => {
  let attach: Mock<(instance: TerminalInstance, onContextLoss: () => void) => boolean>
  let detach: Mock<(instance: TerminalInstance) => void>
  // The most recent onContextLoss callback handed to `attach`, so tests can
  // simulate the browser force-dropping a context.
  let lastOnContextLoss: (() => void) | undefined

  const makePool = (capacity: number): WebglTerminalPool =>
    createWebglTerminalPool({ capacity, attach, detach })

  beforeEach(() => {
    lastOnContextLoss = undefined
    attach = vi.fn<(instance: TerminalInstance, onContextLoss: () => void) => boolean>((_instance, onContextLoss) => {
      lastOnContextLoss = onContextLoss
      return true
    })
    detach = vi.fn<(instance: TerminalInstance) => void>()
  })

  it('attaches only up to capacity, keeping the hottest ids and dropping the coldest', async () => {
    const pool = makePool(3)
    const ids = ['a', 'b', 'c', 'd', 'e']
    for (const id of ids)
      pool.acquire(id, fakeInstance())
    await flush()

    // order (cold -> hot) is a,b,c,d,e; the hottest 3 win contexts.
    expect(pool.size()).toBe(3)
    expect(pool.has('a')).toBe(false)
    expect(pool.has('b')).toBe(false)
    expect(pool.has('c')).toBe(true)
    expect(pool.has('d')).toBe(true)
    expect(pool.has('e')).toBe(true)
    expect(attach).toHaveBeenCalledTimes(3)
  })

  it('re-attaches a waiting desired terminal when a slot frees up', async () => {
    const pool = makePool(2)
    pool.acquire('a', fakeInstance())
    pool.acquire('b', fakeInstance())
    pool.acquire('c', fakeInstance()) // evicts a (order a,b,c -> keep b,c)
    await flush()
    expect(pool.has('a')).toBe(false)
    expect(pool.has('b')).toBe(true)
    expect(pool.has('c')).toBe(true)

    pool.release('c')
    await flush()
    // desired is now {a, b}; a was waiting on DOM and now gets the freed slot.
    expect(pool.has('c')).toBe(false)
    expect(pool.has('a')).toBe(true)
    expect(pool.has('b')).toBe(true)
    expect(pool.size()).toBe(2)
  })

  it('never evicts the focused terminal even when it is the coldest', async () => {
    const pool = makePool(2)
    pool.acquire('a', fakeInstance(), { focused: true })
    pool.acquire('b', fakeInstance())
    pool.acquire('c', fakeInstance())
    await flush()

    // Without the focus pin, `a` (coldest) would lose to b/c. It's protected.
    expect(pool.has('a')).toBe(true)
    expect(pool.has('c')).toBe(true)
    expect(pool.has('b')).toBe(false)
    expect(pool.size()).toBe(2)
  })

  it('admits a newly focused terminal by evicting the coldest attached one', async () => {
    const pool = makePool(2)
    pool.acquire('a', fakeInstance())
    pool.acquire('b', fakeInstance())
    await flush()
    expect(pool.has('a')).toBe(true)
    expect(pool.has('b')).toBe(true)

    // A cold, unattached terminal gains focus -> must get a slot.
    pool.acquire('c', fakeInstance(), { focused: true })
    await flush()
    expect(pool.has('c')).toBe(true)
    expect(pool.size()).toBe(2)
    // The coldest previously-attached (a) is the eviction victim, not b.
    expect(pool.has('a')).toBe(false)
    expect(pool.has('b')).toBe(true)
  })

  it('never attaches a WebGL-ineligible terminal', async () => {
    const pool = makePool(4)
    pool.acquire('a', fakeInstance({ webglAllowed: false }))
    await flush()
    expect(pool.has('a')).toBe(false)
    expect(pool.size()).toBe(0)
    expect(attach).not.toHaveBeenCalled()
  })

  it('does not attach a terminal whose element is detached', async () => {
    const pool = makePool(4)
    pool.acquire('a', fakeInstance({ terminal: { element: { isConnected: false } } as any }))
    await flush()
    expect(pool.has('a')).toBe(false)
    expect(attach).not.toHaveBeenCalled()
  })

  it('re-attaches after a context loss while the terminal is still desired', async () => {
    const pool = makePool(2)
    pool.acquire('a', fakeInstance())
    await flush()
    expect(pool.has('a')).toBe(true)
    expect(attach).toHaveBeenCalledTimes(1)

    // Simulate the browser dropping the GPU context.
    lastOnContextLoss!()
    await flush()

    expect(detach).toHaveBeenCalledTimes(1)
    expect(attach).toHaveBeenCalledTimes(2)
    expect(pool.has('a')).toBe(true)
  })

  it('stops re-attaching after repeated context losses, then recovers on re-acquire', async () => {
    const pool = makePool(2)
    pool.acquire('a', fakeInstance())
    await flush()
    expect(attach).toHaveBeenCalledTimes(1)

    // Each loss triggers exactly one re-attach, up to the retry budget.
    lastOnContextLoss!()
    await flush()
    expect(attach).toHaveBeenCalledTimes(2)
    lastOnContextLoss!()
    await flush()
    expect(attach).toHaveBeenCalledTimes(3)

    // Budget exhausted: the pool stops thrashing and leaves it on DOM.
    lastOnContextLoss!()
    await flush()
    expect(attach).toHaveBeenCalledTimes(3)
    expect(pool.has('a')).toBe(false)

    // A release + re-acquire (e.g. tab switch) resets the budget and recovers.
    pool.release('a')
    await flush()
    pool.acquire('a', fakeInstance())
    await flush()
    expect(pool.has('a')).toBe(true)
    expect(attach).toHaveBeenCalledTimes(4)
  })

  it('resets the loss budget once a context survives the decay window, without a release', async () => {
    // A persistently-visible terminal that is never released must still recover
    // WebGL after transient GPU pressure eases -- otherwise its lifetime loss
    // count, which release would normally reset, pins it to DOM forever.
    let clock = 0
    const pool = createWebglTerminalPool({ capacity: 2, attach, detach, now: () => clock })
    pool.acquire('a', fakeInstance())
    await flush()
    expect(attach).toHaveBeenCalledTimes(1)

    // Two quick losses (inside the decay window) accumulate the budget to its
    // ceiling -- a third un-reset loss would exhaust it and leave 'a' on DOM.
    lastOnContextLoss!()
    await flush()
    lastOnContextLoss!()
    await flush()
    expect(attach).toHaveBeenCalledTimes(3)
    expect(pool.has('a')).toBe(true)

    // The context now survives past the decay window before the next loss: the
    // storm is treated as over, the budget resets, and the loss re-attaches
    // instead of giving up (which the un-reset budget would do here).
    clock += 30_001
    lastOnContextLoss!()
    await flush()
    expect(attach).toHaveBeenCalledTimes(4)
    expect(pool.has('a')).toBe(true)

    // It keeps recovering as long as each loss follows a healthy spell.
    clock += 30_001
    lastOnContextLoss!()
    await flush()
    expect(attach).toHaveBeenCalledTimes(5)
    expect(pool.has('a')).toBe(true)
  })

  it('still gives up on a rapid loss storm within the decay window', async () => {
    // The decay reset must not weaken the anti-thrash bound: losses that all
    // land inside the window keep accumulating and still exhaust the budget.
    let clock = 0
    const pool = createWebglTerminalPool({ capacity: 2, attach, detach, now: () => clock })
    pool.acquire('a', fakeInstance())
    await flush()

    // Three losses, each only a little later but all well within the window.
    for (let i = 0; i < 3; i++) {
      clock += 1000
      lastOnContextLoss!()
      await flush()
    }
    expect(attach).toHaveBeenCalledTimes(3)
    expect(pool.has('a')).toBe(false)
  })

  it('keeps a context attached across a same-tick cross-tile move (no churn)', async () => {
    const pool = makePool(4)
    const instance = fakeInstance()
    pool.acquire('a', instance)
    await flush()
    expect(pool.has('a')).toBe(true)
    expect(attach).toHaveBeenCalledTimes(1)

    // A move: the source tile releases and the destination tile re-acquires
    // the same id in the same synchronous tick, before the coalesced reconcile
    // runs. Net effect must be "still attached" with zero detach/re-attach.
    pool.release('a')
    pool.acquire('a', instance)
    await flush()

    expect(pool.has('a')).toBe(true)
    expect(detach).not.toHaveBeenCalled()
    expect(attach).toHaveBeenCalledTimes(1)
  })

  it('detaches the old instance and re-attaches when the instance is swapped for a still-attached id', async () => {
    const pool = makePool(2)
    const first = fakeInstance()
    pool.acquire('a', first)
    await flush()
    expect(pool.has('a')).toBe(true)
    expect(attach).toHaveBeenCalledTimes(1)
    expect(attach).toHaveBeenLastCalledWith(first, expect.any(Function))

    // A DIFFERENT instance object arrives for the same id without an
    // intervening release (a dispose+recreate that lands before the slot
    // settled). The old instance's context must be detached and the new
    // instance must get its own -- otherwise the stale 'webgl' slot wedges the
    // new instance onto DOM forever while the pool still reports it attached.
    const second = fakeInstance()
    pool.acquire('a', second)
    await flush()

    expect(detach).toHaveBeenCalledWith(first)
    expect(attach).toHaveBeenCalledTimes(2)
    expect(attach).toHaveBeenLastCalledWith(second, expect.any(Function))
    expect(pool.has('a')).toBe(true)
  })

  it('a swapped-in instance resets a retry-exhausted id and attaches fresh', async () => {
    const pool = makePool(2)
    const first = fakeInstance()
    pool.acquire('a', first)
    await flush()
    expect(attach).toHaveBeenCalledTimes(1)

    // Exhaust the context-loss retry budget for `first`: each loss triggers one
    // re-attach up to MAX_CONTEXT_LOSS_RETRIES, then the pool gives up and
    // leaves the id ineligible on DOM.
    lastOnContextLoss!()
    await flush()
    lastOnContextLoss!()
    await flush()
    lastOnContextLoss!()
    await flush()
    expect(pool.has('a')).toBe(false)
    const attachesAfterGivingUp = attach.mock.calls.length

    // A fresh instance for the same id (dispose+recreate, no release) must clear
    // the ineligible mark and earn its own attach attempt.
    const second = fakeInstance()
    pool.acquire('a', second)
    await flush()
    expect(attach).toHaveBeenCalledTimes(attachesAfterGivingUp + 1)
    expect(attach).toHaveBeenLastCalledWith(second, expect.any(Function))
    expect(pool.has('a')).toBe(true)
  })

  it('re-attaches the new instance when a swap lands mid-attach (pending slot not wedged)', async () => {
    const pool = makePool(2)
    const firstFonts = deferred()
    const first = fakeInstance({ fontsReady: firstFonts.promise })
    pool.acquire('a', first)
    await flush()
    // first is still 'pending' -- its fonts have not resolved.
    expect(attach).not.toHaveBeenCalled()

    // Swap in a new instance (whose fonts are ready) while the old attach is
    // still pending. The new instance must attach; the stale pending attach for
    // `first` must be invalidated (its later font resolution is a no-op).
    const second = fakeInstance()
    pool.acquire('a', second)
    await flush()
    expect(attach).toHaveBeenCalledTimes(1)
    expect(attach).toHaveBeenLastCalledWith(second, expect.any(Function))
    expect(pool.has('a')).toBe(true)

    // The abandoned first attach resolving late must not attach a second context.
    firstFonts.resolve()
    await flush()
    expect(attach).toHaveBeenCalledTimes(1)
  })

  it('defers attach until fonts are ready and skips it if released first', async () => {
    const pool = makePool(2)
    const fonts = deferred()
    pool.acquire('a', fakeInstance({ fontsReady: fonts.promise }))
    await flush()
    // Fonts have not loaded yet: no attach.
    expect(attach).not.toHaveBeenCalled()

    // The terminal goes off screen before fonts finish loading.
    pool.release('a')
    await flush()
    fonts.resolve()
    await flush()

    // The stale in-flight attach must not fire.
    expect(attach).not.toHaveBeenCalled()
    expect(pool.has('a')).toBe(false)
  })

  it('rasterizes with the font that wins a swap that lands mid-attach', async () => {
    const pool = makePool(2)
    const firstFont = deferred()
    const secondFont = deferred()
    const instance = fakeInstance({ fontsReady: firstFont.promise })
    pool.acquire('a', instance)
    await flush()
    expect(attach).not.toHaveBeenCalled()

    // A font-family swap replaces fontsReady while the attach still awaits.
    instance.fontsReady = secondFont.promise
    firstFont.resolve()
    await flush()
    // Attach must keep waiting for the *new* font, not proceed on the old one.
    expect(attach).not.toHaveBeenCalled()

    secondFont.resolve()
    await flush()
    expect(attach).toHaveBeenCalledTimes(1)
    expect(pool.has('a')).toBe(true)
  })

  it('release is a no-op for an unknown id', async () => {
    const pool = makePool(2)
    expect(() => pool.release('never-acquired')).not.toThrow()
    await flush()
    expect(pool.size()).toBe(0)
  })

  it('disposeAll detaches live contexts and clears all state', async () => {
    const pool = makePool(2)
    pool.acquire('a', fakeInstance())
    pool.acquire('b', fakeInstance())
    await flush()
    expect(pool.size()).toBe(2)

    pool.disposeAll()
    expect(pool.size()).toBe(0)
    expect(pool.has('a')).toBe(false)
    expect(detach).toHaveBeenCalledTimes(2)
  })
})
