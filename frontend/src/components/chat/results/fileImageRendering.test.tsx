import type { ImageRenderActions } from '../renderContext'
import type { RenderReadinessScope } from '../renderReadiness'
import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { fireEvent, render, waitFor, within } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { classSelector } from '~/test-support/composedClass'
import { expectReleasedAtRequestLimit } from '~/test-support/renderReadiness'
import { createFileImageResolver, FILE_IMAGE_SIZE_CACHE_MAX, FileImageRefusedError } from '../fileImageResolver'
import { createRenderReadinessScope, READINESS_REQUEST_LIMIT_MS } from '../renderReadiness'
import { toolImageBox, toolImageBoxFailed } from '../toolStyles.css'
import { imageReservationStyle, ImageResultView } from './imageResult'

const image = { data: 'iVBORw0KGgo=', mimeType: 'image/png' }
const SIZE: ImageDimensions = { width: 400, height: 200 }

/**
 * The actions of an image view, for a row in the list near the view: no read is
 * deferred, and no image and no size is known. An ask for the size answers what the size
 * cache of the case holds, as the resolver does: with no cache, the size read gives none.
 */
function imageActions(overrides: Partial<ImageRenderActions> = {}): ImageRenderActions {
  const cachedFileImageSize = overrides.cachedFileImageSize ?? (() => undefined)
  return {
    loadFileImage: async () => undefined,
    cachedFileImage: () => undefined,
    loadFileImageSize: async path => cachedFileImageSize(path) ?? undefined,
    cachedFileImageSize,
    readiness: undefined,
    openImage: () => {},
    deferLoad: () => false,
    premeasurePass: () => false,
    ...overrides,
  }
}

/** The inline style that `element` carries, as the properties that a reservation sets. */
function reservation(element: Element | null): Record<string, string> {
  if (!(element instanceof HTMLElement))
    throw new TypeError('The element is absent')
  return {
    'aspect-ratio': element.style.getPropertyValue('aspect-ratio'),
    'width': element.style.getPropertyValue('width'),
  }
}

const box = (container: HTMLElement) => container.querySelector(classSelector(toolImageBox))

/**
 * The reservation of an image of `size`, as an element carries it. jsdom rewrites a CSS
 * value when it stores it, so the expected value goes through an element also.
 */
function reservationFor(size: ImageDimensions): Record<string, string> {
  const probe = document.createElement('div')
  for (const [property, value] of Object.entries(imageReservationStyle(size)))
    probe.style.setProperty(property, value)
  return reservation(probe)
}

/** The source of the image that most cases mount: a file image that states no size. */
const FILE_IMAGE: ImageResultSource = { filePath: '/image.png' }

/**
 * Mount a view of `source` with `actions` over the defaults of `imageActions`. A case whose
 * source changes renders the view itself, so that the view reads the source reactively.
 */
function mountImage(actions: Partial<ImageRenderActions> = {}, source: ImageResultSource = FILE_IMAGE) {
  return render(() => <ImageResultView source={source} actions={imageActions(actions)} />)
}

/** Mount a view with a readiness scope, as a row copy gives it. */
function mountWithScope(actions: Partial<ImageRenderActions>, source: ImageResultSource = FILE_IMAGE) {
  let scope!: RenderReadinessScope
  const view = render(() => {
    scope = createRenderReadinessScope()
    return <ImageResultView source={source} actions={imageActions({ ...actions, readiness: scope })} />
  })
  return { ...view, scope }
}

