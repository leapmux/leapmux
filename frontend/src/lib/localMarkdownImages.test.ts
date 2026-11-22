import type { LocalMarkdownImageRequest } from './localMarkdownImages'
import { Code } from '@connectrpc/connect'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { resetStorageAccountForTests, setStorageAccountForTests } from '~/lib/browserStorage'
import { ChannelError } from '~/lib/channelError'
import { MAX_FILE_IMAGE_BYTES } from '~/lib/imageBlocks'
import { deferred, settle } from '~/test-support/async'
import { pngBytes } from '~/test-support/imageBytes'
import {
  _resetLocalMarkdownImageCacheForTest,
  acquireLocalMarkdownImage,
  clearLocalMarkdownImageCache,
  LOCAL_MARKDOWN_IMAGE_MAX_BYTES,
  LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES,
  peekLocalMarkdownImage,
  probeLocalMarkdownImageDimensions,
} from './localMarkdownImages'

const readWorkerFileMock = vi.hoisted(() => vi.fn())
const readWorkerFileHeadMock = vi.hoisted(() => vi.fn())
vi.mock('./readWorkerFile', () => ({
  readWorkerFile: readWorkerFileMock,
  readWorkerFileHead: readWorkerFileHeadMock,
}))

/** Minimal valid PNG header padded to `totalLength` bytes. */
function makePngBytes(width = 32, height = 16, totalLength = 64): Uint8Array {
  const bytes = new Uint8Array(Math.max(24, totalLength))
  bytes.set(pngBytes(width, height), 0)
  return bytes
}

function makeReq(overrides: Partial<LocalMarkdownImageRequest> = {}): LocalMarkdownImageRequest {
  return {
    workerId: 'w-1',
    rawSrc: './arch.png',
    baseDir: '/repo/docs',
    workingDir: '/repo',
    homeDir: '/home/alice',
    flavor: 'posix',
    ...overrides,
  }
}

