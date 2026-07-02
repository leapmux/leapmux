import { describe, expect, it } from 'vitest'
import { sniffImageDimensionsFromBase64, sniffImageDimensionsFromDataUrl } from './imageDimensions'

function b64(bytes: number[]): string {
  return btoa(String.fromCharCode(...bytes))
}

function chars(s: string): number[] {
  return [...s].map(c => c.charCodeAt(0))
}

function u16be(v: number): number[] {
  return [(v >>> 8) & 0xFF, v & 0xFF]
}

function u16le(v: number): number[] {
  return [v & 0xFF, (v >>> 8) & 0xFF]
}

function u32be(v: number): number[] {
  return [(v >>> 24) & 0xFF, (v >>> 16) & 0xFF, (v >>> 8) & 0xFF, v & 0xFF]
}

// --- fixture builders ---

function pngBytes(width: number, height: number): number[] {
  return [
    0x89,
    0x50,
    0x4E,
    0x47,
    0x0D,
    0x0A,
    0x1A,
    0x0A, // signature
    ...u32be(13),
    ...chars('IHDR'),
    ...u32be(width),
    ...u32be(height),
  ]
}

function gifBytes(width: number, height: number): number[] {
  return [...chars('GIF89a'), ...u16le(width), ...u16le(height), 0, 0]
}

function webpLossyBytes(width: number, height: number): number[] {
  return [
    ...chars('RIFF'),
    0x24,
    0x00,
    0x00,
    0x00,
    ...chars('WEBP'),
    ...chars('VP8 '),
    0x18,
    0x00,
    0x00,
    0x00,
    0x00,
    0x00,
    0x00, // frame tag
    0x9D,
    0x01,
    0x2A, // start code
    ...u16le(width),
    ...u16le(height),
  ]
}

function webpLosslessBytes(width: number, height: number): number[] {
  const bits = (width - 1) | ((height - 1) << 14)
  return [
    ...chars('RIFF'),
    0x24,
    0x00,
    0x00,
    0x00,
    ...chars('WEBP'),
    ...chars('VP8L'),
    0x18,
    0x00,
    0x00,
    0x00,
    0x2F,
    bits & 0xFF,
    (bits >>> 8) & 0xFF,
    (bits >>> 16) & 0xFF,
    (bits >>> 24) & 0xFF,
    0x00,
    0x00,
    0x00,
    0x00,
    0x00,
  ]
}

function webpExtendedBytes(width: number, height: number): number[] {
  const w = width - 1
  const h = height - 1
  return [
    ...chars('RIFF'),
    0x24,
    0x00,
    0x00,
    0x00,
    ...chars('WEBP'),
    ...chars('VP8X'),
    0x0A,
    0x00,
    0x00,
    0x00,
    0x00,
    0x00,
    0x00,
    0x00, // flags + reserved
    w & 0xFF,
    (w >>> 8) & 0xFF,
    (w >>> 16) & 0xFF,
    h & 0xFF,
    (h >>> 8) & 0xFF,
    (h >>> 16) & 0xFF,
  ]
}

/** SOI + optional leading segments + SOF0 carrying the coded dimensions. */
function jpegBytes(width: number, height: number, leading: number[] = []): number[] {
  return [
    0xFF,
    0xD8,
    ...leading,
    0xFF,
    0xC0,
    ...u16be(17),
    0x08,
    ...u16be(height),
    ...u16be(width),
    0x03,
    0x01,
    0x11,
    0x00,
    0x02,
    0x11,
    0x01,
    0x03,
    0x11,
    0x01,
  ]
}

/** APP1 EXIF segment (big-endian TIFF) carrying just the Orientation tag. */
function exifApp1Segment(orientation: number): number[] {
  const payload = [
    ...chars('Exif'),
    0x00,
    0x00,
    ...chars('MM'),
    ...u16be(42),
    ...u32be(8), // TIFF header, IFD0 at +8
    ...u16be(1), // one entry
    ...u16be(0x0112),
    ...u16be(3),
    ...u32be(1),
    ...u16be(orientation),
    0x00,
    0x00,
  ]
  return [0xFF, 0xE1, ...u16be(payload.length + 2), ...payload]
}