describe('file image rendering', () => {
  it('waits for scrolling to finish before changing the row geometry', async () => {
    const [paused, setPaused] = createSignal(true)
    const read = vi.fn(async () => image)
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({ loadFileImage: read })}
        holdDisplay={() => paused()}
      />
    ))
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    await Promise.resolve()
    await Promise.resolve()
    expect(container.querySelector('img')).toBeNull()
    setPaused(false)
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('src')).toBe(`data:image/png;base64,${image.data}`))
  })

  it('shows a cached image in the first render of a hidden copy, and asks for it once', async () => {
    const read = vi.fn(async () => image)
    const { container } = mountImage({
      loadFileImage: read,
      cachedFileImage: () => image,
      // The header of the file gave no size, so only the image gives the height.
      cachedFileImageSize: () => null,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    expect(container.querySelector('img')?.getAttribute('src')).toContain(image.data)
    await Promise.resolve()
    await Promise.resolve()

    // The view asks for the image that it shows, so that it keeps the image when the
    // resolver drops it. The resolver answers an ask for an image that it holds with no
    // request (see the tests of the resolver). The copy never claims a read meanwhile.
    expect(read).toHaveBeenCalledOnce()
    expect(container.querySelector('img')?.getAttribute('src')).toContain(image.data)
    expect(container.textContent).not.toContain('Loading image')
  })

  it('ignores a file read after the source changes', async () => {
    const [path, setPath] = createSignal('/first.png')
    let finishFirst!: (value: typeof image) => void
    const read = vi.fn(async (file: string) => file === '/first.png'
      ? new Promise<typeof image>((resolve) => { finishFirst = resolve })
      : { ...image, data: 'BBBB' })
    const { container } = render(() => (
      <ImageResultView
        source={{
          filePath: path(),
        }}
        actions={imageActions({ loadFileImage: read })}
      />
    ))
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    setPath('/second.png')
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,BBBB'))
    finishFirst(image)
    await Promise.resolve()
    expect(container.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,BBBB')
  })
})

// `loadFile` refuses to read during a premeasure pass and for an offscreen row,
// while the display model still reported `loading`. The row then said "Loading
// image..." for a read nobody started, and Retry hides for as long as `loading`
// holds -- so the row offered no way out. See SCAN-S8-5.
describe('a file image the row cannot read yet', () => {
  it('claims no read during a premeasure pass of an image whose size is known', async () => {
    const read = vi.fn(async () => image)
    const { container } = mountImage({
      loadFileImage: read,
      cachedFileImageSize: () => SIZE,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    await Promise.resolve()
    expect(read).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('Loading image')
  })

  it('claims no read of an image whose size is known while the row is offscreen, and reads once it arrives', async () => {
    const [offscreen, setOffscreen] = createSignal(true)
    const read = vi.fn(async () => image)
    const { container } = mountImage({
      loadFileImage: read,
      cachedFileImageSize: () => SIZE,
      deferLoad: offscreen,
    })
    await Promise.resolve()
    await Promise.resolve()
    expect(read).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('Loading image')
    setOffscreen(false)
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())
  })

  it('checks the wait again when its test runs again, and reads once', async () => {
    // In the chat, the test for a row that is far from the view reads the scroll position
    // with no subscription. It runs again when the scroll-activity window of the view
    // changes, which this signal stands for.
    let offscreen = true
    const [scrollActivity, setScrollActivity] = createSignal(0)
    const read = vi.fn(async () => image)
    mountImage({
      loadFileImage: read,
      cachedFileImageSize: () => SIZE,
      deferLoad: () => {
        scrollActivity()
        return offscreen
      },
    })
    await Promise.resolve()
    expect(read).not.toHaveBeenCalled()

    // The row came near the view, and nothing ran the test again yet.
    offscreen = false
    await Promise.resolve()
    expect(read).not.toHaveBeenCalled()

    setScrollActivity(1)
    await waitFor(() => expect(read).toHaveBeenCalledOnce())

    // The test runs again with the same answer: the read does not start again.
    setScrollActivity(2)
    await Promise.resolve()
    await Promise.resolve()
    expect(read).toHaveBeenCalledOnce()
  })

  it('still claims a read that is genuinely in flight', async () => {
    const read = vi.fn(() => new Promise<typeof image>(() => {}))
    const { container } = mountImage({ loadFileImage: read })
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    expect(container.textContent).toContain('Loading image')
  })
})

// A hidden premeasure copy measures a row before the reader reaches it. A file image
// that showed one line of text until its read finished made that measure wrong by the
// height of the image, and the row grew when the image arrived. The view now reads the
// size of the file first, and holds the box of the image in every state.
describe('a file image whose size is known before the image', () => {
  it('draws the box of the image for a deferred read, and reads nothing', async () => {
    const read = vi.fn(async () => image)
    const readSize = vi.fn(async () => SIZE)
    const { container } = mountImage({
      loadFileImage: read,
      loadFileImageSize: readSize,
      deferLoad: () => true,
    })
    await waitFor(() => expect(box(container)).not.toBeNull())

    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    // No read runs, so the box claims none.
    expect(container.textContent).not.toContain('Loading image')
    expect(readSize).toHaveBeenCalledExactlyOnceWith('/image.png')
    expect(read).not.toHaveBeenCalled()
  })

  it('reads the size once for a path', async () => {
    const [offscreen, setOffscreen] = createSignal(true)
    const readSize = vi.fn(async () => SIZE)
    const { container } = mountImage({
      loadFileImage: () => new Promise<undefined>(() => {}),
      loadFileImageSize: readSize,
      deferLoad: offscreen,
    })
    await waitFor(() => expect(box(container)).not.toBeNull())
    setOffscreen(false)
    await waitFor(() => expect(container.textContent).toContain('Loading image'))

    expect(readSize).toHaveBeenCalledOnce()
  })

  it('reads no size for an image that has no file path', async () => {
    const readSize = vi.fn(async () => SIZE)
    mountImage({ loadFileImageSize: readSize }, image)
    await Promise.resolve()
    expect(readSize).not.toHaveBeenCalled()
  })

  it('gives the image the box that it reserved', async () => {
    const [offscreen, setOffscreen] = createSignal(true)
    const read = vi.fn(async () => ({ ...image, dimensions: SIZE }))
    const { container } = mountImage({
      loadFileImage: read,
      loadFileImageSize: async () => SIZE,
      deferLoad: offscreen,
    })
    await waitFor(() => expect(box(container)).not.toBeNull())
    const reserved = reservation(box(container))

    setOffscreen(false)
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())

    expect(reservation(container.querySelector('img'))).toEqual(reserved)
    expect(box(container)).toBeNull()
  })

  it('keeps the box, with the error and a retry, when the read of the image fails', async () => {
    const read = vi.fn(async (): Promise<undefined> => {
      throw new Error('The image file is empty')
    })
    const { container, getByRole } = mountImage({
      loadFileImage: read,
      cachedFileImageSize: () => SIZE,
    })
    await waitFor(() => expect(container.textContent).toContain('The image file is empty'))

    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    expect(box(container)!.textContent).toContain('The image file is empty')
    expect(box(container)!.contains(getByRole('button', { name: 'Retry image' }))).toBe(true)
  })

  it('lets the box grow to its error and its retry when the read of the image fails', async () => {
    const read = vi.fn(async (): Promise<undefined> => {
      throw new Error('The image file is empty')
    })
    const { container } = mountImage({
      loadFileImage: read,
      cachedFileImageSize: () => ({ width: 16, height: 16 }),
    })
    // While the read runs, the box has the size of the image, and cuts its text.
    expect(box(container)?.matches(classSelector(toolImageBoxFailed))).toBe(false)

    await waitFor(() => expect(container.textContent).toContain('The image file is empty'))

    // A box of 16px cannot hold the error and the button, so it must not cut them.
    expect(box(container)?.matches(classSelector(toolImageBoxFailed))).toBe(true)
  })

  it('states the image as the content of a box that shows no status', () => {
    const { container } = mountImage({
      cachedFileImageSize: () => SIZE,
      deferLoad: () => true,
    }, { filePath: '/image.png', description: 'A chart' })

    expect(box(container)?.getAttribute('role')).toBe('img')
    expect(box(container)?.getAttribute('aria-label')).toBe('A chart')
  })

  it('gives a box that shows its status no image role, so the retry stays reachable', async () => {
    const read = vi.fn(async (): Promise<undefined> => {
      throw new Error('The image file is empty')
    })
    const { container, getByRole } = mountImage({
      loadFileImage: read,
      cachedFileImageSize: () => SIZE,
    })
    await waitFor(() => expect(container.textContent).toContain('The image file is empty'))

    expect(box(container)?.hasAttribute('role')).toBe(false)
    expect(getByRole('button', { name: 'Retry image' })).not.toBeNull()
  })

  it('keeps the size that a view found in the cache as a size that a caller asked for', async () => {
    const images = createFileImageResolver(async () => image, async () => SIZE)
    await images.loadSize('/a.png')
    for (let i = 1; i < FILE_IMAGE_SIZE_CACHE_MAX; i++)
      await images.loadSize(`/other-${i}.png`)
    // The cache is full, and `/a.png` is its oldest size. A view finds it there.
    mountImage({
      loadFileImageSize: path => images.loadSize(path),
      cachedFileImageSize: path => images.peekSize(path),
      deferLoad: () => true,
    }, { filePath: '/a.png' })
    await Promise.resolve()

    await images.loadSize('/one-more.png')

    // The resolver drops the size that no caller asked for the longest: not this one.
    expect(images.peekSize('/a.png')).toEqual(SIZE)
    expect(images.peekSize('/other-1.png')).toBeUndefined()
  })

  it('shows the box in the first render for a size that the cache holds', () => {
    const readSize = vi.fn(async () => SIZE)
    const { scope, container } = mountWithScope({
      loadFileImageSize: readSize,
      cachedFileImageSize: () => SIZE,
      deferLoad: () => true,
    })

    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    // The cache gives the size, so the copy never waits for a read.
    expect(scope.measurable()).toBe(true)
    // The view still asks for the size: the resolver answers that ask from its cache, and
    // counts it for the order in which it drops sizes.
    expect(readSize).toHaveBeenCalledExactlyOnceWith('/image.png')
  })

  it('keeps the box when the cache drops the size that the view found there', async () => {
    // The resolver drops its least recently used sizes at its limit, and every size at a
    // clear. The row shows the box, and its height must not change for that.
    const [cached, setCached] = createSignal<ImageDimensions | undefined>(SIZE)
    const readSize = vi.fn(async () => SIZE)
    const { container } = mountImage({
      loadFileImageSize: readSize,
      cachedFileImageSize: cached,
      deferLoad: () => true,
    })
    expect(reservation(box(container))).toEqual(reservationFor(SIZE))

    setCached(undefined)

    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    await Promise.resolve()
    await Promise.resolve()
    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    // The one ask at the mount. The drop starts no read.
    expect(readSize).toHaveBeenCalledOnce()
  })

  it('reads the size again for a new path after the cache dropped the size of the old one', async () => {
    const [cached, setCached] = createSignal<ImageDimensions | undefined>(SIZE)
    const [path, setPath] = createSignal('/first.png')
    const readSize = vi.fn(async (file: string) => file === '/first.png'
      ? SIZE
      : { width: 100, height: 300 })
    const { container } = render(() => (
      <ImageResultView
        source={{
          filePath: path(),
        }}
        actions={imageActions({
          loadFileImageSize: readSize,
          cachedFileImageSize: cached,
          deferLoad: () => true,
        })}
      />
    ))
    setCached(undefined)

    setPath('/second.png')

    // The copy of the size belongs to the first path, so the second path reads its own.
    await waitFor(() => expect(reservation(box(container))).toEqual(reservationFor({ width: 100, height: 300 })))
    expect(readSize.mock.calls).toEqual([
      ['/first.png'],
      ['/second.png'],
    ])
  })

  it('keeps the description below the box, as below the image', async () => {
    const { container } = mountImage({
      cachedFileImageSize: () => SIZE,
      deferLoad: () => true,
    }, { filePath: '/image.png', description: 'A screenshot' })

    expect(box(container)).not.toBeNull()
    expect(box(container)!.textContent).not.toContain('A screenshot')
    expect(container.textContent).toContain('A screenshot')
  })
})

describe('an image with a stated size that cannot show', () => {
  it('shows the label of an image that has no data and no file, in place of an empty box', () => {
    const { container } = mountImage({}, { mimeType: 'image/png', dimensions: SIZE })

    expect(container.textContent).toContain('[image: image/png]')
    expect(box(container)).toBeNull()
  })

  it('shows why an inline image of a refused format does not show', () => {
    const { container } = mountImage({}, { data: 'AAAA', mimeType: 'image/tiff', dimensions: SIZE })

    expect(container.textContent).toContain('unsupported format')
    expect(box(container)).toBeNull()
  })

  it('keeps the link of an external image that states its size', () => {
    const { container } = mountImage({}, { url: 'https://example.com/a.png', mimeType: 'image/png', dimensions: SIZE })

    expect(container.querySelector('a')?.getAttribute('href')).toBe('https://example.com/a.png')
    expect(box(container)).toBeNull()
  })
})

describe('the copies of one file image', () => {
  // A hidden copy measures the row, and the row in the list then draws the same image.
  // Both must draw the same content, or the row takes another height than its measure.
  const fail = async (): Promise<undefined> => {
    throw new Error('The file does not exist')
  }

  it('draws the failed read of an image with no size in a row far from the view as in its hidden copy', async () => {
    const copy = mountImage({
      loadFileImage: fail,
      cachedFileImageSize: () => null,
      deferLoad: () => true,
      premeasurePass: () => true,
    }, { filePath: '/gone.png' })
    await waitFor(() => expect(copy.container.textContent).toContain('Retry image'))

    const row = mountImage({
      loadFileImage: fail,
      cachedFileImageSize: () => null,
      deferLoad: () => true,
    }, { filePath: '/gone.png' })

    await waitFor(() => expect(row.container.textContent).toBe(copy.container.textContent))
  })

  it('draws an image with no size in a row far from the view after the resolver dropped it', async () => {
    const read = vi.fn(async () => image)
    // The hidden copy read the image, and the resolver dropped it at its limit since.
    const row = mountImage({
      loadFileImage: read,
      cachedFileImage: () => undefined,
      cachedFileImageSize: () => null,
      deferLoad: () => true,
    }, { filePath: '/image.svg' })

    // Only the image gives the height of this row, so the row reads it wherever it is.
    await waitFor(() => expect(row.container.querySelector('img')).not.toBeNull())
    expect(row.container.textContent).not.toContain('[image]')
  })

  /** The actions of an image view over a real resolver, as a chat row assembles them. */
  function resolverActions(images: ReturnType<typeof createFileImageResolver>, overrides: Partial<ImageRenderActions>): ImageRenderActions {
    return imageActions({
      loadFileImage: (path, options) => images.load(path, options),
      cachedFileImage: path => images.peek(path),
      loadFileImageSize: path => images.loadSize(path),
      cachedFileImageSize: path => images.peekSize(path),
      ...overrides,
    })
  }

  it('draws the refusal of a file in each copy from one read, and reads again for Retry', async () => {
    const read = vi.fn(async (): Promise<typeof image> => {
      throw new FileImageRefusedError('The image file format is not supported')
    })
    const images = createFileImageResolver(read, async () => undefined)
    const copy = render(() => (
      <ImageResultView
        source={{ filePath: '/photo.bmp' }}
        actions={resolverActions(images, {
          deferLoad: () => true,
          premeasurePass: () => true,
        })}
      />
    ))
    await waitFor(() => expect(copy.container.textContent).toContain('not supported'))

    const row = render(() => (
      <ImageResultView
        source={{ filePath: '/photo.bmp' }}
        actions={resolverActions(images, {
          deferLoad: () => true,
        })}
      />
    ))
    await waitFor(() => expect(row.container.textContent).toBe(copy.container.textContent))
    // The same file gives the same refusal, so the second copy cost no read.
    expect(read).toHaveBeenCalledOnce()

    fireEvent.click(within(row.container).getByRole('button', { name: 'Retry image' }))
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2))
  })

  it('reserves the box of an image in a later copy from the size that the image stated', async () => {
    const read = vi.fn(async () => ({ ...image, dimensions: SIZE }))
    // The size read fails, so the first copy has no size until it read the image. The
    // resolver holds one image at most.
    const images = createFileImageResolver(read, async () => {
      throw new Error('the worker is unavailable')
    }, image.data.length)
    const copy = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={resolverActions(images, {
          deferLoad: () => true,
          premeasurePass: () => true,
        })}
      />
    ))
    await waitFor(() => expect(copy.container.querySelector('img')).not.toBeNull())
    const measured = reservation(copy.container.querySelector('img'))
    expect(measured).toEqual(reservationFor(SIZE))

    // The resolver drops the image at its limit. It keeps the size.
    await images.load('/other.png')
    expect(images.peek('/image.png')).toBeUndefined()
    read.mockClear()
    const row = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={resolverActions(images, {
          deferLoad: () => true,
        })}
      />
    ))

    // The row far from the view takes the box that the copy measured, and reads no image.
    expect(reservation(box(row.container))).toEqual(measured)
    await Promise.resolve()
    await Promise.resolve()
    expect(read).not.toHaveBeenCalled()
  })

  it('keeps its own copy of an image whose read states its size, when the resolver drops it afterwards', async () => {
    // The size read fails, so only the image gives the height. The resolver holds one image.
    const images = createFileImageResolver(async () => ({ ...image, dimensions: SIZE }), async () => {
      throw new Error('the worker is unavailable')
    }, image.data.length)
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={resolverActions(images, {
          deferLoad: () => true,
          premeasurePass: () => true,
        })}
      />
    ))
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())

    // The resolver learned the size from the image, and drops the image at its limit.
    await images.load('/other.png')
    expect(images.peek('/image.png')).toBeUndefined()
    await Promise.resolve()

    // The copy read the image itself, so it keeps the image for its measure.
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('keeps its own copy of an image that it read, when the resolver drops it afterwards', async () => {
    // The read of a hidden copy fills the cache of the resolver, and the resolver can
    // drop the image again while the copy still waits for its measure.
    const [cached, setCached] = createSignal<typeof image>()
    const read = vi.fn(async () => {
      setCached(image)
      return image
    })
    const { container } = mountImage({
      loadFileImage: read,
      cachedFileImage: () => cached(),
      cachedFileImageSize: () => null,
      deferLoad: () => true,
      premeasurePass: () => true,
    }, { filePath: '/image.svg' })
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())

    setCached(undefined)
    await Promise.resolve()

    expect(container.querySelector('img')).not.toBeNull()
    expect(read).toHaveBeenCalledOnce()
  })
})

