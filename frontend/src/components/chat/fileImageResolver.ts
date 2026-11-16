import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { createSignal, untrack } from 'solid-js'
import { imageRenderInfo, MAX_INLINE_IMAGE_BASE64_LEN } from '~/lib/imageBlocks'
import { lruGet, lruSet } from '~/lib/mapLru'

/**
 * A refusal of an image file that a new read of the same file cannot change: its size,
 * its format, or its content. A reader throws it, and the resolver then keeps the refusal
 * until a caller asks for a refresh. Every other failure of a read, such as a Worker that
 * does not answer, is read again at the next ask.
 */
export class FileImageRefusedError extends Error {
  override name = 'FileImageRefusedError'
}

/**
 * Read an image file. Rejects with a FileImageRefusedError for a file that it refuses
 * whatever a new read gives, and with any other error for a read that failed.
 */
export type FileImageReader = (path: string, signal: AbortSignal) => Promise<ImageResultSource>
/**
 * Read the size of an image file from its first bytes. Resolves `undefined` when the
 * header gives no size, and rejects when the read failed.
 */
export type FileImageSizeReader = (path: string, signal: AbortSignal) => Promise<ImageDimensions | undefined>
export interface FileImageLoadOptions {
  refresh?: boolean
  reference?: string
}

/**
 * What is known of the size of an image file, after a read settled:
 * - `none`: the header of the file gives no size, so only the image gives its height.
 * - `known`: the size of the image, from the header of the file or from the image.
 */
export type SettledFileImageSize
  = | { kind: 'none' }
    | { kind: 'known', size: ImageDimensions }

/**
 * What a host holds for the size of an image file: a settled answer (SettledFileImageSize),
 * or `unknown` when it holds none. A host holds none before a read settled, after a read
 * that failed, and after it dropped the answer at its limit or at a clear.
 */
export type FileImageSizeAnswer
  = | { kind: 'unknown' }
    | SettledFileImageSize

/** The settled answer for the size that a read gave: `undefined` is a header with no size. */
export function settledFileImageSize(size: ImageDimensions | undefined): SettledFileImageSize {
  return size === undefined
    ? { kind: 'none' }
    : { kind: 'known', size }
}

/**
 * The image files of one chat row: the reads of an image and of its size, and what the
 * host holds of each. The four members travel together, so no host can supply a part.
 */
export interface FileImageSource {
  /** Read an image file. `refresh` reads it again past the cache. Rejects for a read that failed. */
  load: (path: string, options?: { refresh?: boolean }) => Promise<ImageResultSource>
  /** The image that the host holds for `path`, when it holds one. Reactive. */
  peek: (path: string) => ImageResultSource | undefined
  /**
   * Read the size of an image file from its first bytes. Never rejects: a file whose
   * header gives no size, and a failed read, give `undefined`.
   */
  loadSize: (path: string) => Promise<ImageDimensions | undefined>
  /** What the host holds for the size of the image file (see FileImageSizeAnswer). Not reactive. */
  peekSize: (path: string) => FileImageSizeAnswer
}

/**
 * The image files of a row whose host has no source, such as a row with no resolver. A
 * read of an image fails, and no read can give a size: the size answer is "no size" at
 * once, so no view waits for a read.
 */
export const NO_FILE_IMAGES: FileImageSource = {
  load: () => Promise.reject(new Error('The image source is unavailable')),
  peek: () => undefined,
  loadSize: async () => undefined,
  peekSize: () => ({ kind: 'none' }),
}

/**
 * The most image sizes that a resolver keeps, and the most refusals. It is above the
 * largest loaded window of a chat (MAX_LOADED_CHAT_MESSAGES_CEILING, which the test of
 * this module compares it with), so a window whose rows hold one image each keeps the
 * size of each. The resolver drops the sizes that no caller asked for the longest.
 */
export const FILE_IMAGE_SIZE_CACHE_MAX = 2400

/**
 * The key of one image file in each map of a resolver. The reference identifies the tool
 * call that asks: the file can differ at the time of another tool call.
 */
