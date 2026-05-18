import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const readFileImpl = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  readFile: (...args: unknown[]) => readFileImpl(...args),
}))

const { downloadFileFromWorker } = await import('~/lib/fileDownload')

function readResp(content: Uint8Array, totalSize?: number) {
  return {
    content,
    totalSize: BigInt(totalSize ?? content.length),
  }
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
    let offset = 0
    readFileImpl.mockImplementation(async (_workerId, req) => {
      const reqOffset = Number(req.offset)
      const reqLimit = Number(req.limit)
      expect(reqOffset).toBe(offset)
      expect(reqLimit).toBe(CHUNK)
      const remaining = total - offset
      const size = Math.min(reqLimit, remaining)
      const buf = new Uint8Array(size)
      offset += size
      return readResp(buf, total)
    })

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
})
