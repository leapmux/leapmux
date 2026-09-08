import type { UseFilesystemRootsArgs } from './useFilesystemRoots'
import { createRoot, createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useFilesystemRoots } from './useFilesystemRoots'

const listFilesystemRoots = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  listFilesystemRoots: (...args: unknown[]) => listFilesystemRoots(...args),
}))

beforeEach(() => {
  listFilesystemRoots.mockReset()
})

/** Flush the microtask queue so the hook's fetch settles. */
function tick() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

describe('useFilesystemRoots', () => {
  it('exposes the roots the worker reported', async () => {
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })

    await createRoot(async (dispose) => {
      const { roots } = useFilesystemRoots(() => ({ workerId: 'w1' }))
      await tick()
      expect(roots()).toEqual(['C:\\', 'D:\\'])
      expect(listFilesystemRoots).toHaveBeenCalledWith('w1')
      dispose()
    })
  })

  // The caller hides its drive menu on an empty list, which is the correct
  // reading of "this worker has not told us what it has".
  it('answers an empty list after a failed fetch', async () => {
    listFilesystemRoots.mockRejectedValue(new Error('offline'))
    const onError = vi.fn()

    await createRoot(async (dispose) => {
      const { roots } = useFilesystemRoots(() => ({ workerId: 'w1' }), onError)
      await tick()
      expect(roots()).toEqual([])
      expect(onError).toHaveBeenCalled()
      dispose()
    })
  })

  // A POSIX caller passes null: one root is already known, so the round trip
  // would buy nothing.
  it('does not fetch while the source is null', async () => {
    await createRoot(async (dispose) => {
      const [args] = createSignal<UseFilesystemRootsArgs | null>(null)
      const { roots } = useFilesystemRoots(args)
      await tick()
      expect(listFilesystemRoots).not.toHaveBeenCalled()
      expect(roots()).toEqual([])
      dispose()
    })
  })

  it('fetches once the source becomes non-null', async () => {
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\'] })

    await createRoot(async (dispose) => {
      const [args, setArgs] = createSignal<UseFilesystemRootsArgs | null>(null)
      const { roots } = useFilesystemRoots(args)
      await tick()

      setArgs({ workerId: 'w1' })
      await tick()

      expect(roots()).toEqual(['C:\\'])
      dispose()
    })
  })
})