function fileImageKey(path: string, reference = ''): string {
  return JSON.stringify([reference, path])
}

/**
 * The requests that run, one for each key. Every caller of a key shares the request that
 * runs for it. A request that `abortAll` replaced records nothing: its answer can be
 * stale. The image reads and the size reads of createFileImageResolver each keep one.
 */
function createKeyedRequests<T>() {
  const pending = new Map<string, { controller: AbortController, promise: Promise<T> }>()
  return {
    /**
     * The request of `key`: the one that runs, or a new one that `start` makes. `isCurrent`
     * answers false once `abortAll` replaced the request, which then must record nothing.
     */
    run(key: string, start: (signal: AbortSignal, isCurrent: () => boolean) => Promise<T>): Promise<T> {
      const existing = pending.get(key)
      if (existing)
        return existing.promise
      const controller = new AbortController()
      const isCurrent = (): boolean => pending.get(key)?.controller === controller
      // The request starts in the next microtask, after it is registered.
      const promise = Promise.resolve()
        .then(() => start(controller.signal, isCurrent))
        .finally(() => {
          if (isCurrent())
            pending.delete(key)
        })
      pending.set(key, { controller, promise })
      return promise
    },
    /** Abort every request that runs, and forget it. */
    abortAll(): void {
      for (const entry of pending.values())
        entry.controller.abort()
      pending.clear()
    },
  }
}

/**
 * Share reads across visible rows, measurement rows, and image tabs without retaining unlimited image bytes.
 *
 * The resolver also keeps the size of the image files that rows asked for, so a row can
 * reserve the image box before it reads the image. A size read is never deferred, and a
 * size has no cost in `maxChars`. The sizes have a limit of their own
 * (FILE_IMAGE_SIZE_CACHE_MAX).
 *
 * It keeps each refusal of the reader also (see FileImageRefusedError), until a caller
 * asks for a refresh. Each copy of a row asks for its image, and a refused file would
 * cost one read of up to MAX_FILE_IMAGE_BYTES for each of them.
 */
