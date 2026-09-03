import { describe, expect, it } from 'vitest'
import { decodeImageBytes } from './ChatImageViewer'

// A one-pixel PNG's first bytes; only the decode matters here, not the image.
const PNG_BASE64 = 'iVBORw0KGgo='
const PNG_BYTES = [0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]

describe('decodeimagebytes', () => {
  it('decodes inline base64 with its stated mime type', () => {
    const decoded = decodeImageBytes({ data: PNG_BASE64, mimeType: 'image/png' })
    expect(decoded?.mimeType).toBe('image/png')
    expect([...(decoded?.content ?? [])]).toEqual(PNG_BYTES)
  })

  it('decodes a pre-formed data URL, taking the mime from the URL', () => {
    const decoded = decodeImageBytes({ url: `data:image/jpeg;base64,${PNG_BASE64}` })
    expect(decoded?.mimeType).toBe('image/jpeg')
    expect([...(decoded?.content ?? [])]).toEqual(PNG_BYTES)
  })

  it('refuses base64 with no mime type, which no blob could be built from', () => {
    expect(decodeImageBytes({ data: PNG_BASE64 })).toBeNull()
  })

  it('refuses an external URL: nothing local to open, and fetching would leak', () => {
    expect(decodeImageBytes({ url: 'https://example.com/x.png' })).toBeNull()
  })

  it('refuses a non-base64 data URL', () => {
    expect(decodeImageBytes({ url: 'data:image/png,rawbytes' })).toBeNull()
  })

  it('refuses a data URL with no comma', () => {
    expect(decodeImageBytes({ url: 'data:image/png;base64' })).toBeNull()
  })

  it('returns null rather than throwing on undecodable base64', () => {
    expect(decodeImageBytes({ data: '!!!not base64!!!', mimeType: 'image/png' })).toBeNull()
  })

  it('returns null for a source with nothing in it', () => {
    expect(decodeImageBytes({})).toBeNull()
  })
})
