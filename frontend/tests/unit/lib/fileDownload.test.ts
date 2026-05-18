import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readResp } from '../helpers/workerRpcMocks'

const readFileImpl = vi.fn()
const fileSaveOpenImpl = vi.fn()
const fileSaveOpenDialogImpl = vi.fn()
const fileSaveWriteImpl = vi.fn()
const fileSaveCommitImpl = vi.fn()
const fileSaveAbortImpl = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  readFile: (...args: unknown[]) => readFileImpl(...args),
}))

vi.mock('~/api/platformBridge', () => ({
  platformBridge: {
    fileSaveOpen: (...args: unknown[]) => fileSaveOpenImpl(...args),
    fileSaveOpenDialog: (...args: unknown[]) => fileSaveOpenDialogImpl(...args),
    fileSaveWrite: (...args: unknown[]) => fileSaveWriteImpl(...args),
    fileSaveCommit: (...args: unknown[]) => fileSaveCommitImpl(...args),
    fileSaveAbort: (...args: unknown[]) => fileSaveAbortImpl(...args),
  },
}))

const { downloadFileFromWorker, saveFileAs, saveFileToDownloads } = await import('~/lib/fileDownload')

/**
 * Reset every save-stream mock and default the side-effect-free ones
 * (write / commit / abort) to a resolved no-op. Shared by the
 * SAVE_FN_CASES driver and the dialog-specific describe so they stay
 * in sync.
 */
function resetSaveMocks() {
  readFileImpl.mockReset()
  fileSaveOpenImpl.mockReset()
  fileSaveOpenDialogImpl.mockReset()
  fileSaveWriteImpl.mockReset()
  fileSaveCommitImpl.mockReset()
  fileSaveAbortImpl.mockReset()
  fileSaveWriteImpl.mockResolvedValue(undefined)
  fileSaveCommitImpl.mockResolvedValue(undefined)
  fileSaveAbortImpl.mockResolvedValue(undefined)
}

interface AnchorSpy {
  clickSpy: ReturnType<typeof vi.fn>
  /** Captured value of `<a download="...">` when set. */
  downloadName: { value: string | undefined }
  restore: () => void
}

/**
 * Replace `document.createElement('a')` with a stub anchor whose
 * `click` is captured. Tests assert on clickSpy / downloadName and call
 * `restore()` (or rely on `afterEach`).
 */
function spyAnchor(): AnchorSpy {
  const clickSpy = vi.fn()
  const downloadName: { value: string | undefined } = { value: undefined }
  const origCreate = document.createElement.bind(document)
  const createSpy = vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
    const el = origCreate(tag) as HTMLAnchorElement
    if (tag === 'a') {
      el.click = clickSpy
      Object.defineProperty(el, 'download', {
        configurable: true,
        set(v: string) { downloadName.value = v },
        get() { return downloadName.value },
      })
    }
    return el
  })
  return { clickSpy, downloadName, restore: () => createSpy.mockRestore() }
}

/**
 * Configure `readFile` to serve `total` bytes back in 1 MiB chunks,
 * advancing the in-memory offset between calls and asserting each
 * request's offset/limit matches the production chunk size.
 */
function mockChunkedRead(total: number) {
  let offset = 0
  readFileImpl.mockImplementation(async (_workerId, req) => {
    const reqOffset = Number(req.offset)
    const reqLimit = Number(req.limit)
    expect(reqOffset).toBe(offset)
    expect(reqLimit).toBe(1 << 20) // production constant
    const size = Math.min(reqLimit, total - offset)
    const buf = new Uint8Array(size)
    offset += size
    return readResp(buf, total)
  })
}

