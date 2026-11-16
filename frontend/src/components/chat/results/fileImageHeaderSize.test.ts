import type { FileImageSizeAnswer, FileImageSource } from '../fileImageResolver'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { createComputed, createRoot, createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred, settle } from '~/test-support/async'
import { NO_FILE_IMAGES } from '../fileImageResolver'
import { createFileImageHeaderSize } from './fileImageHeaderSize'

const SIZE: ImageDimensions = { width: 400, height: 200 }
const OTHER_SIZE: ImageDimensions = { width: 100, height: 300 }
const KNOWN_SIZE: FileImageSizeAnswer = { kind: 'known', size: SIZE }
const UNKNOWN_SIZE: FileImageSizeAnswer = { kind: 'unknown' }

/** A size read that the case ends. */
type SizeRead = ReturnType<typeof deferred<ImageDimensions | undefined>>

const disposers: Array<() => void> = []
afterEach(() => disposers.splice(0).forEach(dispose => dispose()))

/** The image that a case starts with: its file, and whether its source states its size. */
interface InitialImage {
  path: string | undefined
  stated: boolean
}

/**
 * The answer of the unit for a file image, over image files whose size cache knows
 * nothing unless the case says so. The case changes the path and the statement of the
 * source through the returned setters.
 */
function mountHeaderSize(files: Partial<FileImageSource>, initial: InitialImage = { path: '/image.png', stated: false }) {
  return createRoot((dispose) => {
    disposers.push(dispose)
    const [path, setPath] = createSignal(initial.path)
    const [stated, setStated] = createSignal(initial.stated)
    const source: FileImageSource = {
      ...NO_FILE_IMAGES,
      peekSize: () => UNKNOWN_SIZE,
      ...files,
    }
    const headerSize = createFileImageHeaderSize({
      path,
      stated,
      files: () => source,
    })
    return { headerSize, setPath, setStated }
  })
}

describe('createFileImageHeaderSize', () => {
  it('answers the size that its read gave', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const loadSize = vi.fn(() => size.promise)
    const { headerSize } = mountHeaderSize({ loadSize })
    expect(headerSize()).toEqual(UNKNOWN_SIZE)
    expect(loadSize).toHaveBeenCalledExactlyOnceWith('/image.png')

    size.resolve(SIZE)
    await settle()

    expect(headerSize()).toEqual(KNOWN_SIZE)
  })

  it.each([
    [
      'gave no size',
      (size: SizeRead) => size.resolve(undefined),
    ],
    [
      'failed',
      (size: SizeRead) => size.reject(new Error('the worker is unavailable')),
    ],
  ])('answers "no size" for a read that %s', async (_end, end) => {
    const size = deferred<ImageDimensions | undefined>()
    const { headerSize } = mountHeaderSize({
      loadSize: () => size.promise,
    })

    end(size)
    await settle()

    // Only the image gives the height then, so the view reads it.
    expect(headerSize()).toEqual({ kind: 'none' })
  })

  it('answers the size that the cache holds in its first read, and still asks for it', () => {
    const loadSize = vi.fn(() => new Promise<ImageDimensions | undefined>(() => {}))
    const { headerSize } = mountHeaderSize({
      peekSize: () => KNOWN_SIZE,
      loadSize,
    })

    expect(headerSize()).toEqual(KNOWN_SIZE)
    // The resolver answers this ask from its cache, and counts it for the order in which
    // it drops sizes.
    expect(loadSize).toHaveBeenCalledExactlyOnceWith('/image.png')
  })

  it('keeps the size that it found in the cache when the cache drops it', () => {
    const [cached, setCached] = createSignal<FileImageSizeAnswer>(KNOWN_SIZE)
    const { headerSize } = mountHeaderSize({
      peekSize: () => cached(),
      loadSize: () => new Promise<ImageDimensions | undefined>(() => {}),
    })

    setCached(UNKNOWN_SIZE)

    expect(headerSize()).toEqual(KNOWN_SIZE)
  })

  it('keeps its own answer when the cache learns another size afterwards', async () => {
    let cached: FileImageSizeAnswer = UNKNOWN_SIZE
    const { headerSize } = mountHeaderSize({
      peekSize: () => cached,
      loadSize: async () => undefined,
    })
    await settle()
    expect(headerSize()).toEqual({ kind: 'none' })

    // An image read of another copy states the size, and the resolver keeps it.
    cached = KNOWN_SIZE

    // A view that decided to read its image keeps that decision.
    expect(headerSize()).toEqual({ kind: 'none' })
  })

  it('reads the cache with no subscription before its own answer arrives', () => {
    const [cached, setCached] = createSignal<FileImageSizeAnswer>(UNKNOWN_SIZE)
    const { headerSize } = mountHeaderSize({
      peekSize: () => cached(),
      loadSize: () => new Promise<ImageDimensions | undefined>(() => {}),
    })
    let runs = 0
    createRoot((dispose) => {
      disposers.push(dispose)
      createComputed(() => {
        headerSize()
        runs++
      })
    })

    setCached(KNOWN_SIZE)

    expect(runs).toBe(1)
  })

  it('reads the size again for a new path, and ignores the read of the old path', async () => {
    const reads = new Map<string, SizeRead>()
    const loadSize = vi.fn((path: string) => {
      const read = deferred<ImageDimensions | undefined>()
      reads.set(path, read)
      return read.promise
    })
    const { headerSize, setPath } = mountHeaderSize({ loadSize }, { path: '/first.png', stated: false })

    setPath('/second.png')
    expect(loadSize.mock.calls).toEqual([
      ['/first.png'],
      ['/second.png'],
    ])
    reads.get('/second.png')!.resolve(OTHER_SIZE)
    await settle()
    expect(headerSize()).toEqual({ kind: 'known', size: OTHER_SIZE })

    // The read of the old path settles last. Its answer must not replace the answer of
    // the path that the view shows.
    reads.get('/first.png')!.resolve(SIZE)
    await settle()
    expect(headerSize()).toEqual({ kind: 'known', size: OTHER_SIZE })
  })

  it('reads no size for an image whose source states its size, and reads it once the source does not', () => {
    const loadSize = vi.fn(() => new Promise<ImageDimensions | undefined>(() => {}))
    const { setStated } = mountHeaderSize({ loadSize }, { path: '/image.png', stated: true })
    expect(loadSize).not.toHaveBeenCalled()

    setStated(false)

    expect(loadSize).toHaveBeenCalledExactlyOnceWith('/image.png')
  })

  it('answers "unknown" for an image that has no file, and reads nothing', () => {
    const loadSize = vi.fn(async () => SIZE)
    const { headerSize } = mountHeaderSize({
      loadSize,
      peekSize: () => KNOWN_SIZE,
    }, { path: undefined, stated: false })

    expect(headerSize()).toEqual(UNKNOWN_SIZE)
    expect(loadSize).not.toHaveBeenCalled()
  })

  it('answers "unknown" for a view that has no image files', () => {
    const headerSize = createRoot((dispose) => {
      disposers.push(dispose)
      return createFileImageHeaderSize({
        path: () => '/image.png',
        stated: () => false,
        files: () => undefined,
      })
    })

    expect(headerSize()).toEqual(UNKNOWN_SIZE)
  })
})
