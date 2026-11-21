import type { PathFlavor } from './paths'
import { Code } from '@connectrpc/connect'
import { onStorageAccountChange } from './browserStorage'
import { ChannelError } from './channelError'
import { sniffImageMime } from './fileType'
import { MAX_FILE_IMAGE_BYTES, RENDERABLE_IMAGE_MIME_TYPES } from './imageBlocks'
import { sniffImageDimensionsFromBytes } from './imageDimensions'
import { resolveLocalLinkCandidates } from './localLinks'
import { readWorkerFile } from './readWorkerFile'

/** Maximum number of cached local Markdown image blob entries. */
export const LOCAL_MARKDOWN_IMAGE_MAX_ENTRIES = 64
/** Maximum total decoded byte footprint across cached local Markdown image entries. */
export const LOCAL_MARKDOWN_IMAGE_MAX_BYTES = 32 * 1024 * 1024

export interface LocalMarkdownImageRequest {
  workerId: string
  rawSrc: string
  baseDir: string
  workingDir: string
  homeDir: string
  flavor: PathFlavor
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
const inFlight = new Map<string, InFlightEntry>()
let totalCachedBytes = 0
let cacheEpoch = 0

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

function touchEntry(entry: CacheEntry): void {
  cache.delete(entry.key)
  cache.set(entry.key, entry)
}

function evictEntry(entry: CacheEntry): void {
  cache.delete(entry.key)
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

function isFallbackRpcError(error: unknown): boolean {
  return error instanceof ChannelError
    && error.source === 'rpc'
    && (error.code === Code.NotFound || error.code === Code.PermissionDenied)
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
  }

async function probeCandidates(
  workerId: string,
  candidates: readonly string[],
  signal: AbortSignal,
): Promise<ProbeOutcome> {
  let lastError: unknown
  for (const path of candidates) {
    if (signal.aborted)
      return { kind: 'error', reason: 'Image load aborted' }

    try {
      const resp = await readWorkerFile({
        workerId,
        path,
        maxBytes: MAX_FILE_IMAGE_BYTES,
        signal,
      })
      if (resp.totalSize > BigInt(MAX_FILE_IMAGE_BYTES))
        return { kind: 'error', reason: 'Image too large to preview' }
      if (resp.totalSize === 0n)
        return { kind: 'error', reason: 'Image file is empty' }
      const bytes = resp.content
      const resolvedPath = resp.path || path
      const mime = sniffImageMime(resolvedPath, bytes)
      if (!RENDERABLE_IMAGE_MIME_TYPES.has(mime))
        return { kind: 'error', reason: 'Unsupported image format' }
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
        return { kind: 'error', reason: 'Image load aborted' }
      lastError = error
      if (isFallbackRpcError(error))
        continue
      break
    }
  }

  if (lastError instanceof ChannelError && lastError.source === 'rpc' && lastError.message)
    return { kind: 'error', reason: lastError.message }
  return { kind: 'error', reason: 'Unable to load image' }
}

/**
 * Synchronously acquire a lease from the LRU cache if the image is already loaded.
 * Refreshes LRU recency on hit; returns `undefined` on cache miss or invalid request.
 */
export function peekLocalMarkdownImage(req: LocalMarkdownImageRequest): LocalMarkdownImageLease | undefined {
  const resolved = cacheKeyFor(req)
  if (!resolved)
    return undefined
  const entry = cache.get(resolved.key)
  if (!entry)
    return undefined
  touchEntry(entry)
  return createLease(entry)
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

  const cached = cache.get(resolved.key)
  if (cached) {
    touchEntry(cached)
    return Promise.resolve(createLease(cached))
  }

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
          for (const waiter of liveWaiters)
            waiter.resolve({ result: outcome, release: noopRelease })
          return
        }

        // All callers aborted before settlement: skip creating a blob URL altogether.
        if (liveWaiters.length === 0)
          return

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
        const entry: CacheEntry = {
          key: resolved.key,
          result,
          bytes: outcome.bytes.byteLength,
          refCount: 0,
          evicted: false,
          revoked: false,
        }

        if (startedFlight.epoch === cacheEpoch) {
          const existing = cache.get(resolved.key)
          if (existing)
            evictEntry(existing)
          cache.set(resolved.key, entry)
          totalCachedBytes += entry.bytes
          evictIfNeeded(resolved.key)
        }
        else {
          // Cache was cleared while this probe was in flight: keep it out of the
          // new epoch's cache and revoke as soon as all pre-clear waiters release.
          entry.evicted = true
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