describe('downloadFileFromWorker', () => {
  let originalCreate: typeof URL.createObjectURL
  let originalRevoke: typeof URL.revokeObjectURL
  const createUrlSpy = vi.fn(() => 'blob:mock-url')
  const revokeUrlSpy = vi.fn()
  let anchor: AnchorSpy

  beforeEach(() => {
    readFileImpl.mockReset()
    createUrlSpy.mockClear()
    revokeUrlSpy.mockClear()
    originalCreate = URL.createObjectURL
    originalRevoke = URL.revokeObjectURL
    URL.createObjectURL = createUrlSpy as unknown as typeof URL.createObjectURL
    URL.revokeObjectURL = revokeUrlSpy as unknown as typeof URL.revokeObjectURL
    vi.useFakeTimers()
    anchor = spyAnchor()
  })

  afterEach(() => {
    URL.createObjectURL = originalCreate
    URL.revokeObjectURL = originalRevoke
    vi.useRealTimers()
    anchor.restore()
  })

  it('streams a small file in a single chunk and triggers an anchor download', async () => {
    const bytes = new TextEncoder().encode('hello world')
    readFileImpl.mockResolvedValue(readResp(bytes))

    await downloadFileFromWorker('w1', '/repo/hello.txt')

    expect(readFileImpl).toHaveBeenCalledTimes(1)
    expect(readFileImpl).toHaveBeenCalledWith('w1', expect.objectContaining({
      workerId: 'w1',
      path: '/repo/hello.txt',
      offset: 0n,
    }))
    expect(anchor.clickSpy).toHaveBeenCalledTimes(1)
    expect(createUrlSpy).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(1100)
    expect(revokeUrlSpy).toHaveBeenCalledWith('blob:mock-url')
  })

  it('streams a large file in multiple 1 MiB chunks until totalSize is reached', async () => {
    const CHUNK = 1 << 20
    const total = CHUNK * 3 + 7
    mockChunkedRead(total)

    await downloadFileFromWorker('w1', '/repo/big.bin')

    // 3 MB + 7 bytes → 4 chunks
    expect(readFileImpl).toHaveBeenCalledTimes(4)
    expect(anchor.clickSpy).toHaveBeenCalledTimes(1)
  })

  it('stops early when readFile returns zero bytes even if totalSize disagrees', async () => {
    readFileImpl.mockResolvedValueOnce(readResp(new Uint8Array(4096), 10_000))
    readFileImpl.mockResolvedValueOnce(readResp(new Uint8Array(0), 10_000))

    await downloadFileFromWorker('w1', '/repo/short.bin')

    expect(readFileImpl).toHaveBeenCalledTimes(2)
    expect(anchor.clickSpy).toHaveBeenCalledTimes(1)
  })

  it('uses basename(filePath) as the download filename with detected flavor', async () => {
    readFileImpl.mockResolvedValue(readResp(new Uint8Array([1, 2, 3, 4])))

    await downloadFileFromWorker('w1', 'C:\\Users\\alice\\file.bin')
    expect(anchor.downloadName.value).toBe('file.bin')

    anchor.downloadName.value = undefined
    await downloadFileFromWorker('w1', '/repo/sub/dir/photo.png')
    expect(anchor.downloadName.value).toBe('photo.png')
  })

  it('propagates readFile errors and never leaks an object URL', async () => {
    readFileImpl.mockRejectedValue(new Error('boom'))

    await expect(downloadFileFromWorker('w1', '/repo/x.bin')).rejects.toThrow('boom')
    expect(anchor.clickSpy).not.toHaveBeenCalled()
    expect(createUrlSpy).not.toHaveBeenCalled()
    expect(revokeUrlSpy).not.toHaveBeenCalled()
  })

  it('fires onProgress with the first and final chunks (throttled in between)', async () => {
    // Three chunks fire within microseconds → the shared 100 ms throttle
    // keeps only the first emit (lastEmittedAt starts at -Infinity) and
    // the isFinal emit; chunk 2 is coalesced.
    const CHUNK = 1 << 20
    const total = CHUNK * 2 + 100
    mockChunkedRead(total)
    const onProgress = vi.fn()

    await downloadFileFromWorker('w1', '/repo/big.bin', undefined, onProgress)

    expect(onProgress).toHaveBeenCalledTimes(2)
    expect(onProgress.mock.calls.map(c => c[0])).toEqual([CHUNK, total])
    expect(onProgress.mock.calls.every(c => c[1] === total)).toBe(true)
  })

  it('fires a final onProgress at received-bytes when the stream truncates below totalSize', async () => {
    // Worker advertises 10_000 bytes but yields 4096 then an empty
    // chunk. Without the post-loop force-emit, the throttle never
    // sees `received >= total` and the spinner is stuck at the
    // intermediate percent. The dedup guard keeps the count tight.
    readFileImpl.mockResolvedValueOnce(readResp(new Uint8Array(4096), 10_000))
    readFileImpl.mockResolvedValueOnce(readResp(new Uint8Array(0), 10_000))
    const onProgress = vi.fn()

    await downloadFileFromWorker('w1', '/repo/short.bin', undefined, onProgress)

    const lastCall = onProgress.mock.calls[onProgress.mock.calls.length - 1]
    expect(lastCall[0]).toBe(4096)
    expect(lastCall[1]).toBe(4096)
  })

  it('emits intermediate onProgress when ≥ 100 ms elapse between chunks', async () => {
    const CHUNK = 1 << 20
    const total = CHUNK * 3
    mockChunkedRead(total)
    const onProgress = vi.fn()
    let nowMs = 1000
    const nowSpy = vi.spyOn(performance, 'now').mockImplementation(() => {
      const v = nowMs
      nowMs += 150
      return v
    })
    try {
      await downloadFileFromWorker('w1', '/repo/big.bin', undefined, onProgress)
      expect(onProgress.mock.calls.map(c => c[0])).toEqual([CHUNK, CHUNK * 2, total])
    }
    finally {
      nowSpy.mockRestore()
    }
  })
})

