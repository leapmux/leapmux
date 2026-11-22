import { render } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { pngBytes } from '~/test-support/imageBytes'
import { ImageFileView } from './ImageFileView'

describe('ImageFileView', () => {
  let createObjectUrlSpy: ReturnType<typeof vi.spyOn>
  let revokeObjectUrlSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    let seq = 0
    createObjectUrlSpy = vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:test-image-${++seq}`)
    revokeObjectUrlSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('uses sniffed magic-byte MIME type for extensionless and misnamed raster images', () => {
    const png = new Uint8Array(pngBytes(16, 16))
    const first = render(() => (
      <ImageFileView
        content={png}
        filePath="/repo/diagram"
        mode="render"
      />
    ))
    const firstBlob = createObjectUrlSpy.mock.calls[0]?.[0] as Blob | undefined
    expect(firstBlob).toBeInstanceOf(Blob)
    expect(firstBlob?.type).toBe('image/png')
    first.unmount()

    const second = render(() => (
      <ImageFileView
        content={png}
        filePath="/repo/misnamed.jpg"
        mode="render"
      />
    ))
    const secondBlob = createObjectUrlSpy.mock.calls[1]?.[0] as Blob | undefined
    expect(secondBlob).toBeInstanceOf(Blob)
    expect(secondBlob?.type).toBe('image/png')
    second.unmount()
    expect(revokeObjectUrlSpy).toHaveBeenCalledTimes(2)
  })

  it('falls back to the file extension for SVG images', () => {
    const svg = new TextEncoder().encode('<svg xmlns="http://www.w3.org/2000/svg"></svg>')
    const rendered = render(() => (
      <ImageFileView
        content={svg}
        filePath="/repo/icon.svg"
        mode="render"
      />
    ))
    const blob = createObjectUrlSpy.mock.calls[0]?.[0] as Blob | undefined
    expect(blob).toBeInstanceOf(Blob)
    expect(blob?.type).toBe('image/svg+xml')
    rendered.unmount()
  })
})
