import type { ImageRenderActions } from '../renderContext'
import type { RenderReadinessScope } from '../renderReadiness'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { classSelector } from '~/test-support/composedClass'
import { createRenderReadinessScope, READINESS_REQUEST_LIMIT_MS } from '../renderReadiness'
import { toolImageBox } from '../toolStyles.css'
import { imageReservationStyle, ImageResultView } from './imageResult'

const image = { data: 'iVBORw0KGgo=', mimeType: 'image/png' }
const SIZE: ImageDimensions = { width: 400, height: 200 }

/**
 * The actions of an image view, for a row in the list near the view: no read is
 * deferred, no image and no size is known, and the size read gives none.
 */
function imageActions(overrides: Partial<ImageRenderActions> = {}): ImageRenderActions {
  return {
    loadFileImage: async () => undefined,
    cachedFileImage: () => undefined,
    loadFileImageSize: async () => undefined,
    cachedFileImageSize: () => undefined,
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

  it('uses a cached image during measurement without starting another read', async () => {
    const read = vi.fn(async () => image)
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          cachedFileImage: () => image,
          // The header of the file gave no size, which makes a hidden copy read the image
          // unless the resolver holds it already.
          cachedFileImageSize: () => null,
          deferLoad: () => true,
          premeasurePass: () => true,
        })}
      />
    ))
    expect(container.querySelector('img')?.getAttribute('src')).toContain(image.data)
    await Promise.resolve()
    expect(read).not.toHaveBeenCalled()
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
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          cachedFileImageSize: () => SIZE,
          deferLoad: () => true,
          premeasurePass: () => true,
        })}
      />
    ))
    await Promise.resolve()
    expect(read).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('Loading image')
  })

  it('claims no read while the row is offscreen, and reads once it arrives', async () => {
    const [offscreen, setOffscreen] = createSignal(true)
    const read = vi.fn(async () => image)
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          deferLoad: offscreen,
        })}
      />
    ))
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
    render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          deferLoad: () => {
            scrollActivity()
            return offscreen
          },
        })}
      />
    ))
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
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({ loadFileImage: read })}
      />
    ))
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
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          loadFileImageSize: readSize,
          deferLoad: () => true,
        })}
      />
    ))
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
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: () => new Promise<undefined>(() => {}),
          loadFileImageSize: readSize,
          deferLoad: offscreen,
        })}
      />
    ))
    await waitFor(() => expect(box(container)).not.toBeNull())
    setOffscreen(false)
    await waitFor(() => expect(container.textContent).toContain('Loading image'))

    expect(readSize).toHaveBeenCalledOnce()
  })

  it('reads no size for an image that has no file path', async () => {
    const readSize = vi.fn(async () => SIZE)
    render(() => (
      <ImageResultView
        source={image}
        actions={imageActions({ loadFileImageSize: readSize })}
      />
    ))
    await Promise.resolve()
    expect(readSize).not.toHaveBeenCalled()
  })

  it('gives the image the box that it reserved', async () => {
    const [offscreen, setOffscreen] = createSignal(true)
    const read = vi.fn(async () => ({ ...image, dimensions: SIZE }))
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          loadFileImageSize: async () => SIZE,
          deferLoad: offscreen,
        })}
      />
    ))
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
    const { container, getByRole } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          cachedFileImageSize: () => SIZE,
        })}
      />
    ))
    await waitFor(() => expect(container.textContent).toContain('The image file is empty'))

    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    expect(box(container)!.textContent).toContain('The image file is empty')
    expect(box(container)!.contains(getByRole('button', { name: 'Retry image' }))).toBe(true)
  })

  it('shows the box in the first render for a size that the cache holds', () => {
    let scope: RenderReadinessScope | undefined
    const readSize = vi.fn(async () => SIZE)
    const { container } = render(() => {
      scope = createRenderReadinessScope()
      return (
        <ImageResultView
          source={{ filePath: '/image.png' }}
          actions={imageActions({
            loadFileImageSize: readSize,
            cachedFileImageSize: () => SIZE,
            readiness: scope,
            deferLoad: () => true,
          })}
        />
      )
    })

    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    // The size needs no read, so the copy never waits for one.
    expect(scope!.measurable()).toBe(true)
    expect(readSize).not.toHaveBeenCalled()
  })

  it('keeps the box when the cache drops the size that the view found there', () => {
    // The resolver drops its least recently used sizes at its limit, and every size at a
    // clear. The row shows the box, and its height must not change for that.
    const [cached, setCached] = createSignal<ImageDimensions | undefined>(SIZE)
    const readSize = vi.fn(async () => undefined)
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImageSize: readSize,
          cachedFileImageSize: cached,
          deferLoad: () => true,
        })}
      />
    ))
    expect(reservation(box(container))).toEqual(reservationFor(SIZE))

    setCached(undefined)

    expect(reservation(box(container))).toEqual(reservationFor(SIZE))
    expect(readSize).not.toHaveBeenCalled()
  })

  it('reads the size again for a new path after the cache dropped the size of the old one', async () => {
    const [cached, setCached] = createSignal<ImageDimensions | undefined>(SIZE)
    const [path, setPath] = createSignal('/first.png')
    const readSize = vi.fn(async () => ({ width: 100, height: 300 }))
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
    expect(readSize).toHaveBeenCalledExactlyOnceWith('/second.png')
  })

  it('keeps the description below the box, as below the image', async () => {
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png', description: 'A screenshot' }}
        actions={imageActions({
          cachedFileImageSize: () => SIZE,
          deferLoad: () => true,
        })}
      />
    ))

    expect(box(container)).not.toBeNull()
    expect(box(container)!.textContent).not.toContain('A screenshot')
    expect(container.textContent).toContain('A screenshot')
  })
})