export function createFileImageResolver(read: FileImageReader, readSize: FileImageSizeReader, maxChars = 2 * MAX_INLINE_IMAGE_BASE64_LEN) {
  const cache = new Map<string, ImageResultSource>()
  const requests = createKeyedRequests<ImageResultSource>()
  const [version, setVersion] = createSignal(0)
  let chars = 0
  const cost = (source: ImageResultSource) => source.data?.length ?? source.url?.length ?? 0
  // The refusals of the reader, the oldest first. A refusal is one small answer for one
  // image of a row, as a size is, so the refusals take the limit of the sizes.
  const refusals = new Map<string, FileImageRefusedError>()
  // The size of each image file that the resolver knows: the answer of a size read that
  // settled, or the size that an image stated when the resolver read it. A failed read
  // records nothing. In LRU order, the least recently asked size first.
  const sizes = new Map<string, SettledFileImageSize>()
  const sizeRequests = createKeyedRequests<ImageDimensions | undefined>()

  function clear(): void {
    requests.abortAll()
    cache.clear()
    chars = 0
    refusals.clear()
    setVersion(value => value + 1)
    sizeRequests.abortAll()
    sizes.clear()
  }

  /** Keep `answer` for `key`, as the size that a caller asked for last. */
  function keepSize(key: string, answer: SettledFileImageSize): void {
    // The limit drops a size with no notice. No reader depends on a size that it lost: a
    // view keeps the size that it showed (see ImageResultView), and any other caller asks
    // again.
    lruSet(sizes, key, answer, FILE_IMAGE_SIZE_CACHE_MAX)
  }

  /**
   * Read the size of the image file at `path`. One request serves every caller of a key,
   * and the resolver keeps the answer up to its limit. Never rejects: a failed read gives
   * `undefined` and records nothing, so the next ask reads again. A read that gave no size,
   * or failed, gives the size that an image stated while the read ran (see load).
   *
   * A call for a size that the resolver holds sends no request. It still counts as an ask:
   * the resolver drops the sizes that no caller asked for the longest.
   */
  function loadSize(path: string, options?: { reference?: string }): Promise<ImageDimensions | undefined> {
    return untrack(() => {
      const key = fileImageKey(path, options?.reference)
      const kept = lruGet(sizes, key)
      if (kept !== undefined) {
        return Promise.resolve(kept.kind === 'known'
          ? kept.size
          : undefined)
      }
      // An image that the resolver holds states its size, so its header needs no read.
      const held = cache.get(key)
      if (held) {
        keepSize(key, settledFileImageSize(held.dimensions))
        return Promise.resolve(held.dimensions)
      }
      return sizeRequests.run(key, async (signal, isCurrent) => {
        // The size that an image stated while the read ran (see load), if any.
        const stated = (): ImageDimensions | undefined => {
          const answer = isCurrent()
            ? sizes.get(key)
            : undefined
          return answer?.kind === 'known'
            ? answer.size
            : undefined
        }
        try {
          const size = await readSize(path, signal)
          if (size !== undefined) {
            if (isCurrent())
              keepSize(key, { kind: 'known', size })
            return size
          }
          // A read that gave no size replaces no size that an image stated meanwhile: the
          // image states its size with more authority.
          const known = stated()
          if (known === undefined && isCurrent())
            keepSize(key, { kind: 'none' })
          return known
        }
        catch {
          // A failed read records nothing, so the next ask reads again: the failure can end
          // by itself, as for a Worker that did not answer.
          return stated()
        }
      })
    })
  }

  /**
   * Read the image file at `path`. One request serves every caller of a key, and the
   * resolver keeps the image within `maxChars`. Rejects for a read that failed, and for a
   * file that the reader or the image policy refuses. It keeps a refusal, and rejects
   * with it again with no read, until a call with `refresh`.
   */
  function load(path: string, options?: FileImageLoadOptions): Promise<ImageResultSource> {
    return untrack(() => {
      const key = fileImageKey(path, options?.reference)
      if (options?.refresh) {
        refusals.delete(key)
        const previous = cache.get(key)
        if (previous) {
          cache.delete(key)
          chars -= cost(previous)
          setVersion(value => value + 1)
        }
      }
      const cached = lruGet(cache, key)
      if (cached)
        return Promise.resolve(cached)
      const refusal = refusals.get(key)
      if (refusal)
        return Promise.reject(refusal)
      return requests.run(key, async (signal, isCurrent) => {
        try {
          const source = await read(path, signal)
          if (signal.aborted || !isCurrent())
            throw new DOMException('The image request is no longer active', 'AbortError')
          if (!imageRenderInfo(source).src)
            throw new FileImageRefusedError('The image file cannot be displayed')
          const size = cost(source)
          if (size <= maxChars) {
            while (chars + size > maxChars && cache.size > 0) {
              const oldest = cache.entries().next().value!
              cache.delete(oldest[0])
              chars -= cost(oldest[1])
            }
            cache.set(key, source)
            chars += size
            setVersion(value => value + 1)
          }
          // The image that draws states its size with more authority than a size read:
          // that read can fail, and the file can differ since. A row that asks for the
          // size later then reserves the box of this image.
          if (source.dimensions !== undefined)
            keepSize(key, { kind: 'known', size: source.dimensions })
          return source
        }
        catch (error) {
          if (error instanceof FileImageRefusedError && isCurrent())
            lruSet(refusals, key, error, FILE_IMAGE_SIZE_CACHE_MAX)
          throw error
        }
      })
    })
  }

  return {
    load,
    loadSize,
    clear,
    peek: (path: string, reference = '') => {
      version()
      return cache.get(fileImageKey(path, reference))
    },
    /**
     * What the resolver holds for the size of the image file (see FileImageSizeAnswer).
     * Not reactive: a caller that needs a later answer asks `loadSize`.
     *
     * It answers `unknown` again for a size that the limit of the resolver dropped, or
     * that `clear` dropped. A caller that must keep a size keeps its own copy.
     */
    peekSize: (path: string, reference = ''): FileImageSizeAnswer =>
      sizes.get(fileImageKey(path, reference)) ?? { kind: 'unknown' },
  }
}
