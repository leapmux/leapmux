import type { ImageDimensions } from './imageDimensions'
import type { LocalLinkResolveOptions } from './localLinks'
import { onStorageAccountChange } from './browserStorage'
import { getImageMimeType, sniffImageMime } from './fileType'
import { fileImageRefusal, MAX_FILE_IMAGE_BYTES, RENDERABLE_IMAGE_MIME_TYPES } from './imageBlocks'
import { imageHeaderBytesNeeded, sniffImageDimensionsFromBytes } from './imageDimensions'
import { resolveLocalLinkCandidates } from './localLinks'
import { isLocalPathFallbackRpcError, localPathRpcErrorMessage } from './localPathRpc'
import { lruGet, lruSet } from './mapLru'
import { readWorkerFile, readWorkerFileHead } from './readWorkerFile'

/** Maximum number of cached local Markdown image blob entries. */
export const LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES = 64
/** Maximum total decoded byte footprint across cached local Markdown image entries. */
export const LOCAL_MARKDOWN_IMAGE_MAX_BYTES = 32 * 1024 * 1024
/** Initial header read size used for MIME sniffing on non-image extensions and premeasure sizing. */
const LOCAL_IMAGE_HEAD_BYTES = 4096

export interface LocalMarkdownImageRequest extends LocalLinkResolveOptions {
  workerId: string
  rawSrc: string
}

export type LocalMarkdownImageResult
  = | {
    kind: 'ok'
    url: string
    resolvedPath: string
    width?: number
    height?: number
  }
  | {
    kind: 'error'
    reason: string
  }

export interface LocalMarkdownImageLease {
  result: LocalMarkdownImageResult
  release: () => void
}

interface CacheEntry {
  key: string
  workerId: string
  result: Extract<LocalMarkdownImageResult, { kind: 'ok' }>
  bytes: number
  refCount: number
  evicted: boolean
  revoked: boolean
}

interface Waiter {
  resolve: (lease: LocalMarkdownImageLease) => void
  detach: () => void
}

interface InFlightEntry {
  epoch: number
  controller: AbortController
  waiters: Set<Waiter>
}

function noopRelease(): void {}

const cache = new Map<string, CacheEntry>()
const byResolvedPath = new Map<string, CacheEntry>()
const refusalCache = new Map<string, Extract<LocalMarkdownImageResult, { kind: 'error' }>>()
const dimensionCache = new Map<string, ImageDimensions | null>()
const inFlight = new Map<string, InFlightEntry>()
let totalCachedBytes = 0
let cacheEpoch = 0

function resolvedPathIndexKey(workerId: string, resolvedPath: string): string {
  return `${workerId}\0${resolvedPath}`
}

function cacheKeyFor(req: LocalMarkdownImageRequest): { key: string, candidates: string[] } | undefined {
  if (!req.workerId)
    return undefined
  const candidates = resolveLocalLinkCandidates(req.rawSrc, req)
  if (candidates.length === 0)
    return undefined
  return {
    key: JSON.stringify([req.workerId, ...candidates]),
    candidates,
  }
}

function revokeEntry(entry: CacheEntry): void {
  if (entry.revoked)
    return
  entry.revoked = true
  URL.revokeObjectURL(entry.result.url)
}

function createLease(entry: CacheEntry): LocalMarkdownImageLease {
  entry.refCount++
  let released = false
  return {
    result: entry.result,
    release: () => {
      if (released)
        return
      released = true
      entry.refCount--
      if (entry.refCount === 0 && entry.evicted)
        revokeEntry(entry)
    },
  }
}

function findCachedEntry(workerId: string, key: string, candidates: readonly string[]): CacheEntry | undefined {
  const direct = lruGet(cache, key)
  if (direct)
    return direct
  for (const candidate of candidates) {
    const aliased = byResolvedPath.get(resolvedPathIndexKey(workerId, candidate))
    if (aliased && !aliased.evicted) {
      lruGet(cache, aliased.key)
      return aliased
    }
  }
  return undefined
}

function evictEntry(entry: CacheEntry): void {
  cache.delete(entry.key)
  const aliasKey = resolvedPathIndexKey(entry.workerId, entry.result.resolvedPath)
  if (byResolvedPath.get(aliasKey) === entry)
    byResolvedPath.delete(aliasKey)
  totalCachedBytes -= entry.bytes
  entry.evicted = true
  if (entry.refCount === 0)
    revokeEntry(entry)
}

