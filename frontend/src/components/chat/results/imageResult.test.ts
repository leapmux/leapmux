import { describe, expect, it } from 'vitest'
import { imageRenderInfo, imageReservationMatchesDecoded, imageReservationStyle } from './imageResult'

describe('imagereservationstyle', () => {
  // The width formula must reproduce exactly what auto layout yields after
  // the image decodes: height = min(h, h/w * containerWidth, MAX_HEIGHT).
  it('clamps by natural width for images that fit', () => {
    expect(imageReservationStyle({ width: 100, height: 50 })).toEqual({
      'aspect-ratio': '100 / 50',
      'width': 'min(100px, 100%, 640.00px)', // natural 100px wins
    })
  })

  it('clamps by width-at-max-height for tall images', () => {
    // 800x1600 hits the 320px max height at width 160 — the reserved box
    // stops at the visible image edge instead of spanning the container.
    expect(imageReservationStyle({ width: 800, height: 1600 })).toEqual({
      'aspect-ratio': '800 / 1600',
      'width': 'min(800px, 100%, 160.00px)',
    })
  })

  it('leaves wide images to the container clamp', () => {
    expect(imageReservationStyle({ width: 640, height: 480 })).toEqual({
      'aspect-ratio': '640 / 480',
      'width': 'min(640px, 100%, 426.67px)',
    })
  })
})

describe('imagereservationmatchesdecoded', () => {
  it('rejects decoded dimensions that preserve ratio but not absolute size', () => {
    expect(imageReservationMatchesDecoded(
      { width: 100, height: 50 },
      { naturalWidth: 200, naturalHeight: 100 },
    )).toBe(false)
  })

  it('accepts exactly matching decoded dimensions', () => {
    expect(imageReservationMatchesDecoded(
      { width: 100, height: 50 },
      { naturalWidth: 100, naturalHeight: 50 },
    )).toBe(true)
  })
})

describe('imagerenderinfo', () => {
  it('returns no-data when neither data nor url is present', () => {
    expect(imageRenderInfo({})).toEqual({ reason: 'no-data' })
    expect(imageRenderInfo({ mimeType: 'image/png' })).toEqual({ reason: 'no-data' })
  })

  it('builds an inline data: URL from base64 + allowlisted mime', () => {
    const result = imageRenderInfo({ mimeType: 'image/png', data: 'AAAA' })
    expect(result.via).toBe('inline')
    expect(result.src).toBe('data:image/png;base64,AAAA')
  })

  it('uppercase mime types are normalized', () => {
    const result = imageRenderInfo({ mimeType: 'IMAGE/PNG', data: 'AAAA' })
    expect(result.via).toBe('inline')
    expect(result.src).toBe('data:image/png;base64,AAAA')
  })

  it('refuses non-allowlisted mime types (e.g. svg)', () => {
    expect(imageRenderInfo({ mimeType: 'image/svg+xml', data: 'AAAA' }))
      .toEqual({ reason: 'unsupported-mime' })
  })

  it('refuses base64 without an explicit mime type', () => {
    expect(imageRenderInfo({ data: 'AAAA' })).toEqual({ reason: 'unsupported-mime' })
  })

  it('refuses base64 over the size cap', () => {
    const huge = 'A'.repeat(7 * 1024 * 1024 + 1)
    expect(imageRenderInfo({ mimeType: 'image/png', data: huge }))
      .toEqual({ reason: 'too-large' })
  })

  it('caps pre-formed data: URLs by payload size, not metadata prefix length', () => {
    const payload = 'A'.repeat(7 * 1024 * 1024)
    const data = `data:image/png;base64,${payload}`
    expect(imageRenderInfo({ url: data })).toEqual({ src: data, via: 'inline' })
  })

  it('rejects pre-formed data: URLs whose payload exceeds the cap', () => {
    // The over-cap side of the payload-size boundary (one byte past MAX): the
    // `url.length - comma - 1 > MAX` true branch, not otherwise exercised.
    const payload = 'A'.repeat(7 * 1024 * 1024 + 1)
    const data = `data:image/png;base64,${payload}`
    expect(imageRenderInfo({ url: data })).toEqual({ reason: 'too-large' })
  })

  it('passes through pre-formed data: URLs with allowlisted mime', () => {
    const data = 'data:image/jpeg;base64,XYZ='
    expect(imageRenderInfo({ url: data })).toEqual({ src: data, via: 'inline' })
  })

  it('refuses pre-formed data: URLs with non-allowlisted mime', () => {
    expect(imageRenderInfo({ url: 'data:image/svg+xml;base64,XYZ=' }))
      .toEqual({ reason: 'unsupported-mime' })
  })

  it('refuses a data: URL with no comma at all', () => {
    expect(imageRenderInfo({ url: 'data:image/png;base64' })).toEqual({ reason: 'unknown-shape' })
  })

  it('flags http URLs as external (rendered as a link, not inlined)', () => {
    expect(imageRenderInfo({ url: 'http://example.com/x.png' }))
      .toEqual({ reason: 'external-url' })
  })

  it('flags https URLs as external (rendered as a link, not inlined)', () => {
    expect(imageRenderInfo({ url: 'https://example.com/x.png' }))
      .toEqual({ reason: 'external-url' })
  })

  it('refuses a url in a scheme it cannot act on', () => {
    // `blob:` and `file:` reach the browser from nowhere in this pipeline, so
    // treating an unrecognized scheme as renderable would only ever inline
    // something the agent did not send.
    expect(imageRenderInfo({ url: 'file:///tmp/x.png' })).toEqual({ reason: 'unknown-shape' })
  })

  it('prefers the url over base64 when a block carries both', () => {
    // `parseImageBlock` never produces both, but the type permits it; pinning
    // the precedence keeps a future producer from silently picking the other.
    const data = 'data:image/png;base64,AAAA'
    expect(imageRenderInfo({ url: data, data: 'BBBB', mimeType: 'image/png' }))
      .toEqual({ src: data, via: 'inline' })
  })
})
