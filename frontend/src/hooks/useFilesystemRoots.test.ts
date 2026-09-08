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
      const { roots } = useFilesystemRoots(() => 'w1', () => true)
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
      const { roots } = useFilesystemRoots(() => 'w1', () => true, onError)
      await tick()
      expect(roots()).toEqual([])
      expect(onError).toHaveBeenCalled()
      dispose()
    })
  })

  // A POSIX caller shuts the gate: one root is already known, so the round
  // trip would buy nothing.
  it('does not fetch while the gate is shut', async () => {
    await createRoot(async (dispose) => {
      const { roots } = useFilesystemRoots(() => 'w1', () => false)
      await tick()
      expect(listFilesystemRoots).not.toHaveBeenCalled()
      expect(roots()).toEqual([])
      dispose()
    })
  })

  it('does not fetch while there is no worker', async () => {
    await createRoot(async (dispose) => {
      const { roots } = useFilesystemRoots(() => '', () => true)
      await tick()
      expect(listFilesystemRoots).not.toHaveBeenCalled()
      expect(roots()).toEqual([])
      dispose()
    })
  })

  it('fetches once the gate opens', async () => {
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\'] })

    await createRoot(async (dispose) => {
      const [open, setOpen] = createSignal(false)
      const { roots } = useFilesystemRoots(() => 'w1', open)
      await tick()
      expect(listFilesystemRoots).not.toHaveBeenCalled()

      setOpen(true)
      await tick()

      expect(roots()).toEqual(['C:\\'])
      dispose()
    })
  })

  /**
   * The defect the workerId/gate split exists to remove.
   *
   * The picker's gate IS a property of the worker -- "this one runs Windows"
   * -- so it closes on the same tick the worker changes. A source that folded
   * the two together had no transition left to clear on, and the picker went
   * on offering worker A's `C:\` and `D:\` for a Linux worker B. Choosing one
   * then set the working directory to a path that does not exist there.
   */
  it('clears the previous worker drives when the new worker shuts the gate', async () => {
    listFilesystemRoots.mockResolvedValue({ roots: ['C:\\', 'D:\\'] })

    await createRoot(async (dispose) => {
      const [workerId, setWorkerId] = createSignal('win-1')
      const { roots } = useFilesystemRoots(workerId, () => workerId() === 'win-1')
      await tick()
      expect(roots()).toEqual(['C:\\', 'D:\\'])

      setWorkerId('linux-1')
      await tick()

      expect(roots()).toEqual([])
      expect(listFilesystemRoots).toHaveBeenCalledTimes(1)
      dispose()
    })
  })

  it('clears and re-fetches when the worker changes with the gate open', async () => {
    listFilesystemRoots
      .mockResolvedValueOnce({ roots: ['C:\\'] })
      .mockResolvedValueOnce({ roots: ['E:\\', 'F:\\'] })

    await createRoot(async (dispose) => {
      const [workerId, setWorkerId] = createSignal('w1')
      const { roots } = useFilesystemRoots(workerId, () => true)
      await tick()
      expect(roots()).toEqual(['C:\\'])

      setWorkerId('w2')
      await tick()

      expect(roots()).toEqual(['E:\\', 'F:\\'])
      expect(listFilesystemRoots).toHaveBeenCalledTimes(2)
      dispose()
    })
  })

  // The picker's Refresh button and its shortcut both route here. Without it a
  // transient failure would leave the drive menu empty until the user picked
  // another worker and came back.
  it('re-fetches on refresh and replaces the list', async () => {
    listFilesystemRoots
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({ roots: ['C:\\', 'D:\\'] })

    await createRoot(async (dispose) => {
      const { roots, refresh } = useFilesystemRoots(() => 'w1', () => true)
      await tick()
      expect(roots()).toEqual([])

      await refresh()
      await tick()

      expect(roots()).toEqual(['C:\\', 'D:\\'])
      expect(listFilesystemRoots).toHaveBeenCalledTimes(2)
      dispose()
    })
  })

  it('reports loading across one fetch', async () => {
    let resolveFetch: ((v: { roots: string[] }) => void) | undefined
    listFilesystemRoots.mockImplementation(() => new Promise((r) => {
      resolveFetch = r
    }))

    await createRoot(async (dispose) => {
      const { loading, roots } = useFilesystemRoots(() => 'w1', () => true)
      await tick()
      expect(loading()).toBe(true)

      resolveFetch?.({ roots: ['C:\\'] })
      await tick()

      expect(loading()).toBe(false)
      expect(roots()).toEqual(['C:\\'])
      dispose()
    })
  })
})