describe('a file image whose header gives no size', () => {
  it('reads the whole image in a hidden copy', async () => {
    const read = vi.fn(async () => image)
    render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          loadFileImageSize: async () => undefined,
          deferLoad: () => true,
          premeasurePass: () => true,
        })}
      />
    ))
    // The copy must measure the row with its image: it has no box to reserve.
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
  })

  it('reads nothing for a row in the list that is far from the view', async () => {
    const read = vi.fn(async () => image)
    const readSize = vi.fn(async () => undefined)
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png' }}
        actions={imageActions({
          loadFileImage: read,
          loadFileImageSize: readSize,
          deferLoad: () => true,
        })}
      />
    ))
    await waitFor(() => expect(readSize).toHaveBeenCalledOnce())
    await Promise.resolve()

    expect(read).not.toHaveBeenCalled()
    // The one line that the row showed before: the placeholder of an image, with no box
    // and no claim of a read.
    expect(container.textContent).toContain('[image]')
    expect(box(container)).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).not.toContain('Loading image')
  })

  it('reads the whole image in a hidden copy whose source states no size, and not otherwise', async () => {
    const read = vi.fn(async () => image)
    const { container } = render(() => (
      <ImageResultView
        source={{ filePath: '/image.png', dimensions: SIZE }}
        actions={imageActions({
          loadFileImage: read,
          cachedFileImageSize: () => null,
          deferLoad: () => true,
          premeasurePass: () => true,
        })}
      />
    ))
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

  /** Mount a view with a readiness scope, as a row copy gives it. */
  function mountWithScope(actions: Partial<ImageRenderActions>) {
    let scope!: RenderReadinessScope
    const view = render(() => {
      scope = createRenderReadinessScope()
      return (
        <ImageResultView
          source={{ filePath: '/image.png' }}
          actions={imageActions({ ...actions, readiness: scope })}
        />
      )
    })
    return { ...view, scope }
  }

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

  it('releases the copy when the size read gives no size and no read of the image follows', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const { scope } = mountWithScope({
      loadFileImageSize: () => size.promise,
      deferLoad: () => true,
    })
    await Promise.resolve()
    expect(scope.measurable()).toBe(false)

    size.resolve(undefined)
    await waitFor(() => expect(scope.measurable()).toBe(true))
  })

  it('releases the copy when the size read rejects', async () => {
    const size = deferred<ImageDimensions | undefined>()
    const { scope } = mountWithScope({
      loadFileImageSize: () => size.promise,
      deferLoad: () => true,
    })
    await Promise.resolve()

    size.reject(new Error('the worker is unavailable'))
    await waitFor(() => expect(scope.measurable()).toBe(true))
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

    await vi.advanceTimersByTimeAsync(READINESS_REQUEST_LIMIT_MS - 1)
    expect(scope.measurable()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(scope.measurable()).toBe(true)
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
    await vi.advanceTimersByTimeAsync(READINESS_REQUEST_LIMIT_MS - 6000 - 1)
    expect(scope.measurable()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(scope.measurable()).toBe(true)
  })

  it('releases a hidden copy when its read of the image settles', async () => {
    const whole = deferred<typeof image>()
    const { scope, container } = mountWithScope({
      loadFileImage: () => whole.promise,
      loadFileImageSize: async () => undefined,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    await waitFor(() => expect(container.textContent).toContain('Loading image'))
    expect(scope.measurable()).toBe(false)

    whole.resolve(image)
    await waitFor(() => expect(scope.measurable()).toBe(true))
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('releases a hidden copy when its read of the image fails', async () => {
    const whole = deferred<typeof image>()
    const { scope } = mountWithScope({
      loadFileImage: () => whole.promise,
      loadFileImageSize: async () => undefined,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    await waitFor(() => expect(scope.measurable()).toBe(false))

    whole.reject(new Error('The image file is too large to preview'))
    await waitFor(() => expect(scope.measurable()).toBe(true))
  })

  // A second hidden copy of a row finds the answer of the first size read in the cache:
  // the header gives no size, as for an SVG. It starts no size read, and it must still
  // wait for its read of the image, or it measures the one line "Loading image…".
  it('holds a hidden copy for its read of the image when the cache holds no size for the file', async () => {
    const whole = deferred<typeof image>()
    const readSize = vi.fn(async () => undefined)
    const { scope, container } = mountWithScope({
      loadFileImage: () => whole.promise,
      loadFileImageSize: readSize,
      cachedFileImageSize: () => null,
      deferLoad: () => true,
      premeasurePass: () => true,
    })
    await waitFor(() => expect(container.textContent).toContain('Loading image'))
    expect(scope.measurable()).toBe(false)
    expect(readSize).not.toHaveBeenCalled()

    whole.resolve(image)
    await waitFor(() => expect(scope.measurable()).toBe(true))
    expect(container.querySelector('img')).not.toBeNull()
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

    await vi.advanceTimersByTimeAsync(READINESS_REQUEST_LIMIT_MS - 1)
    expect(scope.measurable()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(scope.measurable()).toBe(true)
  })

  it('holds nothing for a hidden copy with no size when the cache holds the image', () => {
    const { scope } = mountWithScope({
      cachedFileImage: () => image,
      cachedFileImageSize: () => null,
      deferLoad: () => true,
      premeasurePass: () => true,
    })

    // The copy shows the image with no read, so it has its height.
    expect(scope.measurable()).toBe(true)
  })

  it('holds nothing for a row in the list that is far from the view when the cache holds no size', () => {
    const read = vi.fn(async () => image)
    const { scope } = mountWithScope({
      loadFileImage: read,
      cachedFileImageSize: () => null,
      deferLoad: () => true,
    })

    // The row reads no image there, so it has no read to wait for.
    expect(scope.measurable()).toBe(true)
    expect(read).not.toHaveBeenCalled()
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