describe('a file image whose header gives no size', () => {
  // The copy has no box to reserve, so only the image gives its height. Each copy reads
  // the image for that reason, wherever its row is.
  it.each([
    ['a hidden copy', true],
    ['a row in the list that is far from the view', false],
  ])('reads the whole image in %s', async (_copy, premeasure) => {
    const read = vi.fn(async () => image)
    const { container } = mountImage({
      loadFileImage: read,
      loadFileImageSize: async () => undefined,
      deferLoad: () => true,
      premeasurePass: () => premeasure,
    })

    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())
    expect(read).toHaveBeenCalledOnce()
    // It does not draw the one line of an image that waits, where another copy of the
    // row measured the image.
    expect(container.textContent).not.toContain('[image]')
    expect(box(container)).toBeNull()
  })

  it('reads nothing in a hidden copy while its size read runs', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const read = vi.fn(async () => image)
    const { container } = mountImage({
      loadFileImage: read,
      loadFileImageSize: () => size.promise,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    await Promise.resolve()

    // The header can still give the size, and the copy then reserves the box with no image.
    expect(read).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('Loading image')

    size.resolve(undefined)
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
  })

  it('reads nothing in a hidden copy whose source states the size', async () => {
    const read = vi.fn(async () => image)
    const { container } = mountImage({
      loadFileImage: read,
      cachedFileImageSize: () => null,
      deferLoad: () => true,
      premeasurePass: () => true,
    }, { filePath: '/image.png', dimensions: SIZE })
    await Promise.resolve()

    // The source states the size, so the copy reserves the box and reads nothing.
    expect(read).not.toHaveBeenCalled()
    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
  })
})