let nextBlobId = 0
let createObjectUrlSpy: ReturnType<typeof vi.spyOn>
let revokeObjectUrlSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  nextBlobId = 0
  readWorkerFileMock.mockReset()
  readWorkerFileHeadMock.mockReset()
  createObjectUrlSpy = vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:test/${++nextBlobId}`)
  revokeObjectUrlSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  _resetLocalMarkdownImageCacheForTest()
})

afterEach(() => {
  _resetLocalMarkdownImageCacheForTest()
  vi.restoreAllMocks()
})

describe('peekLocalMarkdownImage', () => {
  it('returns undefined for empty workerId, unresolvable paths, and cache misses', () => {
    expect(peekLocalMarkdownImage(makeReq({ workerId: '' }))).toBeUndefined()
    expect(peekLocalMarkdownImage(makeReq({ rawSrc: 'https://evil.example/a.png' }))).toBeUndefined()
    expect(peekLocalMarkdownImage(makeReq({ baseDir: '', workingDir: '' }))).toBeUndefined()
    expect(peekLocalMarkdownImage(makeReq())).toBeUndefined()
    expect(readWorkerFileMock).not.toHaveBeenCalled()
  })

  it('returns a live lease synchronously on cache hit, protects against eviction, and refreshes LRU recency', async () => {
    const png = makePngBytes(120, 80)
    readWorkerFileMock.mockImplementation(async ({ path }: { path: string }) => ({
      path,
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    }))

    const firstReq = makeReq({ rawSrc: './img-0.png' })
    const initial = await acquireLocalMarkdownImage(firstReq)
    expect(initial.result.kind).toBe('ok')
    const firstUrl = initial.result.kind === 'ok'
      ? initial.result.url
      : ''
    initial.release()

    // Insert 63 more unreferenced entries so the cache is full (64 total).
    let secondUrl = ''
    for (let i = 1; i < LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES; i++) {
      const lease = await acquireLocalMarkdownImage(makeReq({ rawSrc: `./img-${i}.png` }))
      if (i === 1 && lease.result.kind === 'ok')
        secondUrl = lease.result.url
      lease.release()
    }

    // 1. Verify LRU recency refresh independently of refCount: peek and immediately
    // release img-0 so only recency refresh protects it from the next eviction.
    const peeked = peekLocalMarkdownImage(firstReq)
    expect(peeked).toBeDefined()
    expect(peeked!.result).toEqual({
      kind: 'ok',
      url: firstUrl,
      resolvedPath: '/repo/docs/img-0.png',
      width: 120,
      height: 80,
    })
    peeked!.release()

    // Adding one more entry evicts img-1 (now oldest), NOT img-0.
    const extra = await acquireLocalMarkdownImage(makeReq({ rawSrc: './img-extra.png' }))
    extra.release()

    expect(revokeObjectUrlSpy).toHaveBeenCalledTimes(1)
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(secondUrl)
    expect(revokeObjectUrlSpy).not.toHaveBeenCalledWith(firstUrl)

    // 2. Verify refCount protection independently of LRU recency: hold a peeked lease
    // while forcing eviction of all entries, then verify revocation waits for release().
    const held = peekLocalMarkdownImage(firstReq)
    expect(held).toBeDefined()
    clearLocalMarkdownImageCache()
    expect(revokeObjectUrlSpy).not.toHaveBeenCalledWith(firstUrl)
    held!.release()
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(firstUrl)
  })
})

describe('acquireLocalMarkdownImage', () => {
  it('returns an error without calling readWorkerFile when workerId is empty or candidates are empty', async () => {
    const noWorker = await acquireLocalMarkdownImage(makeReq({ workerId: '' }))
    expect(noWorker.result).toEqual({ kind: 'error', reason: 'Image path cannot be resolved' })

    const noBase = await acquireLocalMarkdownImage(makeReq({ baseDir: '', workingDir: '' }))
    expect(noBase.result).toEqual({ kind: 'error', reason: 'Image path cannot be resolved' })

    const remote = await acquireLocalMarkdownImage(makeReq({ rawSrc: 'https://example.com/a.png' }))
    expect(remote.result).toEqual({ kind: 'error', reason: 'Image path cannot be resolved' })

    expect(readWorkerFileMock).not.toHaveBeenCalled()
  })

  it('reads PNG bytes via readWorkerFile, sniffs dimensions, and returns a blob URL', async () => {
    const png = makePngBytes(640, 480)
    readWorkerFileMock.mockResolvedValue({
      path: '/repo/docs/arch.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })

    const lease = await acquireLocalMarkdownImage(makeReq())
    expect(lease.result).toEqual({
      kind: 'ok',
      url: 'blob:test/1',
      resolvedPath: '/repo/docs/arch.png',
      width: 640,
      height: 480,
    })
    expect(readWorkerFileMock).toHaveBeenCalledWith(expect.objectContaining({
      workerId: 'w-1',
      path: '/repo/docs/arch.png',
      maxBytes: MAX_FILE_IMAGE_BYTES,
    }))
    lease.release()
  })

  it('loads SVG images via extension MIME image/svg+xml', async () => {
    const svgBytes = new TextEncoder().encode('<svg xmlns="http://www.w3.org/2000/svg"></svg>')
    readWorkerFileMock.mockResolvedValue({
      path: '/repo/docs/icon.svg',
      content: svgBytes,
      totalSize: BigInt(svgBytes.byteLength),
      modTime: '',
    })

    const lease = await acquireLocalMarkdownImage(makeReq({ rawSrc: './icon.svg' }))
    expect(lease.result).toEqual({
      kind: 'ok',
      url: 'blob:test/1',
      resolvedPath: '/repo/docs/icon.svg',
    })
    const blobArg = createObjectUrlSpy.mock.calls[0]?.[0] as Blob
    expect(blobArg.type).toBe('image/svg+xml')
    lease.release()
  })

  it('deduplicates concurrent in-flight requests for the same workerId and candidates into one RPC', async () => {
    const png = makePngBytes(16, 16)
    const gate = deferred<{ path: string, content: Uint8Array, totalSize: bigint, modTime: string }>()
    readWorkerFileMock.mockReturnValue(gate.promise)

    const p1 = acquireLocalMarkdownImage(makeReq())
    const p2 = acquireLocalMarkdownImage(makeReq())
    expect(readWorkerFileMock).toHaveBeenCalledTimes(1)

    gate.resolve({
      path: '/repo/docs/arch.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })

    const [l1, l2] = await Promise.all([p1, p2])
    expect(l1.result).toEqual(l2.result)
    expect(createObjectUrlSpy).toHaveBeenCalledTimes(1)
    l1.release()
    l2.release()
  })

  it('falls back to repo-root candidate on NotFound and PermissionDenied, but not on transport errors', async () => {
    const png = makePngBytes(20, 20)

    readWorkerFileMock
      .mockRejectedValueOnce(new ChannelError('rpc', 'not found', { code: Code.NotFound }))
      .mockResolvedValueOnce({
        path: '/repo/logo.png',
        content: png,
        totalSize: BigInt(png.byteLength),
        modTime: '',
      })

    const l1 = await acquireLocalMarkdownImage(makeReq({ rawSrc: '/logo.png' }))
    expect(l1.result).toMatchObject({ kind: 'ok', resolvedPath: '/repo/logo.png' })
    expect(readWorkerFileMock.mock.calls.map(c => c[0].path)).toEqual(['/logo.png', '/repo/logo.png'])
    l1.release()

    readWorkerFileMock.mockReset()
    readWorkerFileMock
      .mockRejectedValueOnce(new ChannelError('rpc', 'denied', { code: Code.PermissionDenied }))
      .mockResolvedValueOnce({
        path: '/repo/logo2.png',
        content: png,
        totalSize: BigInt(png.byteLength),
        modTime: '',
      })

    const l2 = await acquireLocalMarkdownImage(makeReq({ rawSrc: '/logo2.png' }))
    expect(l2.result).toMatchObject({ kind: 'ok', resolvedPath: '/repo/logo2.png' })
    l2.release()

    readWorkerFileMock.mockReset()
    readWorkerFileMock.mockRejectedValue(new ChannelError('transport', 'socket closed'))

    const l3 = await acquireLocalMarkdownImage(makeReq({ rawSrc: '/logo3.png' }))
    expect(l3.result).toEqual({ kind: 'error', reason: 'Unable to load image' })
    expect(readWorkerFileMock).toHaveBeenCalledTimes(1)
  })

  it('does not cache transport or NotFound errors so retries succeed once available', async () => {
    const png = makePngBytes(10, 10)
    readWorkerFileMock.mockRejectedValueOnce(new ChannelError('transport', 'disconnected'))
    const err1 = await acquireLocalMarkdownImage(makeReq({ rawSrc: './retry.png' }))
    expect(err1.result.kind).toBe('error')

    readWorkerFileMock.mockRejectedValueOnce(new ChannelError('rpc', 'file not found', { code: Code.NotFound }))
    const err2 = await acquireLocalMarkdownImage(makeReq({ rawSrc: './retry.png' }))
    expect(err2.result).toEqual({ kind: 'error', reason: 'file not found' })

    readWorkerFileMock.mockResolvedValueOnce({
      path: '/repo/docs/retry.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })
    const ok = await acquireLocalMarkdownImage(makeReq({ rawSrc: './retry.png' }))
    expect(ok.result.kind).toBe('ok')
    expect(readWorkerFileMock).toHaveBeenCalledTimes(3)
    ok.release()
  })

  it('refuses oversize, empty, and non-image files and caches those refusals until cache clear', async () => {
    readWorkerFileMock.mockResolvedValueOnce({
      path: '/repo/docs/huge.png',
      content: new Uint8Array(0),
      totalSize: BigInt(MAX_FILE_IMAGE_BYTES + 1),
      modTime: '',
    })
    const huge = await acquireLocalMarkdownImage(makeReq({ rawSrc: './huge.png' }))
    expect(huge.result).toEqual({ kind: 'error', reason: 'The image file is too large to preview' })

    readWorkerFileMock.mockResolvedValueOnce({
      path: '/repo/docs/empty.png',
      content: new Uint8Array(0),
      totalSize: 0n,
      modTime: '',
    })
    const empty = await acquireLocalMarkdownImage(makeReq({ rawSrc: './empty.png' }))
    expect(empty.result).toEqual({ kind: 'error', reason: 'The image file is empty' })

    const plainText = new TextEncoder().encode('just plain text')
    readWorkerFileHeadMock.mockResolvedValueOnce({
      path: '/repo/docs/readme.txt',
      content: plainText,
      totalSize: BigInt(plainText.byteLength),
      modTime: '',
    })
    const unsupported = await acquireLocalMarkdownImage(makeReq({ rawSrc: './readme.txt' }))
    expect(unsupported.result).toEqual({ kind: 'error', reason: 'The image file format is not supported' })

    expect(peekLocalMarkdownImage(makeReq({ rawSrc: './huge.png' }))?.result).toEqual({
      kind: 'error',
      reason: 'The image file is too large to preview',
    })
    expect(peekLocalMarkdownImage(makeReq({ rawSrc: './empty.png' }))?.result).toEqual({
      kind: 'error',
      reason: 'The image file is empty',
    })
    expect(peekLocalMarkdownImage(makeReq({ rawSrc: './readme.txt' }))?.result).toEqual({
      kind: 'error',
      reason: 'The image file format is not supported',
    })
    expect(createObjectUrlSpy).not.toHaveBeenCalled()

    clearLocalMarkdownImageCache()
    expect(peekLocalMarkdownImage(makeReq({ rawSrc: './huge.png' }))).toBeUndefined()
  })

  it('evicts entries past the entry cap and byte cap while keeping actively leased URLs alive until release', async () => {
    // Entry-cap eviction with active lease on oldest entry.
    const smallPng = makePngBytes(10, 10, 64)
    readWorkerFileMock.mockImplementation(async ({ path }: { path: string }) => ({
      path,
      content: smallPng,
      totalSize: BigInt(smallPng.byteLength),
      modTime: '',
    }))

    const heldLeases: Array<{ url: string, release: () => void }> = []
    for (let i = 0; i < LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES + 1; i++) {
      const lease = await acquireLocalMarkdownImage(makeReq({ rawSrc: `./cap-${i}.png` }))
      expect(lease.result.kind).toBe('ok')
      heldLeases.push({
        url: lease.result.kind === 'ok'
          ? lease.result.url
          : '',
        release: lease.release,
      })
    }

    // Oldest entry was evicted from cache, but stays unrevoked while still leased.
    expect(revokeObjectUrlSpy).not.toHaveBeenCalled()
    expect(peekLocalMarkdownImage(makeReq({ rawSrc: './cap-0.png' }))).toBeUndefined()

    heldLeases[0]!.release()
    expect(revokeObjectUrlSpy).toHaveBeenCalledExactlyOnceWith(heldLeases[0]!.url)
    for (let i = 1; i < heldLeases.length; i++)
      heldLeases[i]!.release()

    // Byte-cap eviction: each image is 5 MiB, so 7 entries exceed 32 MiB.
    _resetLocalMarkdownImageCacheForTest()
    revokeObjectUrlSpy.mockClear()
    const bigPng = makePngBytes(100, 100, 5 * 1024 * 1024)
    readWorkerFileMock.mockImplementation(async ({ path }: { path: string }) => ({
      path,
      content: bigPng,
      totalSize: BigInt(bigPng.byteLength),
      modTime: '',
    }))

    const countToExceedBytes = Math.floor(LOCAL_MARKDOWN_IMAGE_MAX_BYTES / bigPng.byteLength) + 1
    const urls: string[] = []
    for (let i = 0; i < countToExceedBytes; i++) {
      const lease = await acquireLocalMarkdownImage(makeReq({ rawSrc: `./big-${i}.png` }))
      expect(lease.result.kind).toBe('ok')
      urls.push(lease.result.kind === 'ok'
        ? lease.result.url
        : '')
      lease.release()
    }
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(urls[0])
  })

  it('clears cache, bumps epoch, and isolates pre-clear in-flight probes on clear and account switch', async () => {
    const png = makePngBytes(32, 32)
    // Seed one unreferenced and one leased entry (each with its own resolved path).
    readWorkerFileMock.mockImplementation(async ({ path }: { path: string }) => ({
      path,
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    }))
    const unref = await acquireLocalMarkdownImage(makeReq({ rawSrc: './a.png' }))
    const unrefUrl = unref.result.kind === 'ok'
      ? unref.result.url
      : ''
    unref.release()

    const held = await acquireLocalMarkdownImage(makeReq({ rawSrc: './b.png' }))
    const heldUrl = held.result.kind === 'ok'
      ? held.result.url
      : ''

    // Start an in-flight probe before clear.
    const preClearGate = deferred<{ path: string, content: Uint8Array, totalSize: bigint, modTime: string }>()
    const postClearGate = deferred<{ path: string, content: Uint8Array, totalSize: bigint, modTime: string }>()
    readWorkerFileMock
      .mockReturnValueOnce(preClearGate.promise)
      .mockReturnValueOnce(postClearGate.promise)

    const preClearReq = makeReq({ rawSrc: './inflight.png' })
    const preClearPromise = acquireLocalMarkdownImage(preClearReq)

    clearLocalMarkdownImageCache()

    // Unreferenced entry revoked immediately; held entry waits for release.
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(unrefUrl)
    expect(revokeObjectUrlSpy).not.toHaveBeenCalledWith(heldUrl)
    held.release()
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(heldUrl)

    // Post-clear acquisition for the same key starts a brand-new probe (does not coalesce onto pre-clear).
    const postClearPromise = acquireLocalMarkdownImage(preClearReq)
    expect(readWorkerFileMock).toHaveBeenCalledTimes(4)

    // Pre-clear probe resolves: must NOT populate the new epoch's cache, and revokes once released.
    preClearGate.resolve({
      path: '/repo/docs/inflight.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })
    const preClearLease = await preClearPromise
    expect(preClearLease.result.kind).toBe('ok')
    const preClearUrl = preClearLease.result.kind === 'ok'
      ? preClearLease.result.url
      : ''
    expect(peekLocalMarkdownImage(preClearReq)).toBeUndefined()
    preClearLease.release()
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(preClearUrl)

    // Post-clear probe resolves and populates the cache.
    postClearGate.resolve({
      path: '/repo/docs/inflight.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })
    const postClearLease = await postClearPromise
    expect(postClearLease.result.kind).toBe('ok')
    const postClearUrl = postClearLease.result.kind === 'ok'
      ? postClearLease.result.url
      : ''
    const peekedPostClear = peekLocalMarkdownImage(preClearReq)
    expect(peekedPostClear).toBeDefined()
    peekedPostClear!.release()
    postClearLease.release()

    // Storage account change clears the cache automatically.
    setStorageAccountForTests('acct-next')
    expect(peekLocalMarkdownImage(preClearReq)).toBeUndefined()
    expect(revokeObjectUrlSpy).toHaveBeenCalledWith(postClearUrl)
    resetStorageAccountForTests()
  })

  it('handles concurrent waiter aborts without cancelling active waiters or leaking blob URLs', async () => {
    const png = makePngBytes(16, 16)
    const gate1 = deferred<{ path: string, content: Uint8Array, totalSize: bigint, modTime: string }>()
    let capturedSignal1: AbortSignal | undefined
    readWorkerFileMock.mockImplementationOnce(({ signal }: { signal?: AbortSignal }) => {
      capturedSignal1 = signal
      return gate1.promise
    })

    const c1 = new AbortController()
    const c2 = new AbortController()
    const p1 = acquireLocalMarkdownImage(makeReq({ rawSrc: './shared.png' }), c1.signal)
    const p2 = acquireLocalMarkdownImage(makeReq({ rawSrc: './shared.png' }), c2.signal)

    // Aborting waiter 1 must not abort the shared RPC while waiter 2 is still listening.
    c1.abort()
    const r1 = await p1
    expect(r1.result.kind).toBe('error')
    expect(capturedSignal1?.aborted).toBe(false)

    gate1.resolve({
      path: '/repo/docs/shared.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })
    const r2 = await p2
    expect(r2.result.kind).toBe('ok')
    r2.release()

    // Aborting BOTH waiters aborts the shared AbortSignal and creates zero blob URLs.
    createObjectUrlSpy.mockClear()
    const gate2 = deferred<{ path: string, content: Uint8Array, totalSize: bigint, modTime: string }>()
    let capturedSignal2: AbortSignal | undefined
    readWorkerFileMock.mockImplementationOnce(({ signal }: { signal?: AbortSignal }) => {
      capturedSignal2 = signal
      return gate2.promise
    })

    const a1 = new AbortController()
    const a2 = new AbortController()
    const w1 = acquireLocalMarkdownImage(makeReq({ rawSrc: './aborted-all.png' }), a1.signal)
    const w2 = acquireLocalMarkdownImage(makeReq({ rawSrc: './aborted-all.png' }), a2.signal)

    a1.abort()
    expect(capturedSignal2?.aborted).toBe(false)
    a2.abort()
    expect(capturedSignal2?.aborted).toBe(true)

    const [res1, res2] = await Promise.all([w1, w2])
    expect(res1.result.kind).toBe('error')
    expect(res2.result.kind).toBe('error')
    gate2.resolve({
      path: '/repo/docs/aborted-all.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })
    await settle()
    expect(createObjectUrlSpy).not.toHaveBeenCalled()
  })

  it('resolves waiters with an error instead of rejecting unhandled when URL.createObjectURL throws', async () => {
    const png = makePngBytes(16, 16)
    readWorkerFileMock.mockResolvedValueOnce({
      path: '/repo/docs/boom.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })
    createObjectUrlSpy.mockImplementationOnce(() => {
      throw new Error('blob quota exceeded')
    })

    const lease = await acquireLocalMarkdownImage(makeReq({ rawSrc: './boom.png' }))
    expect(lease.result).toEqual({ kind: 'error', reason: 'Unable to load image' })
    lease.release()
  })

  it('aliases different spellings that resolve to the same worker path into one cached blob', async () => {
    const png = makePngBytes(40, 20)
    readWorkerFileMock.mockResolvedValueOnce({
      path: '/repo/assets/arch.png',
      content: png,
      totalSize: BigInt(png.byteLength),
      modTime: '',
    })

    const l1 = await acquireLocalMarkdownImage(makeReq({ rawSrc: '../assets/arch.png' }))
    expect(l1.result.kind).toBe('ok')
    const url1 = l1.result.kind === 'ok'
      ? l1.result.url
      : ''

    // Root-relative `/assets/arch.png` (candidates: ['/assets/arch.png', '/repo/assets/arch.png'])
    // hits the resolved-path alias without issuing another worker RPC or Blob allocation.
    const peeked = peekLocalMarkdownImage(makeReq({ rawSrc: '/assets/arch.png' }))
    expect(peeked?.result).toEqual(l1.result)
    peeked?.release()

    const l2 = await acquireLocalMarkdownImage(makeReq({ rawSrc: '/assets/arch.png' }))
    expect(l2.result).toEqual(l1.result)
    expect(readWorkerFileMock).toHaveBeenCalledTimes(1)
    expect(createObjectUrlSpy).toHaveBeenCalledTimes(1)

    l1.release()
    l2.release()
    clearLocalMarkdownImageCache()
    expect(revokeObjectUrlSpy).toHaveBeenCalledExactlyOnceWith(url1)
  })

  it('sniffs non-image extensions via readWorkerFileHead before downloading the full file', async () => {
    const pdfHead = new TextEncoder().encode('%PDF-1.7\n')
    readWorkerFileHeadMock.mockResolvedValueOnce({
      path: '/repo/docs/manual.pdf',
      content: pdfHead,
      totalSize: 4_000_000n,
      modTime: '',
    })

    const res = await acquireLocalMarkdownImage(makeReq({ rawSrc: './manual.pdf' }))
    expect(res.result).toEqual({ kind: 'error', reason: 'The image file format is not supported' })
    expect(readWorkerFileHeadMock).toHaveBeenCalledTimes(1)
    expect(readWorkerFileMock).not.toHaveBeenCalled()
  })

  it('caches deterministic refusal outcomes across remounts until cache clear', async () => {
    readWorkerFileMock.mockResolvedValueOnce({
      path: '/repo/docs/oversized.png',
      content: new Uint8Array(0),
      totalSize: BigInt(MAX_FILE_IMAGE_BYTES + 1),
      modTime: '',
    })

    const first = await acquireLocalMarkdownImage(makeReq({ rawSrc: './oversized.png' }))
    expect(first.result).toEqual({ kind: 'error', reason: 'The image file is too large to preview' })

    // Second acquire and sync peek return the cached refusal without another worker RPC.
    const peeked = peekLocalMarkdownImage(makeReq({ rawSrc: './oversized.png' }))
    expect(peeked?.result).toEqual({ kind: 'error', reason: 'The image file is too large to preview' })
    const second = await acquireLocalMarkdownImage(makeReq({ rawSrc: './oversized.png' }))
    expect(second.result).toEqual({ kind: 'error', reason: 'The image file is too large to preview' })
    expect(readWorkerFileMock).toHaveBeenCalledTimes(1)

    clearLocalMarkdownImageCache()
    expect(peekLocalMarkdownImage(makeReq({ rawSrc: './oversized.png' }))).toBeUndefined()
  })
})

describe('probeLocalMarkdownImageDimensions', () => {
  it('reads dimensions from readWorkerFileHead without transferring full payloads or creating blobs', async () => {
    const pngHead = makePngBytes(800, 600, 64)
    readWorkerFileHeadMock.mockResolvedValueOnce({
      path: '/repo/docs/hero.png',
      content: pngHead,
      totalSize: 3_000_000n,
      modTime: '',
    })

    const dims = await probeLocalMarkdownImageDimensions(makeReq({ rawSrc: './hero.png' }))
    expect(dims).toEqual({ width: 800, height: 600 })
    expect(readWorkerFileHeadMock).toHaveBeenCalledTimes(1)
    expect(readWorkerFileMock).not.toHaveBeenCalled()
    expect(createObjectUrlSpy).not.toHaveBeenCalled()
    expect(peekLocalMarkdownImage(makeReq({ rawSrc: './hero.png' }))).toBeUndefined()

    // Second call serves from the dimension cache with zero extra RPCs.
    const cached = await probeLocalMarkdownImageDimensions(makeReq({ rawSrc: './hero.png' }))
    expect(cached).toEqual({ width: 800, height: 600 })
    expect(readWorkerFileHeadMock).toHaveBeenCalledTimes(1)
  })
})
