import { clearMocks, mockIPC } from '@tauri-apps/api/mocks'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readClipboardImage } from './platformBridge'

describe('readClipboardImage', () => {
  let originalGetContext: typeof HTMLCanvasElement.prototype.getContext
  let originalToBlob: typeof HTMLCanvasElement.prototype.toBlob
  let originalImageData: typeof globalThis.ImageData | undefined

  beforeEach(() => {
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__

    // jsdom doesn't ship ImageData / a 2d context / toBlob — stub the shapes
    // readClipboardImage relies on. The fake context's putImageData is a no-op
    // and toBlob invents a Blob with the requested MIME so the File the
    // function returns has the right type.
    originalGetContext = HTMLCanvasElement.prototype.getContext
    originalToBlob = HTMLCanvasElement.prototype.toBlob
    originalImageData = (globalThis as { ImageData?: typeof globalThis.ImageData }).ImageData

    HTMLCanvasElement.prototype.getContext = vi.fn(() => ({
      putImageData: vi.fn(),
    })) as unknown as typeof HTMLCanvasElement.prototype.getContext
    HTMLCanvasElement.prototype.toBlob = function (cb: BlobCallback, type?: string) {
      cb(new Blob([new Uint8Array([0x89, 0x50, 0x4E, 0x47])], { type: type ?? 'image/png' }))
    } as typeof HTMLCanvasElement.prototype.toBlob
    ;(globalThis as { ImageData: unknown }).ImageData = class {
      constructor(public data: Uint8ClampedArray, public width: number, public height: number) {}
    }
  })

  afterEach(() => {
    clearMocks()
    delete (window as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    HTMLCanvasElement.prototype.getContext = originalGetContext
    HTMLCanvasElement.prototype.toBlob = originalToBlob
    if (originalImageData)
      (globalThis as { ImageData: unknown }).ImageData = originalImageData
    else
      delete (globalThis as { ImageData?: unknown }).ImageData
  })

  it('returns null when not running in Tauri', async () => {
    expect(await readClipboardImage()).toBeNull()
  })

  it('returns a PNG File when the Tauri clipboard holds an image', async () => {
    mockIPC((cmd) => {
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1 // resource id
      if (cmd === 'plugin:image|rgba')
        return new Uint8Array([255, 0, 0, 255, 0, 255, 0, 255]) // 2 RGBA pixels
      if (cmd === 'plugin:image|size')
        return { width: 2, height: 1 }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    const file = await readClipboardImage()
    expect(file).not.toBeNull()
    expect(file?.type).toBe('image/png')
    expect(file?.size).toBeGreaterThan(0)
  })

  it('returns null on a zero-sized image', async () => {
    mockIPC((cmd) => {
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1
      if (cmd === 'plugin:image|rgba')
        return new Uint8Array()
      if (cmd === 'plugin:image|size')
        return { width: 0, height: 0 }
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    expect(await readClipboardImage()).toBeNull()
  })

  it('returns null when the plugin throws', async () => {
    mockIPC(() => {
      throw new Error('clipboard read failed')
    })

    expect(await readClipboardImage()).toBeNull()
  })

  it('releases the Image resource after a successful read', async () => {
    const calls: string[] = []
    mockIPC((cmd) => {
      calls.push(cmd)
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1
      if (cmd === 'plugin:image|rgba')
        return new Uint8Array([255, 0, 0, 255])
      if (cmd === 'plugin:image|size')
        return { width: 1, height: 1 }
      if (cmd === 'plugin:resources|close')
        return undefined
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    await readClipboardImage()
    expect(calls).toContain('plugin:resources|close')
  })

  it('releases the Image resource when decoding fails after acquisition', async () => {
    const calls: string[] = []
    mockIPC((cmd) => {
      calls.push(cmd)
      if (cmd === 'plugin:clipboard-manager|read_image')
        return 1
      if (cmd === 'plugin:image|rgba')
        throw new Error('rgba failed')
      if (cmd === 'plugin:resources|close')
        return undefined
      throw new Error(`unmocked Tauri command: ${cmd}`)
    })

    expect(await readClipboardImage()).toBeNull()
    expect(calls).toContain('plugin:resources|close')
  })
})