function evictIfNeeded(protectedKey: string): void {
  const overCap = () =>
    cache.size > LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES || totalCachedBytes > LOCAL_MARKDOWN_IMAGE_MAX_BYTES

  if (!overCap())
    return

  // Pass 1: evict oldest unreferenced entries first so active leases stay cached when possible.
  for (const [key, entry] of cache) {
    if (!overCap())
      return
    if (key === protectedKey || entry.refCount > 0)
      continue
    evictEntry(entry)
  }

  // Pass 2: if still over cap, evict oldest entries regardless of refCount;
  // active leases keep their blob URL alive until the last lease calls release().
  for (const [key, entry] of cache) {
    if (!overCap())
      return
    if (key === protectedKey)
      continue
    evictEntry(entry)
  }
}

type ProbeOutcome
  = | {
    kind: 'ok'
    bytes: Uint8Array
    mime: string
    resolvedPath: string
    width?: number
    height?: number
  }
  | {
    kind: 'error'
    reason: string
    cacheable: boolean
  }

async function probeCandidates(
  workerId: string,
  candidates: readonly string[],
  signal: AbortSignal,
): Promise<ProbeOutcome> {
  let lastError: unknown
  for (const path of candidates) {
    if (signal.aborted)
      return { kind: 'error', reason: 'Image load aborted', cacheable: false }

    try {
      // For files without a known renderable image extension, inspect the leading
      // header first so non-image payloads (e.g. multi-MiB PDFs or videos) are
      // refused without transferring up to MAX_FILE_IMAGE_BYTES.
      let resp
      if (!RENDERABLE_IMAGE_MIME_TYPES.has(getImageMimeType(path))) {
        const head = await readWorkerFileHead({
          workerId,
          path,
          bytes: LOCAL_IMAGE_HEAD_BYTES,
          signal,
        })
        const headPath = head.path || path
        const headMime = sniffImageMime(headPath, head.content)
        const headRefusal = fileImageRefusal(head.totalSize, headMime)
        if (headRefusal !== undefined)
          return { kind: 'error', reason: headRefusal, cacheable: true }
        resp = BigInt(head.content.length) === head.totalSize
          ? head
          : await readWorkerFile({
              workerId,
              path,
              maxBytes: MAX_FILE_IMAGE_BYTES,
              signal,
            })
      }
      else {
        resp = await readWorkerFile({
          workerId,
          path,
          maxBytes: MAX_FILE_IMAGE_BYTES,
          signal,
        })
      }

      const bytes = resp.content
      const resolvedPath = resp.path || path
      const mime = sniffImageMime(resolvedPath, bytes)
      const refusal = fileImageRefusal(resp.totalSize, mime)
      if (refusal !== undefined)
        return { kind: 'error', reason: refusal, cacheable: true }
      const dims = sniffImageDimensionsFromBytes(bytes)
      return {
        kind: 'ok',
        bytes,
        mime,
        resolvedPath,
        ...(dims
          ? { width: dims.width, height: dims.height }
          : {}),
      }
    }
    catch (error) {
      if (signal.aborted)
        return { kind: 'error', reason: 'Image load aborted', cacheable: false }
      lastError = error
      if (isLocalPathFallbackRpcError(error))
        continue
      break
    }
  }

  const rpcMsg = localPathRpcErrorMessage(lastError)
  if (rpcMsg)
    return { kind: 'error', reason: rpcMsg, cacheable: false }
  return { kind: 'error', reason: 'Unable to load image', cacheable: false }
}

/**
 * Synchronously acquire a lease from the LRU cache if the image (or a deterministic
 * content refusal) is already cached. Refreshes LRU recency on hit; returns
 * `undefined` on cache miss or invalid request.
 */
export function peekLocalMarkdownImage(req: LocalMarkdownImageRequest): LocalMarkdownImageLease | undefined {
  const resolved = cacheKeyFor(req)
  if (!resolved)
    return undefined
  const entry = findCachedEntry(req.workerId, resolved.key, resolved.candidates)
  if (entry)
    return createLease(entry)
  const refused = lruGet(refusalCache, resolved.key)
  if (refused)
    return { result: refused, release: noopRelease }
  return undefined
}

/**
 * Read only the leading header bytes of a local Markdown image to determine its
 * intrinsic dimensions for offscreen height premeasurement without transferring
 * multi-megabyte payloads or evicting visible `blob:` cache entries.
 */
