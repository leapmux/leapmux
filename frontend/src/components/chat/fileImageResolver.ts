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
  const pending = new Map<string, { controller: AbortController, promise: Promise<ImageResultSource> }>()
  const [version, setVersion] = createSignal(0)
  let chars = 0
  const cost = (source: ImageResultSource) => source.data?.length ?? source.url?.length ?? 0
  // The refusals of the reader, the oldest first. A refusal is one small answer for one
  // image of a row, as a size is, so the refusals take the limit of the sizes.
  const refusals = new Map<string, FileImageRefusedError>()
  // The size of each image file that the resolver knows: the answer of a size read that
  // settled, or the size that an image stated when the resolver read it. `null` for a
  // file whose read gave no size or failed. In LRU order, the least recently asked size
  // first.
  const sizes = new Map<string, ImageDimensions | null>()
  const pendingSizes = new Map<string, { controller: AbortController, promise: Promise<ImageDimensions | undefined> }>()

  function clear(): void {
    for (const entry of pending.values())
      entry.controller.abort()
    pending.clear()
    cache.clear()
    chars = 0
    refusals.clear()
    setVersion(value => value + 1)
    for (const entry of pendingSizes.values())
      entry.controller.abort()
    pendingSizes.clear()
    sizes.clear()
  }

  /** Keep `size` as the answer for `key`, as the size that a caller asked for last. */
  function keepSize(key: string, size: ImageDimensions | null): void {
    // The limit drops a size with no notice. No reader depends on a size that it lost: a
    // view keeps the size that it showed (see ImageResultView), and any other caller asks
    // again.
    lruSet(sizes, key, size, FILE_IMAGE_SIZE_CACHE_MAX)
  }

  /**
   * Read the size of the image file at `path`. One request serves every caller of a key,
   * and the resolver keeps the answer up to its limit. Never rejects: a failed read gives
   * `undefined`.
   *
   * A call for a size that the resolver holds sends no request. It still counts as an ask:
   * the resolver drops the sizes that no caller asked for the longest.
   */
  function loadSize(path: string, options?: { reference?: string }): Promise<ImageDimensions | undefined> {
    return untrack(() => {
      const key = fileImageKey(path, options?.reference)
      if (sizes.has(key))
        return Promise.resolve(lruGet(sizes, key) ?? undefined)
      // An image that the resolver holds states its size, so its header needs no read.
      const held = cache.get(key)
      if (held) {
        keepSize(key, held.dimensions ?? null)
        return Promise.resolve(held.dimensions)
      }
      const existing = pendingSizes.get(key)
      if (existing)
        return existing.promise
      const controller = new AbortController()
      const promise = Promise.resolve()
        .then(() => readSize(path, controller.signal))
        .catch(() => undefined)
        .then((size) => {
          // A request that `clear` replaced records nothing: its answer can be stale.
          if (pendingSizes.get(key)?.controller !== controller)
            return size
          pendingSizes.delete(key)
          // A read that gave no size replaces no size that an image stated meanwhile: the
          // image states its size with more authority (see load).
          const stated = sizes.get(key)
          if (size === undefined && stated != null)
            return stated
          keepSize(key, size ?? null)
          return size
        })
      pendingSizes.set(key, { controller, promise })
      return promise
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
      const existing = pending.get(key)
      if (existing)
        return existing.promise
      const controller = new AbortController()
      // A request that `clear` replaced records nothing: its answer can be stale.
      const isCurrent = (): boolean => pending.get(key)?.controller === controller
      const request = async (): Promise<ImageResultSource> => {
        try {
          const source = await read(path, controller.signal)
          if (controller.signal.aborted || !isCurrent())
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
            keepSize(key, source.dimensions)
          return source
        }
        catch (error) {
          if (error instanceof FileImageRefusedError && isCurrent())
            lruSet(refusals, key, error, FILE_IMAGE_SIZE_CACHE_MAX)
          throw error
        }
        finally {
          if (isCurrent())
            pending.delete(key)
        }
      }
      // The read starts in the next microtask, after the request is registered.
      const promise = Promise.resolve().then(request)
      pending.set(key, { controller, promise })
      return promise
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
     * The size of the image file that the resolver knows: `undefined` before a read
     * settled, and `null` for a file whose read gave no size or failed. Not reactive: a
     * caller that needs a later answer asks `loadSize`.
     *
     * It answers `undefined` again for a size that the limit of the resolver dropped, or
     * that `clear` dropped. A caller that must keep a size keeps its own copy.
     */
    peekSize: (path: string, reference = ''): ImageDimensions | null | undefined =>
      sizes.get(fileImageKey(path, reference)),
  }
}
