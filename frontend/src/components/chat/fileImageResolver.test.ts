import { describe, expect, it, vi } from 'vitest'
import { createFileImageResolver } from './fileImageResolver'

describe('file image resolution', () => {
  it('reads the file again for a different tool call', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read)
    await images.load('/image.png', { reference: 'first-call' })
    await images.load('/image.png', { reference: 'second-call' })
    await images.load('/image.png', { reference: 'first-call' })
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('refreshes an image when the user retries decoding it', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read)
    await images.load('/image.png')
    await images.load('/image.png', { refresh: true })
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('uses one read for concurrent consumers and later measurement reads', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read)
    const [first, second] = await Promise.all([images.load('/image.png'), images.load('/image.png')])
    expect(first).toBe(second)
    expect(images.peek('/image.png')).toBe(first)
    await images.load('/image.png')
    expect(read).toHaveBeenCalledTimes(1)
  })

  it('evicts image bytes according to the cache limit', async () => {
    const read = vi.fn(async () => ({ data: 'AAAA', mimeType: 'image/png' }))
    const images = createFileImageResolver(read, 8)
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
    const images = createFileImageResolver(read, 4)
    await images.load('large')
    expect(images.peek('large')).toBeUndefined()
    await images.load('large')
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('retries a failed read when a consumer asks again', async () => {
    const read = vi.fn().mockRejectedValueOnce(new Error('File unavailable')).mockResolvedValue({ data: 'AAAA', mimeType: 'image/png' })
    const images = createFileImageResolver(read)
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
    })
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
    const images = createFileImageResolver(async () => ({ url: 'https://example.com/image.png' }))
    await expect(images.load('image')).rejects.toThrow('cannot be displayed')
    expect(images.peek('image')).toBeUndefined()
  })
})