export async function probeLocalMarkdownImageDimensions(
  req: LocalMarkdownImageRequest,
  signal?: AbortSignal,
): Promise<ImageDimensions | undefined> {
  const resolved = cacheKeyFor(req)
  if (!resolved || signal?.aborted)
    return undefined

  const cachedOk = findCachedEntry(req.workerId, resolved.key, resolved.candidates)
  if (cachedOk) {
    return cachedOk.result.width !== undefined && cachedOk.result.height !== undefined
      ? { width: cachedOk.result.width, height: cachedOk.result.height }
      : undefined
  }
  if (lruGet(refusalCache, resolved.key))
    return undefined
  const cachedDims = lruGet(dimensionCache, resolved.key)
  if (cachedDims !== undefined)
    return cachedDims ?? undefined

  const epoch = cacheEpoch
  for (const path of resolved.candidates) {
    if (signal?.aborted)
      return undefined
    try {
      const head = await readWorkerFileHead({
        workerId: req.workerId,
        path,
        bytes: LOCAL_IMAGE_HEAD_BYTES,
        ...(signal
          ? { signal }
          : {}),
      })
      if (signal?.aborted)
        return undefined
      const resolvedPath = head.path || path
      const mime = sniffImageMime(resolvedPath, head.content)
      const refusal = fileImageRefusal(head.totalSize, mime)
      if (refusal !== undefined) {
        if (epoch === cacheEpoch)
          lruSet(refusalCache, resolved.key, { kind: 'error', reason: refusal }, LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES)
        return undefined
      }
      let dims = sniffImageDimensionsFromBytes(head.content)
      if (!dims) {
        const needed = imageHeaderBytesNeeded(head.content)
        if (needed !== null && needed > head.content.length && head.totalSize > BigInt(head.content.length)) {
          const longer = await readWorkerFileHead({
            workerId: req.workerId,
            path,
            bytes: needed,
            ...(signal
              ? { signal }
              : {}),
          })
          if (signal?.aborted)
            return undefined
          dims = sniffImageDimensionsFromBytes(longer.content)
        }
      }
      if (epoch === cacheEpoch)
        lruSet(dimensionCache, resolved.key, dims ?? null, LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES)
      return dims ?? undefined
    }
    catch (error) {
      if (signal?.aborted)
        return undefined
      if (isLocalPathFallbackRpcError(error))
        continue
      return undefined
    }
  }
  return undefined
}

/**
 * Acquire a ref-counted `blob:` URL lease for a local Markdown image, fetching over the
 * worker E2EE file RPC on cache miss and deduplicating concurrent in-flight loads.
 */