function box(type: string, ...contents: number[][]): number[] {
  const payload = contents.flat()
  return [...u32be(8 + payload.length), ...chars(type), ...payload]
}

function fullBox(type: string, version: number, flags: number, ...contents: number[][]): number[] {
  return box(type, [version, (flags >>> 16) & 0xFF, (flags >>> 8) & 0xFF, flags & 0xFF], ...contents)
}

function ispeBox(width: number, height: number): number[] {
  return fullBox('ispe', 0, 0, u32be(width), u32be(height))
}

function avifBytes(opts: {
  pitm?: number
  /** pitm box version: v0 stores a u16 item id, v1 a u32. */
  pitmVersion?: 0 | 1
  ipco: number[][]
  /** ipma associations for the pitm item: 1-based ipco indices. */
  associations?: number[]
  /** Encode associations as 16-bit entries (ipma flags & 1). */
  wideAssociations?: boolean
}): number[] {
  const metaChildren: number[][] = []
  if (opts.pitm !== undefined) {
    metaChildren.push(opts.pitmVersion === 1
      ? fullBox('pitm', 1, 0, u32be(opts.pitm))
      : fullBox('pitm', 0, 0, u16be(opts.pitm)))
  }
  const iprpChildren: number[][] = [box('ipco', ...opts.ipco)]
  if (opts.associations && opts.pitm !== undefined) {
    // The essential bit (top bit) is set on wide entries to prove the parser
    // masks it off rather than treating it as part of the index.
    const associationBytes = opts.wideAssociations
      ? opts.associations.flatMap(index => u16be(0x8000 | index))
      : opts.associations
    iprpChildren.push(fullBox(
      'ipma',
      0,
      opts.wideAssociations ? 1 : 0,
      u32be(1),
      u16be(opts.pitm),
      [opts.associations.length],
      associationBytes,
    ))
  }
  metaChildren.push(box('iprp', ...iprpChildren))
  return [
    ...box('ftyp', chars('avif'), u32be(0), chars('avif')),
    ...fullBox('meta', 0, 0, ...metaChildren),
  ]
}

