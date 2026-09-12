import type { ImageResultSource } from '~/lib/imageBlocks'
import { createSignal, untrack } from 'solid-js'
import { imageRenderInfo, MAX_INLINE_IMAGE_BASE64_LEN } from '~/lib/imageBlocks'

export type FileImageReader = (path: string, signal: AbortSignal) => Promise<ImageResultSource>
export interface FileImageLoadOptions {
  refresh?: boolean
  reference?: string
}

/** Share reads across visible rows, measurement rows, and image tabs without retaining unlimited image bytes. */
export function createFileImageResolver(read: FileImageReader, maxChars = 2 * MAX_INLINE_IMAGE_BASE64_LEN) {
  const cache = new Map<string, ImageResultSource>()
  const pending = new Map<string, { controller: AbortController, promise: Promise<ImageResultSource> }>()
  const [version, setVersion] = createSignal(0)
  let chars = 0
  const cost = (source: ImageResultSource) => source.data?.length ?? source.url?.length ?? 0

  function clear(): void {
    for (const entry of pending.values())
      entry.controller.abort()
    pending.clear()
    cache.clear()
    chars = 0
    setVersion(value => value + 1)
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
    clear,
    peek: (path: string, reference = '') => {
      version()
      return cache.get(JSON.stringify([reference, path]))
    },
  }
}