// `saveFileToDownloads` and `saveFileAs` differ only in which
// `platformBridge` open call they use; the chunk-streaming and handle
// finalization that follows is shared (`pumpChunksToHandle`). Run the
// shared behaviors against both fns from one driver so a regression in
// either entry point fails both rows. Per-fn divergences (the save-as
// dialog ordering and cancellation) live in their own describe below.
interface SaveFnCase {
  label: string
  /** The exported `fileDownload` function under test. */
  fn: (workerId: string, path: string, flavor?: undefined, onProgress?: (received: number, total: number) => void) => Promise<string | null>
  /** The `platformBridge` open mock the fn delegates to. */
  open: typeof fileSaveOpenImpl
  /** Handle id and final path the open mock resolves to. */
  id: number
  finalPath: string
}

const SAVE_FN_CASES: SaveFnCase[] = [
  { label: 'saveFileToDownloads', fn: saveFileToDownloads, open: fileSaveOpenImpl, id: 7, finalPath: '/home/alice/Downloads/big.bin' },
  { label: 'saveFileAs', fn: saveFileAs, open: fileSaveOpenDialogImpl, id: 22, finalPath: '/home/alice/saved.bin' },
]

for (const c of SAVE_FN_CASES) {
  describe(`${c.label} (shared streaming flow)`, () => {
    beforeEach(() => {
      resetSaveMocks()
      c.open.mockResolvedValue({ id: c.id, path: c.finalPath })
    })

    it('opens a destination then writes each worker chunk in order', async () => {
      const CHUNK = 1 << 20
      const total = CHUNK * 2 + 7
      mockChunkedRead(total)

      const path = await c.fn('w1', '/repo/big.bin')

      expect(path).toBe(c.finalPath)
      expect(c.open).toHaveBeenCalledWith('big.bin')
      expect(fileSaveWriteImpl).toHaveBeenCalledTimes(3)
      for (const call of fileSaveWriteImpl.mock.calls)
        expect(call[0]).toBe(c.id)
      expect(fileSaveCommitImpl).toHaveBeenCalledWith(c.id)
      expect(fileSaveAbortImpl).not.toHaveBeenCalled()
    })

    it('aborts the handle and propagates a readFile error', async () => {
      readFileImpl.mockRejectedValue(new Error('worker exploded'))

      await expect(c.fn('w1', '/repo/big.bin')).rejects.toThrow('worker exploded')
      expect(fileSaveWriteImpl).not.toHaveBeenCalled()
      expect(fileSaveAbortImpl).toHaveBeenCalledWith(c.id)
      expect(fileSaveCommitImpl).not.toHaveBeenCalled()
    })

    it('aborts and propagates a mid-stream readFile error', async () => {
      const CHUNK = 1 << 20
      let calls = 0
      readFileImpl.mockImplementation(async () => {
        calls += 1
        if (calls === 1)
          return readResp(new Uint8Array(CHUNK), CHUNK * 3)
        throw new Error('worker died')
      })

      await expect(c.fn('w1', '/repo/big.bin')).rejects.toThrow('worker died')
      expect(fileSaveWriteImpl).toHaveBeenCalledTimes(1)
      expect(fileSaveAbortImpl).toHaveBeenCalledWith(c.id)
      expect(fileSaveCommitImpl).not.toHaveBeenCalled()
    })

    it('fires onProgress after each successful chunk write', async () => {
      const CHUNK = 1 << 20
      mockChunkedRead(CHUNK * 2)
      const onProgress = vi.fn()

      await c.fn('w1', '/repo/big.bin', undefined, onProgress)

      expect(onProgress).toHaveBeenCalledTimes(2)
      expect(onProgress.mock.calls[0]).toEqual([CHUNK, CHUNK * 2])
      expect(onProgress.mock.calls[1]).toEqual([CHUNK * 2, CHUNK * 2])
    })

    it('forwards a zero-offset full-buffer view straight through without copying', async () => {
      const bytes = new Uint8Array(64)
      bytes[0] = 0xAB
      bytes[63] = 0xCD
      readFileImpl.mockResolvedValue(readResp(bytes))

      await c.fn('w1', '/repo/x.bin')

      expect(fileSaveWriteImpl).toHaveBeenCalledTimes(1)
      const written = fileSaveWriteImpl.mock.calls[0][1] as Uint8Array
      expect(written.buffer).toBe(bytes.buffer)
    })

    it('copies a sub-slice view so callers see an isolated ArrayBuffer', async () => {
      const big = new ArrayBuffer(256)
      const slice = new Uint8Array(big, 64, 128)
      slice[0] = 0xAB
      slice[127] = 0xCD
      readFileImpl.mockResolvedValue(readResp(slice))

      await c.fn('w1', '/repo/x.bin')

      expect(fileSaveWriteImpl).toHaveBeenCalledTimes(1)
      const written = fileSaveWriteImpl.mock.calls[0][1] as Uint8Array
      expect(written.buffer).not.toBe(big)
      expect(written.byteOffset).toBe(0)
      expect(written.byteLength).toBe(128)
      expect(written[0]).toBe(0xAB)
      expect(written[127]).toBe(0xCD)
    })

    it('pipelines the next worker read with the in-flight fileSaveWrite', async () => {
      // Hold each fileSaveWrite open until the test releases it. The
      // first write being in flight should NOT block the next worker
      // read — a strictly sequential pump would have exactly 1
      // readFile call before write 1 resolves, but pipelining means
      // at least one more read is already in flight.
      const CHUNK = 1 << 20
      mockChunkedRead(CHUNK * 3)
      const writeReleases: Array<() => void> = []
      fileSaveWriteImpl.mockImplementation(() => new Promise<void>((resolve) => {
        writeReleases.push(resolve)
      }))

      const done = c.fn('w1', '/repo/big.bin')
      await vi.waitFor(() => expect(writeReleases.length).toBe(1))
      expect(readFileImpl.mock.calls.length).toBeGreaterThan(1)
      writeReleases[0]()
      await vi.waitFor(() => expect(writeReleases.length).toBe(2))
      writeReleases[1]()
      await vi.waitFor(() => expect(writeReleases.length).toBe(3))
      writeReleases[2]()
      await done
      expect(fileSaveCommitImpl).toHaveBeenCalledWith(c.id)
    })

    it('throttles intermediate onProgress to ~100 ms but always emits the first and final chunks', async () => {
      // 5 chunks; each fileSaveWrite advances the simulated clock 60 ms.
      // Chunks 1 (first) and 5 (final) always emit. Chunk 2 lands 60 ms
      // after the chunk-1 emit (< 100, skipped); chunk 3 lands 120 ms
      // after (>= 100, emits); chunk 4 lands 60 ms after the chunk-3
      // emit (< 100, skipped).
      const CHUNK = 1 << 20
      mockChunkedRead(CHUNK * 5)
      const onProgress = vi.fn()
      let nowMs = 1000
      const nowSpy = vi.spyOn(performance, 'now').mockImplementation(() => nowMs)
      fileSaveWriteImpl.mockImplementation(async () => {
        nowMs += 60
      })
      try {
        await c.fn('w1', '/repo/big.bin', undefined, onProgress)

        const receivedAt = onProgress.mock.calls.map(c => c[0] as number)
        expect(receivedAt).toEqual([CHUNK, CHUNK * 3, CHUNK * 5])
      }
      finally {
        nowSpy.mockRestore()
      }
    })
  })
}