describe('sniffimagedimensions', () => {
  it('parses PNG IHDR dimensions', () => {
    expect(sniffImageDimensionsFromBase64(b64(pngBytes(640, 480)))).toEqual({ width: 640, height: 480 })
  })

  it('parses GIF logical screen dimensions', () => {
    expect(sniffImageDimensionsFromBase64(b64(gifBytes(320, 200)))).toEqual({ width: 320, height: 200 })
  })

  it('parses all three WebP flavors', () => {
    expect(sniffImageDimensionsFromBase64(b64(webpLossyBytes(550, 368)))).toEqual({ width: 550, height: 368 })
    expect(sniffImageDimensionsFromBase64(b64(webpLosslessBytes(800, 600)))).toEqual({ width: 800, height: 600 })
    expect(sniffImageDimensionsFromBase64(b64(webpExtendedBytes(1024, 768)))).toEqual({ width: 1024, height: 768 })
  })

  it('parses baseline JPEG SOF dimensions, skipping leading segments', () => {
    const dqt = [0xFF, 0xDB, ...u16be(4), 0x00, 0x00]
    expect(sniffImageDimensionsFromBase64(b64(jpegBytes(320, 240, dqt)))).toEqual({ width: 320, height: 240 })
  })

  it('swaps JPEG dimensions for the rotated EXIF orientations (5-8)', () => {
    expect(sniffImageDimensionsFromBase64(b64(jpegBytes(200, 100, exifApp1Segment(6)))))
      .toEqual({ width: 100, height: 200 })
    // Orientations 1-4 flip/identity only: no swap.
    expect(sniffImageDimensionsFromBase64(b64(jpegBytes(200, 100, exifApp1Segment(3)))))
      .toEqual({ width: 200, height: 100 })
  })

  it('returns null for a JPEG whose scan hits SOS without a SOF', () => {
    expect(sniffImageDimensionsFromBase64(b64([0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x04, 0x00, 0x00]))).toBeNull()
  })

  it('skips JPEG fill bytes (0xFF padding runs) before a marker', () => {
    // A lone 0xFF between SOI and the SOF marker is legal padding.
    expect(sniffImageDimensionsFromBase64(b64(jpegBytes(320, 240, [0xFF])))).toEqual({ width: 320, height: 240 })
  })

  it('resolves the AVIF primary item ispe through pitm/ipma, not the first ispe', () => {
    // ipco: [1] thumbnail ispe 100x100, [2] primary ispe 800x600.
    const bytes = avifBytes({
      pitm: 1,
      ipco: [ispeBox(100, 100), ispeBox(800, 600)],
      associations: [2],
    })
    expect(sniffImageDimensionsFromBase64(b64(bytes))).toEqual({ width: 800, height: 600 })
  })

  it('applies an associated AVIF irot 90-degree rotation as an axis swap', () => {
    const irot90 = box('irot', [0x01])
    const bytes = avifBytes({
      pitm: 1,
      ipco: [ispeBox(800, 600), irot90],
      associations: [1, 2],
    })
    expect(sniffImageDimensionsFromBase64(b64(bytes))).toEqual({ width: 600, height: 800 })
  })

  it('falls back to a sole ispe when pitm/ipma are absent', () => {
    const bytes = avifBytes({ ipco: [ispeBox(512, 256)] })
    expect(sniffImageDimensionsFromBase64(b64(bytes))).toEqual({ width: 512, height: 256 })
  })

  it('reads 16-bit ipma associations (flags & 1), masking the essential bit', () => {
    const bytes = avifBytes({
      pitm: 1,
      ipco: [ispeBox(100, 100), ispeBox(800, 600)],
      associations: [2],
      wideAssociations: true,
    })
    expect(sniffImageDimensionsFromBase64(b64(bytes))).toEqual({ width: 800, height: 600 })
  })

  it('reads a version-1 pitm (32-bit item id)', () => {
    const bytes = avifBytes({
      pitm: 7,
      pitmVersion: 1,
      ipco: [ispeBox(160, 90)],
      associations: [1],
    })
    expect(sniffImageDimensionsFromBase64(b64(bytes))).toEqual({ width: 160, height: 90 })
  })

  it('refuses ambiguous AVIF shapes: multiple ispes without associations, or an associated clap crop', () => {
    const twoIspes = avifBytes({ ipco: [ispeBox(100, 100), ispeBox(800, 600)] })
    expect(sniffImageDimensionsFromBase64(b64(twoIspes))).toBeNull()

    const clap = box('clap', u32be(1), u32be(1), u32be(1), u32be(1), u32be(0), u32be(1), u32be(0), u32be(1))
    const cropped = avifBytes({ pitm: 1, ipco: [ispeBox(800, 600), clap], associations: [1, 2] })
    expect(sniffImageDimensionsFromBase64(b64(cropped))).toBeNull()
  })

  it('returns null for unknown formats, corrupt headers, and truncation', () => {
    expect(sniffImageDimensionsFromBase64(b64(chars('BM12345678')))).toBeNull() // BMP: unsupported
    expect(sniffImageDimensionsFromBase64(b64(pngBytes(640, 480).slice(0, 20)))).toBeNull() // truncated PNG
    expect(sniffImageDimensionsFromBase64(b64([...chars('GIF88a'), ...u16le(1), ...u16le(1), 0, 0]))).toBeNull() // bad GIF version
    expect(sniffImageDimensionsFromBase64(b64(pngBytes(0, 480)))).toBeNull() // zero dimension
    expect(sniffImageDimensionsFromBase64('!!!not-base64!!!')).toBeNull()
    expect(sniffImageDimensionsFromBase64('')).toBeNull()
  })

  it('unwraps base64 data URLs and rejects non-base64 ones', () => {
    expect(sniffImageDimensionsFromDataUrl(`data:image/png;base64,${b64(pngBytes(12, 34))}`))
      .toEqual({ width: 12, height: 34 })
    expect(sniffImageDimensionsFromDataUrl('data:image/png,rawpayload')).toBeNull()
    expect(sniffImageDimensionsFromDataUrl('https://example.com/a.png')).toBeNull()
  })
})
