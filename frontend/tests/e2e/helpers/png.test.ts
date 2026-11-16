import { Buffer } from 'node:buffer'
import { crc32, inflateSync } from 'node:zlib'
import { describe, expect, it } from 'vitest'
import { encodePng, PNG_SIGNATURE, readPngChunks } from './png'

/** Grey scanlines of `width` by `height` pixels: the filter type 0, then one sample of `tone` for each pixel. */
function greyScanlines(width: number, height: number, tone: number): Buffer {
  const scanlines = Buffer.alloc((1 + width) * height, tone)
  for (let row = 0; row < height; row++)
    scanlines[row * (1 + width)] = 0
  return scanlines
}

describe('PNG_SIGNATURE', () => {
  it('holds the eight bytes that start every PNG file', () => {
    expect(PNG_SIGNATURE.toString('latin1')).toBe('\u0089PNG\r\n\u001A\n')
  })
})

describe('encodePng', () => {
  it('writes the signature, then the chunks IHDR, IDAT, and IEND, each with a valid checksum', () => {
    const png = encodePng({
      width: 3,
      height: 2,
      colorType: 0,
      scanlines: greyScanlines(3, 2, 0x80),
    })

    expect(png.subarray(0, 8).equals(PNG_SIGNATURE)).toBe(true)
    const chunks = readPngChunks(png)
    expect(chunks.map(chunk => chunk.type)).toEqual(['IHDR', 'IDAT', 'IEND'])
    for (const { type, data, checksum } of chunks) {
      expect(crc32(Buffer.concat([
        Buffer.from(type, 'latin1'),
        data,
      ])), `${type} checksum`).toBe(checksum)
    }
    expect(chunks[2]!.data).toHaveLength(0)
  })

  it.each([
    { colorType: 0, samples: 1 },
    { colorType: 2, samples: 3 },
  ] as const)('states the size, the depth 8, and the color type $colorType in the IHDR chunk', ({ colorType, samples }) => {
    const scanlines = Buffer.alloc((1 + 5 * samples) * 4)
    const header = readPngChunks(encodePng({
      width: 5,
      height: 4,
      colorType,
      scanlines,
    }))[0]!

    expect(header.type).toBe('IHDR')
    expect([...header.data]).toEqual([
      0,
      0,
      0,
      5,
      0,
      0,
      0,
      4,
      8,
      colorType,
      0,
      0,
      0,
    ])
  })

  it('stores the scanlines in the IDAT chunk', () => {
    const scanlines = greyScanlines(4, 3, 0x2A)
    const data = readPngChunks(encodePng({
      width: 4,
      height: 3,
      colorType: 0,
      scanlines,
    }))[1]!.data

    expect(inflateSync(data).equals(scanlines)).toBe(true)
  })

  it('writes a large image', () => {
    const chunks = readPngChunks(encodePng({
      width: 800,
      height: 600,
      colorType: 0,
      scanlines: greyScanlines(800, 600, 0x80),
    }))

    expect(chunks[0]!.data.readUInt32BE(0)).toBe(800)
    expect(chunks[0]!.data.readUInt32BE(4)).toBe(600)
  })

  it.each([
    [0, 1],
    [1, 0],
    [-1, 1],
    [1.5, 1],
  ])('refuses the size %s by %s', (width, height) => {
    expect(() => encodePng({
      width,
      height,
      colorType: 0,
      scanlines: Buffer.alloc(2),
    })).toThrow('positive integers')
  })

  it('refuses scanlines whose length does not fit the size and the color type', () => {
    expect(() => encodePng({
      width: 2,
      height: 2,
      colorType: 0,
      scanlines: Buffer.alloc(5),
    }))
      .toThrow('A PNG of 2 by 2 pixels of color type 0 needs 6 bytes of scanlines, not 5.')
    // Grey scanlines are too short for an RGB image of the same size.
    expect(() => encodePng({
      width: 2,
      height: 2,
      colorType: 2,
      scanlines: greyScanlines(2, 2, 0),
    }))
      .toThrow('A PNG of 2 by 2 pixels of color type 2 needs 14 bytes of scanlines, not 6.')
  })
})

describe('readPngChunks', () => {
  const png = encodePng({
    width: 2,
    height: 2,
    colorType: 0,
    scanlines: greyScanlines(2, 2, 0),
  })

  it('returns no chunk for the signature alone', () => {
    expect(readPngChunks(PNG_SIGNATURE)).toEqual([])
  })

  it('refuses bytes that do not start with the PNG signature', () => {
    expect(() => readPngChunks(Buffer.from('GIF89a'))).toThrow('do not start with the PNG signature')
    expect(() => readPngChunks(Buffer.alloc(0))).toThrow('do not start with the PNG signature')
  })

  it('refuses a chunk that runs past the end of the bytes', () => {
    // The checksum of IEND loses its last byte.
    expect(() => readPngChunks(png.subarray(0, png.length - 1))).toThrow(`The PNG chunk at byte ${png.length - 12} runs past the end of the bytes.`)
    // Three bytes after the signature cannot hold the length of a chunk.
    expect(() => readPngChunks(Buffer.concat([
      PNG_SIGNATURE,
      Buffer.alloc(3),
    ]))).toThrow('The PNG chunk at byte 8 runs past the end of the bytes.')
  })
})
