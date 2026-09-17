import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { ImageResultView } from './imageResult'

const image = { data: 'iVBORw0KGgo=', mimeType: 'image/png' }

describe('file image rendering', () => {
  it('waits for scrolling to finish before changing the row geometry', async () => {
    const [paused, setPaused] = createSignal(true)
    const read = vi.fn(async () => image)
    const { container } = render(() => <ImageResultView source={{ filePath: '/image.png' }} actions={{ loadFileImage: read, cachedFileImage: () => undefined, openImage: () => {}, deferLoad: () => false, premeasurePass: () => false }} holdDisplay={() => paused()} />)
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    await Promise.resolve()
    await Promise.resolve()
    expect(container.querySelector('img')).toBeNull()
    setPaused(false)
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('src')).toBe(`data:image/png;base64,${image.data}`))
  })

  it('uses a cached image during measurement without starting another read', () => {
    const read = vi.fn(async () => image)
    const { container } = render(() => <ImageResultView source={{ filePath: '/image.png' }} actions={{ loadFileImage: read, cachedFileImage: () => image, openImage: () => {}, deferLoad: () => true, premeasurePass: () => true }} />)
    expect(container.querySelector('img')?.getAttribute('src')).toContain(image.data)
    expect(read).not.toHaveBeenCalled()
  })

  it('ignores a file read after the source changes', async () => {
    const [path, setPath] = createSignal('/first.png')
    let finishFirst!: (value: typeof image) => void
    const read = vi.fn(async (file: string) => file === '/first.png'
      ? new Promise<typeof image>((resolve) => { finishFirst = resolve })
      : { ...image, data: 'BBBB' })
    const { container } = render(() => <ImageResultView source={{ filePath: path() }} actions={{ loadFileImage: read, cachedFileImage: () => undefined, openImage: () => {}, deferLoad: () => false, premeasurePass: () => false }} />)
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
  it('claims no read during a premeasure pass', () => {
    const read = vi.fn(async () => image)
    const { container } = render(() => (
      <ImageResultView source={{ filePath: '/image.png' }} actions={{ loadFileImage: read, cachedFileImage: () => undefined, openImage: () => {}, deferLoad: () => true, premeasurePass: () => true }} />
    ))
    expect(read).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('Loading image')
  })

  it('claims no read while the row is offscreen, and reads once it arrives', async () => {
    const [offscreen, setOffscreen] = createSignal(true)
    const read = vi.fn(async () => image)
    const { container } = render(() => (
      <ImageResultView source={{ filePath: '/image.png' }} actions={{ loadFileImage: read, cachedFileImage: () => undefined, openImage: () => {}, deferLoad: offscreen, premeasurePass: () => false }} />
    ))
    expect(read).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('Loading image')
    setOffscreen(false)
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())
  })

  it('still claims a read that is genuinely in flight', async () => {
    const read = vi.fn(() => new Promise<typeof image>(() => {}))
    const { container } = render(() => (
      <ImageResultView source={{ filePath: '/image.png' }} actions={{ loadFileImage: read, cachedFileImage: () => undefined, openImage: () => {}, deferLoad: () => false, premeasurePass: () => false }} />
    ))
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    expect(container.textContent).toContain('Loading image')
  })
})
