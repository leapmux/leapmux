import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { createSignal, untrack } from 'solid-js'
import { imageRenderInfo, MAX_INLINE_IMAGE_BASE64_LEN } from '~/lib/imageBlocks'
import { capMapInsertionOrder, lruGet } from '~/lib/mapLru'

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
 * The most image sizes that a resolver keeps. It is above the largest loaded window of a
 * chat (MAX_LOADED_CHAT_MESSAGES_CEILING, which the test of this module compares it
 * with), so the size of each image row of a window stays. The resolver drops the sizes
 * that no caller asked for the longest.
 */
export const FILE_IMAGE_SIZE_CACHE_MAX = 2400

/**
 * Share reads across visible rows, measurement rows, and image tabs without retaining unlimited image bytes.
 *
 * The resolver also keeps the size of the image files that rows asked for, so a row can
 * reserve the image box before it reads the image. A size read is never deferred, and a
 * size has no cost in `maxChars`. The sizes have a limit of their own
 * (FILE_IMAGE_SIZE_CACHE_MAX).
 */
export function createFileImageResolver(read: FileImageReader, readSize: FileImageSizeReader, maxChars = 2 * MAX_INLINE_IMAGE_BASE64_LEN) {
  const cache = new Map<string, ImageResultSource>()
  const pending = new Map<string, { controller: AbortController, promise: Promise<ImageResultSource> }>()
  const [version, setVersion] = createSignal(0)
  let chars = 0
  const cost = (source: ImageResultSource) => source.data?.length ?? source.url?.length ?? 0
  // The answer of each size read that settled: the size, or `null` for a file whose read
  // gave no size or failed. In LRU order, the least recently asked size first.
  const sizes = new Map<string, ImageDimensions | null>()
  const pendingSizes = new Map<string, { controller: AbortController, promise: Promise<ImageDimensions | undefined> }>()
  // A signal of its own: a size that settles must not run each reader of a cached image again.
  const [sizeVersion, setSizeVersion] = createSignal(0)

  function clear(): void {
    for (const entry of pending.values())
      entry.controller.abort()
    pending.clear()
    cache.clear()
    chars = 0
    setVersion(value => value + 1)
    for (const entry of pendingSizes.values())
      entry.controller.abort()
    pendingSizes.clear()
    sizes.clear()
    setSizeVersion(value => value + 1)
  }

  /**
   * Read the size of the image file at `path`. One request serves every caller of a key,
   * and the resolver keeps the answer up to its limit. Never rejects: a failed read gives
   * `undefined`.
   */
  function loadSize(path: string, options?: { reference?: string }): Promise<ImageDimensions | undefined> {
    return untrack(() => {
      const key = JSON.stringify([options?.reference ?? '', path])
      if (sizes.has(key))
        return Promise.resolve(lruGet(sizes, key) ?? undefined)
      const existing = pendingSizes.get(key)
      if (existing)
        return existing.promise
      const controller = new AbortController()
      const promise = Promise.resolve()
        .then(() => readSize(path, controller.signal))
        .catch(() => undefined)
        .then((size) => {
          // A request that `clear` replaced records nothing: its answer can be stale.
          if (pendingSizes.get(key)?.controller === controller) {
            pendingSizes.delete(key)
            sizes.set(key, size ?? null)
            // The limit drops a size with no notice. No reader depends on a size that it
            // lost: a view keeps the size that it showed (see ImageResultView), and any
            // other caller asks again.
            capMapInsertionOrder(sizes, FILE_IMAGE_SIZE_CACHE_MAX)
            setSizeVersion(value => value + 1)
          }
          return size
        })
      pendingSizes.set(key, { controller, promise })
      return promise
    })
  }

  function load(path: string, options?: FileImageLoadOptions): Promise<ImageResultSource> {
    return untrack(() => {
      const key = JSON.stringify([options?.reference ?? '', path])
      if (options?.refresh) {
        const previous = cache.get(key)
        if (previous) {
          cache.delete(key)
          chars -= cost(previous)
          setVersion(value => value + 1)
        }
      }
      const cached = cache.get(key)
      if (cached) {
        cache.delete(key)
        cache.set(key, cached)
        return Promise.resolve(cached)
      }
      const existing = pending.get(key)
      if (existing)
        return existing.promise
      const controller = new AbortController()
      const promise = Promise.resolve().then(() => read(path, controller.signal)).then((source) => {
        if (controller.signal.aborted || pending.get(key)?.controller !== controller)
          throw new DOMException('The image request is no longer active', 'AbortError')
        if (!imageRenderInfo(source).src)
          throw new Error('The image file cannot be displayed')
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
        return source
      }).finally(() => {
        if (pending.get(key)?.controller === controller)
          pending.delete(key)
      })
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
      return cache.get(JSON.stringify([reference, path]))
    },
    /**
     * The size that `loadSize` gave for the image file: `undefined` before a read
     * settled, and `null` for a file whose read gave no size or failed. Reactive.
     *
     * It answers `undefined` again for a size that the limit of the resolver dropped, or
     * that `clear` dropped. A caller that must keep a size keeps its own copy.
     */
    peekSize: (path: string, reference = ''): ImageDimensions | null | undefined => {
      sizeVersion()
      return sizes.get(JSON.stringify([reference, path]))
    },
  }
}