describe('the readiness that a file image reports', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('holds a row in the list while its read of an image with no size runs', async () => {
    const { scope, container } = mountWithScope({
      loadFileImageSize: async () => undefined,
      loadFileImage: () => new Promise(() => {}),
      deferLoad: () => false,
    })
    await waitFor(() => expect(container.textContent).toContain('Loading image'))

    // The row shows one line now, and the image gives it another height.
    expect(scope.measurable()).toBe(false)
  })

  it.each([
    ['a hidden copy', true],
    ['a row in the list', false],
  ])('holds nothing for %s whose source states the size, and reads no size', async (_copy, premeasure) => {
    const readSize = vi.fn(() => new Promise<ImageDimensions | undefined>(() => {}))
    const { scope, container } = mountWithScope({
      loadFileImageSize: readSize,
      deferLoad: () => true,
      premeasurePass: () => premeasure,
    }, { filePath: '/image.png', dimensions: SIZE })
    await Promise.resolve()

    // The source gives the box, so the header of the file can add nothing.
    expect(scope.measurable()).toBe(true)
    expect(readSize).not.toHaveBeenCalled()
    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
  })

  it('holds the copy while the size read runs', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const { scope, container } = mountWithScope({
      loadFileImageSize: () => size.promise,
      deferLoad: () => true,
    })
    await Promise.resolve()
    expect(scope.measurable()).toBe(false)

    size.resolve(SIZE)
    await waitFor(() => expect(scope.measurable()).toBe(true))
    expect(box(container)).not.toBeNull()
  })

  it('releases the copy when the size read rejects and its read of the image settles', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const { scope, container } = mountWithScope({
      loadFileImage: async () => image,
      loadFileImageSize: () => size.promise,
      deferLoad: () => true,
    })
    await Promise.resolve()
    expect(scope.measurable()).toBe(false)

    // A size read that failed gives no size, so the copy reads the image for its height.
    size.reject(new Error('the worker is unavailable'))
    await waitFor(() => expect(scope.measurable()).toBe(true))
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('releases the copy at the limit for a size read that does not settle', async () => {
    vi.useFakeTimers()
    const size = deferred<ImageDimensions | undefined>()
    const { scope, container } = mountWithScope({
      loadFileImageSize: () => size.promise,
      deferLoad: () => true,
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(scope.measurable()).toBe(false)

    await expectReleasedAtRequestLimit(scope)
    // The copy reports the height that it has: the one line, with no box.
    expect(box(container)).toBeNull()

    // The limit aborted no request: the size still arrives, and the box shows.
    size.resolve(SIZE)
    await vi.advanceTimersByTimeAsync(0)
    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    expect(scope.measurable()).toBe(true)
  })

  it('counts the size read and the read of the image in a hidden copy as one wait', async () => {
    vi.useFakeTimers()
    const size = deferred<ImageDimensions | undefined>()
    const read = vi.fn(() => new Promise<undefined>(() => {}))
    const { scope } = mountWithScope({
      loadFileImage: read,
      loadFileImageSize: () => size.promise,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    await vi.advanceTimersByTimeAsync(6000)
    size.resolve(undefined)
    await vi.advanceTimersByTimeAsync(0)

    // The header gave no size, so the copy reads the image, and still waits.
    expect(read).toHaveBeenCalledOnce()
    expect(scope.measurable()).toBe(false)

    // One limit for both reads: it ends 10 seconds after the start of the size read.
    await expectReleasedAtRequestLimit(scope, 6000)
  })

  /** Where a copy of a row can be, as the actions of its image view state it. */
  const COPIES: [string, Partial<ImageRenderActions>][] = [
    [
      'a hidden copy',
      {
        deferLoad: () => true,
        premeasurePass: () => true,
      },
    ],
    [
      'a row in the list that is far from the view',
      {
        deferLoad: () => true,
      },
    ],
    [
      'a row in the list near the view',
      {
        deferLoad: () => false,
      },
    ],
  ]

  it.each(COPIES)('releases %s when its read of the image settles', async (_copy, place) => {
    const whole = deferred<typeof image>()
    const { scope, container } = mountWithScope({
      loadFileImage: () => whole.promise,
      loadFileImageSize: async () => undefined,
      ...place,
    })
    await waitFor(() => expect(container.textContent).toContain('Loading image'))
    expect(scope.measurable()).toBe(false)

    whole.resolve(image)
    await waitFor(() => expect(scope.measurable()).toBe(true))
    expect(container.querySelector('img')).not.toBeNull()
  })

  it.each(COPIES)('releases %s when its read of the image fails', async (_copy, place) => {
    const whole = deferred<typeof image>()
    const { scope, container } = mountWithScope({
      loadFileImage: () => whole.promise,
      loadFileImageSize: async () => undefined,
      ...place,
    })
    await waitFor(() => expect(container.textContent).toContain('Loading image'))
    expect(scope.measurable()).toBe(false)

    whole.reject(new Error('The image file is too large to preview'))
    await waitFor(() => expect(scope.measurable()).toBe(true))
    expect(container.textContent).toContain('The image file is too large to preview')
  })

  // A later copy of a row finds the answer of the first size read in the cache: the
  // header gives no size, as for an SVG. No size read holds it, and it must still wait
  // for its read of the image, or it measures the one line "Loading image…".
  it.each(COPIES)('holds %s for its read of the image when the cache holds no size for the file', async (_copy, place) => {
    const whole = deferred<typeof image>()
    const { scope, container } = mountWithScope({
      loadFileImage: () => whole.promise,
      cachedFileImageSize: () => null,
      ...place,
    })
    await waitFor(() => expect(container.textContent).toContain('Loading image'))
    expect(scope.measurable()).toBe(false)

    whole.resolve(image)
    await waitFor(() => expect(scope.measurable()).toBe(true))
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('holds nothing for a read that the reader started with Retry', async () => {
    const reads = [
      deferred<typeof image>(),
      deferred<typeof image>(),
    ]
    let asked = 0
    const { scope, container } = mountWithScope({
      loadFileImage: () => reads[asked++]!.promise,
      cachedFileImageSize: () => null,
    })
    reads[0]!.reject(new Error('The worker did not answer'))
    await waitFor(() => expect(scope.measurable()).toBe(true))

    fireEvent.click(within(container).getByRole('button', { name: 'Retry image' }))

    // A row that reports a wait is hidden. The reader pressed a button of this row, so the
    // row stays in view, with its status.
    expect(container.textContent).toContain('Loading image')
    expect(scope.measurable()).toBe(true)

    reads[1]!.resolve(image)
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())
    expect(scope.measurable()).toBe(true)
  })

  it('releases that hidden copy at the limit when its read of the image does not settle', async () => {
    vi.useFakeTimers()
    const { scope } = mountWithScope({
      loadFileImage: () => new Promise<undefined>(() => {}),
      cachedFileImageSize: () => null,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(scope.measurable()).toBe(false)

    await expectReleasedAtRequestLimit(scope)
  })

  it.each(COPIES)('holds nothing for %s with no size when the cache holds the image', async (_copy, place) => {
    const whole = deferred<typeof image>()
    const { scope } = mountWithScope({
      loadFileImage: () => whole.promise,
      cachedFileImage: () => image,
      cachedFileImageSize: () => null,
      ...place,
    })

    // The copy shows the image from its first render, so it has its height. Its ask for
    // the image, which lets it keep the image, changes nothing of that.
    expect(scope.measurable()).toBe(true)
    await Promise.resolve()
    expect(scope.measurable()).toBe(true)
    whole.resolve(image)
    await Promise.resolve()
    await Promise.resolve()
    expect(scope.measurable()).toBe(true)
  })

  it('starts a new wait, with its own limit, for a new path', async () => {
    vi.useFakeTimers()
    const [path, setPath] = createSignal('/first.png')
    let scope!: RenderReadinessScope
    render(() => {
      scope = createRenderReadinessScope()
      return (
        <ImageResultView
          source={{
            filePath: path(),
          }}
          actions={imageActions({
            loadFileImageSize: () => new Promise<undefined>(() => {}),
            deferLoad: () => true,
            readiness: scope,
          })}
        />
      )
    })
    await vi.advanceTimersByTimeAsync(6000)
    expect(scope.measurable()).toBe(false)

    setPath('/second.png')
    // The rest of the limit of the first path: the wait of the second path goes on.
    await vi.advanceTimersByTimeAsync(READINESS_REQUEST_LIMIT_MS - 6000)
    expect(scope.measurable()).toBe(false)

    await vi.advanceTimersByTimeAsync(6000)
    expect(scope.measurable()).toBe(true)
  })
})
