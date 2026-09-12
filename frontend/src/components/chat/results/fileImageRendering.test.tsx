import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { ImageResultView } from './imageResult'

const image = { data: 'iVBORw0KGgo=', mimeType: 'image/png' }

describe('file image rendering', () => {
  it('waits for scrolling to finish before changing the row geometry', async () => {
    const [paused, setPaused] = createSignal(true)
    const read = vi.fn(async () => image)
    const { container } = render(() => <ImageResultView source={{ filePath: '/image.png' }} context={{ sources: testMessageSources({ fileImage: read }), syntaxHighlightingPaused: paused }} />)
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    await Promise.resolve()
    await Promise.resolve()
    expect(container.querySelector('img')).toBeNull()
    setPaused(false)
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('src')).toBe(`data:image/png;base64,${image.data}`))
  })

  it('uses a cached image during measurement without starting another read', () => {
    const read = vi.fn(async () => image)
    const { container } = render(() => <ImageResultView source={{ filePath: '/image.png' }} context={{ premeasureMode: true, sources: testMessageSources({ cachedFileImage: () => image, fileImage: read }) }} />)
    expect(container.querySelector('img')?.getAttribute('src')).toContain(image.data)
    expect(read).not.toHaveBeenCalled()
  })

  it('ignores a file read after the source changes', async () => {
    const [path, setPath] = createSignal('/first.png')
    let finishFirst!: (value: typeof image) => void
    const read = vi.fn(async (file: string) => file === '/first.png'
      ? new Promise<typeof image>((resolve) => { finishFirst = resolve })
      : { ...image, data: 'BBBB' })
    const { container } = render(() => <ImageResultView source={{ filePath: path() }} context={{ sources: testMessageSources({ fileImage: read }) }} />)
    await waitFor(() => expect(read).toHaveBeenCalledOnce())
    setPath('/second.png')
    await waitFor(() => expect(container.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,BBBB'))
    finishFirst(image)
    await Promise.resolve()
    expect(container.querySelector('img')?.getAttribute('src')).toBe('data:image/png;base64,BBBB')
  })
})