describe('saveFileAs (dialog-specific)', () => {
  beforeEach(() => {
    resetSaveMocks()
  })

  it('shows the save-as dialog before any worker readFile', async () => {
    // The order check: track first call across both spies.
    const order: string[] = []
    fileSaveOpenDialogImpl.mockImplementation(async () => {
      order.push('dialog')
      return { id: 11, path: '/home/alice/Documents/saved.bin' }
    })
    readFileImpl.mockImplementation(async () => {
      order.push('read')
      return readResp(new Uint8Array(8))
    })

    await saveFileAs('w1', '/repo/big.bin')

    expect(order[0]).toBe('dialog')
    expect(order).toContain('read')
    expect(fileSaveOpenDialogImpl).toHaveBeenCalledWith('big.bin')
  })

  it('returns null without ever calling readFile when the dialog is cancelled', async () => {
    fileSaveOpenDialogImpl.mockResolvedValue(null)

    const result = await saveFileAs('w1', '/repo/big.bin')

    expect(result).toBeNull()
    expect(readFileImpl).not.toHaveBeenCalled()
    expect(fileSaveWriteImpl).not.toHaveBeenCalled()
    expect(fileSaveCommitImpl).not.toHaveBeenCalled()
    expect(fileSaveAbortImpl).not.toHaveBeenCalled()
  })
})
