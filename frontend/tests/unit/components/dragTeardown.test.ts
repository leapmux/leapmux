import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createDragTeardownHandle } from '~/components/shell/dragTeardown'

// Yields enough microtasks for Solid to flush the queued `createEffect`
// that the teardown handle uses to watch its structural keys. The handle
// uses `on(..., { defer: true })`, so the first scheduled run happens on
// the first key mutation post-mount.
async function flushEffects(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

interface Setup {
  setKeys: (keys: unknown[]) => unknown[]
  set: ReturnType<typeof createDragTeardownHandle>['set']
  dispose: () => void
}

/**
 * Build the handle inside a `createRoot` so its `createEffect` and
 * `onCleanup` have an owner. Setup must NOT be `async` — `createRoot`
 * disposes when the callback returns, and an async callback returns a
 * Promise immediately, so any awaited mutations would happen against a
 * disposed root.
 */
function setupHandle(initialKeys: unknown[]): Setup {
  let dispose!: () => void
  let set!: ReturnType<typeof createDragTeardownHandle>['set']
  const [keys, setKeys] = createSignal<unknown[]>(initialKeys)
  createRoot((d) => {
    dispose = d
    set = createDragTeardownHandle(keys).set
  })
  return { setKeys, set, dispose }
}

describe('createDragTeardownHandle', () => {
  it('does not call the teardown when the structural keys are unchanged', async () => {
    const teardown = vi.fn()
    const { set, dispose } = setupHandle([1, 'a'])
    set(teardown)
    await flushEffects()
    expect(teardown).not.toHaveBeenCalled()
    dispose()
  })

  it('calls the teardown when any of the structural keys changes', async () => {
    const teardown = vi.fn()
    const { setKeys, set, dispose } = setupHandle([1, 'a'])
    set(teardown)
    setKeys([2, 'a']) // first slot changed
    await flushEffects()
    expect(teardown).toHaveBeenCalledTimes(1)
    dispose()
  })

  it('does not fire on initial subscription (defer: true)', async () => {
    const teardown = vi.fn()
    const { set, dispose } = setupHandle([42])
    set(teardown)
    await flushEffects()
    expect(teardown).not.toHaveBeenCalled()
    dispose()
  })

  it('only calls the most recently set teardown on structural change', async () => {
    const first = vi.fn()
    const second = vi.fn()
    const { setKeys, set, dispose } = setupHandle([0])
    set(first)
    set(second)
    setKeys([1])
    await flushEffects()
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
    dispose()
  })

  it('set(null) drops the active reference so a later structural change is a no-op', async () => {
    const teardown = vi.fn()
    const { setKeys, set, dispose } = setupHandle([0])
    set(teardown)
    set(null) // drag finished naturally
    setKeys([1])
    await flushEffects()
    expect(teardown).not.toHaveBeenCalled()
    dispose()
  })

  it('owner disposal cancels the active teardown', () => {
    const teardown = vi.fn()
    const { set, dispose } = setupHandle([0])
    set(teardown)
    dispose()
    expect(teardown).toHaveBeenCalledTimes(1)
  })
})
