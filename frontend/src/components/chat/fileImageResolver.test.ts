import type { FileImageSizeReader } from './fileImageResolver'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { MAX_LOADED_CHAT_MESSAGES_CEILING } from '~/stores/chat.store'
import { deferred } from '~/test-support/async'
import { createFileImageResolver, FILE_IMAGE_SIZE_CACHE_MAX } from './fileImageResolver'

/** A size reader for the cases whose subject is the image itself: the header gives no size. */
const noSize: FileImageSizeReader = async () => undefined

/** An image reader for the cases whose subject is the size. */
const anImage = async () => ({ data: 'AAAA', mimeType: 'image/png' })

describe('file image resolution', () => {
  it('reads the file again for a different tool call', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read, noSize)
    await images.load('/image.png', { reference: 'first-call' })
    await images.load('/image.png', { reference: 'second-call' })
    await images.load('/image.png', { reference: 'first-call' })
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('refreshes an image when the user retries decoding it', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read, noSize)
    await images.load('/image.png')
    await images.load('/image.png', { refresh: true })
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('uses one read for concurrent consumers and later measurement reads', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read, noSize)
    const [first, second] = await Promise.all([
      images.load('/image.png'),
      images.load('/image.png'),
    ])
    expect(first).toBe(second)
    expect(images.peek('/image.png')).toBe(first)
    await images.load('/image.png')
    expect(read).toHaveBeenCalledTimes(1)
  })

  it('evicts image bytes according to the cache limit', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read, noSize, 8)
    await images.load('first')
    await images.load('second')
    await images.load('first')
    await images.load('third')
    expect(images.peek('first')).toBeDefined()
    expect(images.peek('second')).toBeUndefined()
    expect(images.peek('third')).toBeDefined()
    await images.load('second')
    expect(read).toHaveBeenCalledTimes(4)
  })

  it('does not retain an image larger than its cache budget', async () => {
    const read = vi.fn(async () => ({ data: 'AAAAAAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read, noSize, 4)
    await images.load('large')
    expect(images.peek('large')).toBeUndefined()
    await images.load('large')
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('retries a failed read when a consumer asks again', async () => {
    const read = vi.fn().mockRejectedValueOnce(new Error('File unavailable')).mockResolvedValue({ data: 'AAAA', mimeType: 'image/png' })
    const images = createFileImageResolver(read, noSize)
    await expect(images.load('image')).rejects.toThrow('File unavailable')
    expect(images.peek('image')).toBeUndefined()
    await images.load('image')
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('rejects stale reads after the scope clears', async () => {
    let finish!: (source: { data: string, mimeType: string }) => void
    let signal!: AbortSignal
    const images = createFileImageResolver(async (_path, currentSignal) => {
      signal = currentSignal
      return new Promise((resolve) => {
        finish = resolve
      })
    }, noSize)
    const pending = images.load('image')
    const rejected = expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    await Promise.resolve()
    images.clear()
    expect(signal.aborted).toBe(true)
    finish({ data: 'AAAA', mimeType: 'image/png' })
    await rejected
    expect(images.peek('image')).toBeUndefined()
  })

  it('rejects a file response that violates the shared image policy', async () => {
    const images = createFileImageResolver(async () => ({ url: 'https://example.com/image.png' }), noSize)
    await expect(images.load('image')).rejects.toThrow('cannot be displayed')
    expect(images.peek('image')).toBeUndefined()
  })
  it('uses one size read for concurrent consumers, and keeps the answer', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const readSize = vi.fn(() => size.promise)
    const images = createFileImageResolver(anImage, readSize)

    const first = images.loadSize('/image.png')
    const second = images.loadSize('/image.png')
    await Promise.resolve()
    // Before the read settles, the cache has no answer.
    expect(images.peekSize('/image.png')).toBeUndefined()
    size.resolve({ width: 400, height: 200 })

    expect(await first).toEqual({ width: 400, height: 200 })
    expect(await second).toEqual({ width: 400, height: 200 })
    expect(images.peekSize('/image.png')).toEqual({ width: 400, height: 200 })
    expect(await images.loadSize('/image.png')).toEqual({ width: 400, height: 200 })
    expect(readSize).toHaveBeenCalledTimes(1)
  })

  it('reads the size again for a different tool call', async () => {
    const readSize = vi.fn(async () => ({ width: 400, height: 200 }))
    const images = createFileImageResolver(anImage, readSize)

    await images.loadSize('/image.png', { reference: 'first-call' })
    await images.loadSize('/image.png', { reference: 'second-call' })
    await images.loadSize('/image.png', { reference: 'first-call' })

    expect(readSize).toHaveBeenCalledTimes(2)
    expect(images.peekSize('/image.png', 'first-call')).toEqual({ width: 400, height: 200 })
    expect(images.peekSize('/image.png')).toBeUndefined()
  })

  it('answers no size for a read that gave none, and for a read that failed', async () => {
    const readSize = vi.fn()
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error('File unavailable'))
    const images = createFileImageResolver(anImage, readSize)

    await expect(images.loadSize('no-header')).resolves.toBeUndefined()
    await expect(images.loadSize('unreadable')).resolves.toBeUndefined()

    expect(images.peekSize('no-header')).toBeNull()
    expect(images.peekSize('unreadable')).toBeNull()
    // Each answer is kept: a second call reads nothing.
    await images.loadSize('no-header')
    await images.loadSize('unreadable')
    expect(readSize).toHaveBeenCalledTimes(2)
  })

  it('notifies a reader of a size when its read settles', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const images = createFileImageResolver(anImage, () => size.promise)
    const seen: (ImageDimensions | null | undefined)[] = []
    const dispose = createRoot((disposeRoot) => {
      // A reactive reader of the cache, as the size memo of an image view is. It runs
      // again only when the resolver notifies it.
      createEffect(() => seen.push(images.peekSize('/image.png')))
      return disposeRoot
    })
    expect(seen).toEqual([undefined])

    const pending = images.loadSize('/image.png')
    // The start of a read changes no answer, so it notifies no reader.
    expect(seen).toEqual([undefined])
    size.resolve({ width: 10, height: 20 })
    await pending

    expect(seen).toEqual([
      undefined,
      { width: 10, height: 20 },
    ])
    dispose()
  })

  it('notifies a reader of a size when the scope clears', async () => {
    const images = createFileImageResolver(anImage, async () => ({ width: 10, height: 20 }))
    await images.loadSize('/image.png')
    const seen: (ImageDimensions | null | undefined)[] = []
    const dispose = createRoot((disposeRoot) => {
      createEffect(() => seen.push(images.peekSize('/image.png')))
      return disposeRoot
    })

    images.clear()

    expect(seen).toEqual([
      { width: 10, height: 20 },
      undefined,
    ])
    dispose()
  })

  it('drops the sizes when the scope clears, and reads again afterwards', async () => {
    const readSize = vi.fn(async () => ({ width: 400, height: 200 }))
    const images = createFileImageResolver(anImage, readSize)
    await images.loadSize('/image.png')

    images.clear()

    expect(images.peekSize('/image.png')).toBeUndefined()
    await images.loadSize('/image.png')
    expect(readSize).toHaveBeenCalledTimes(2)
  })

  it('records nothing for a size read that the scope cleared', async () => {
    const size = deferred<ImageDimensions | undefined>()
    let signal!: AbortSignal
    const images = createFileImageResolver(anImage, (_path, currentSignal) => {
      signal = currentSignal
      return size.promise
    })
    const pending = images.loadSize('/image.png')
    await Promise.resolve()

    images.clear()
    expect(signal.aborted).toBe(true)
    size.resolve({ width: 400, height: 200 })
    await pending

    // The answer of the old scope must not serve the new one.
    expect(images.peekSize('/image.png')).toBeUndefined()
  })

  it('charges a size nothing against the image cache limit', async () => {
    const images = createFileImageResolver(anImage, async () => ({ width: 400, height: 200 }), 4)
    await images.load('cached')
    expect(images.peek('cached')).toBeDefined()

    for (let i = 0; i < 100; i++)
      await images.loadSize(`image-${i}`)

    // The cache holds 4 characters, which the one image fills. No size evicted it.
    expect(images.peek('cached')).toBeDefined()
    expect(images.peekSize('image-0')).toEqual({ width: 400, height: 200 })
  })

  it('drops the least recently asked size past the limit, and reads it again when a caller asks', async () => {
    const readSize = vi.fn<FileImageSizeReader>(async () => ({ width: 400, height: 200 }))
    const images = createFileImageResolver(anImage, readSize)
    for (let i = 0; i <= FILE_IMAGE_SIZE_CACHE_MAX; i++)
      await images.loadSize(`image-${i}`)

    expect(images.peekSize('image-0')).toBeUndefined()
    expect(images.peekSize('image-1')).toEqual({ width: 400, height: 200 })
    expect(images.peekSize(`image-${FILE_IMAGE_SIZE_CACHE_MAX}`)).toEqual({ width: 400, height: 200 })

    readSize.mockClear()
    await expect(images.loadSize('image-0')).resolves.toEqual({ width: 400, height: 200 })
    expect(readSize).toHaveBeenCalledOnce()
  })

  it('keeps a size that a caller asked for again, and drops the next one in its place', async () => {
    const readSize = vi.fn<FileImageSizeReader>(async () => ({ width: 400, height: 200 }))
    const images = createFileImageResolver(anImage, readSize)
    for (let i = 0; i < FILE_IMAGE_SIZE_CACHE_MAX; i++)
      await images.loadSize(`image-${i}`)
    readSize.mockClear()

    // The first size is the least recently asked one. A second ask makes it the most recent.
    await images.loadSize('image-0')
    expect(readSize).not.toHaveBeenCalled()
    await images.loadSize('one-more')

    expect(images.peekSize('image-0')).toEqual({ width: 400, height: 200 })
    expect(images.peekSize('image-1')).toBeUndefined()
  })

  it('counts a file with no size against the limit like any other answer', async () => {
    const images = createFileImageResolver(anImage, noSize)
    for (let i = 0; i <= FILE_IMAGE_SIZE_CACHE_MAX; i++)
      await images.loadSize(`image-${i}`)

    expect(images.peekSize('image-0')).toBeUndefined()
    expect(images.peekSize('image-1')).toBeNull()
  })

  it('keeps more sizes than the largest loaded window has rows', () => {
    // Otherwise a window of image rows would drop the size of a row that is still loaded.
    expect(FILE_IMAGE_SIZE_CACHE_MAX).toBeGreaterThan(MAX_LOADED_CHAT_MESSAGES_CEILING)
  })
})