export function acquireLocalMarkdownImage(
  req: LocalMarkdownImageRequest,
  signal?: AbortSignal,
): Promise<LocalMarkdownImageLease> {
  const resolved = cacheKeyFor(req)
  if (!resolved) {
    return Promise.resolve({
      result: { kind: 'error', reason: 'Image path cannot be resolved' },
      release: noopRelease,
    })
  }

  if (signal?.aborted) {
    return Promise.resolve({
      result: { kind: 'error', reason: 'Image load aborted' },
      release: noopRelease,
    })
  }

  const cached = findCachedEntry(req.workerId, resolved.key, resolved.candidates)
  if (cached)
    return Promise.resolve(createLease(cached))

  const refused = lruGet(refusalCache, resolved.key)
  if (refused)
    return Promise.resolve({ result: refused, release: noopRelease })

  let flight = inFlight.get(resolved.key)
  if (!flight) {
    const startedFlight: InFlightEntry = {
      epoch: cacheEpoch,
      controller: new AbortController(),
      waiters: new Set(),
    }
    flight = startedFlight
    inFlight.set(resolved.key, startedFlight)

    void probeCandidates(req.workerId, resolved.candidates, startedFlight.controller.signal)
      .then((outcome) => {
        if (inFlight.get(resolved.key) === startedFlight)
          inFlight.delete(resolved.key)

        const liveWaiters = [...startedFlight.waiters]
        startedFlight.waiters.clear()
        for (const waiter of liveWaiters)
          waiter.detach()

        if (outcome.kind === 'error') {
          const errorResult: Extract<LocalMarkdownImageResult, { kind: 'error' }> = {
            kind: 'error',
            reason: outcome.reason,
          }
          if (outcome.cacheable && startedFlight.epoch === cacheEpoch)
            lruSet(refusalCache, resolved.key, errorResult, LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES)
          for (const waiter of liveWaiters)
            waiter.resolve({ result: errorResult, release: noopRelease })
          return
        }

        // All callers aborted before settlement: skip creating a blob URL altogether.
        if (liveWaiters.length === 0)
          return

        let entry: CacheEntry
        try {
          // Another spelling may have already populated the cache by resolvedPath while
          // this probe was in flight: reuse that entry instead of allocating a duplicate Blob.
          const aliasKey = resolvedPathIndexKey(req.workerId, outcome.resolvedPath)
          const existingAlias = startedFlight.epoch === cacheEpoch
            ? byResolvedPath.get(aliasKey)
            : undefined
          if (existingAlias && !existingAlias.evicted) {
            lruGet(cache, existingAlias.key)
            entry = existingAlias
          }
          else {
            const blob = new Blob([outcome.bytes as BlobPart], { type: outcome.mime })
            const url = URL.createObjectURL(blob)
            const result: Extract<LocalMarkdownImageResult, { kind: 'ok' }> = {
              kind: 'ok',
              url,
              resolvedPath: outcome.resolvedPath,
              ...(outcome.width !== undefined && outcome.height !== undefined
                ? { width: outcome.width, height: outcome.height }
                : {}),
            }
            entry = {
              key: resolved.key,
              workerId: req.workerId,
              result,
              bytes: outcome.bytes.byteLength,
              refCount: 0,
              evicted: false,
              revoked: false,
            }

            if (startedFlight.epoch === cacheEpoch) {
              cache.set(resolved.key, entry)
              byResolvedPath.set(aliasKey, entry)
              totalCachedBytes += entry.bytes
              evictIfNeeded(resolved.key)
            }
            else {
              // Cache was cleared while this probe was in flight: keep it out of the
              // new epoch's cache and revoke as soon as all pre-clear waiters release.
              entry.evicted = true
            }
          }
        }
        catch {
          const fallback: Extract<LocalMarkdownImageResult, { kind: 'error' }> = {
            kind: 'error',
            reason: 'Unable to load image',
          }
          for (const waiter of liveWaiters)
            waiter.resolve({ result: fallback, release: noopRelease })
          return
        }

        // Allocate all leases synchronously before resolving promises so an immediate
        // release by waiter 1 cannot drop refCount to 0 while waiter 2 is still pending.
        const leases = liveWaiters.map(() => createLease(entry))
        for (let i = 0; i < liveWaiters.length; i++)
          liveWaiters[i]!.resolve(leases[i]!)
      })
  }

  const activeFlight = flight
  return new Promise<LocalMarkdownImageLease>((resolve) => {
    const waiter: Waiter = {
      resolve,
      detach: noopRelease,
    }

    const onAbort = () => {
      if (!activeFlight.waiters.delete(waiter))
        return
      waiter.detach()
      resolve({
        result: { kind: 'error', reason: 'Image load aborted' },
        release: noopRelease,
      })
      if (activeFlight.waiters.size === 0) {
        if (inFlight.get(resolved.key) === activeFlight)
          inFlight.delete(resolved.key)
        activeFlight.controller.abort()
      }
    }

    waiter.detach = () => {
      signal?.removeEventListener('abort', onAbort)
    }

    activeFlight.waiters.add(waiter)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

/**
 * Evict every cached local Markdown image entry, bump the cache epoch, and drop
 * in-flight coalescing entries so subsequent acquisitions start fresh in the new epoch.
 */
export function clearLocalMarkdownImageCache(): void {
  cacheEpoch++
  inFlight.clear()
  byResolvedPath.clear()
  refusalCache.clear()
  dimensionCache.clear()
  for (const entry of Array.from(cache.values()))
    evictEntry(entry)
  totalCachedBytes = 0
}

/**
 * Reset module cache state between unit tests and re-subscribe to storage account
 * changes (since `resetStorageAccountForTests` in vitest.setup clears all account
 * listeners before each test).
 */
export function _resetLocalMarkdownImageCacheForTest(): void {
  clearLocalMarkdownImageCache()
  onStorageAccountChange(clearLocalMarkdownImageCache)
}

onStorageAccountChange(clearLocalMarkdownImageCache)
