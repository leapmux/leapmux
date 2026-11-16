import { Buffer } from 'node:buffer'
import { crc32, deflateSync } from 'node:zlib'

/**
 * A minimal PNG encoder and chunk reader for the fixtures of the E2E suite.
 *
 * The encoder writes an image that a browser decodes: a signature, then the chunks IHDR, IDAT, and IEND. Each sample
 * takes 8 bits, and the image uses no palette and no interlace. The reader walks the chunks of such a file for a test
 * that checks what a fixture wrote.
 */

/** The eight bytes that start every PNG file. */
export const PNG_SIGNATURE = Buffer.from([
  0x89,
  0x50,
  0x4E,
  0x47,
  0x0D,
  0x0A,
  0x1A,
  0x0A,
])

/**
 * The number of samples in one pixel, for each color type that `encodePng` writes: one grey sample for the type 0, and
 * a red, a green, and a blue sample for the type 2.
 */
const SAMPLES_PER_PIXEL = { 0: 1, 2: 3 } as const

/** An image for `encodePng`. */
export interface PngImage {
  width: number
  height: number
  /** 0 for a grey image, 2 for an RGB image. */
  colorType: keyof typeof SAMPLES_PER_PIXEL
  /**
   * The rows of the image, top to bottom, before compression. Each row is the filter type, then the samples of each
   * pixel, 8 bits each.
   */
  scanlines: Buffer
}

/** One chunk of a PNG file. */
export interface PngChunk {
  type: string
  data: Buffer
  /** The CRC-32 that the file stores for the type and the data. */
  checksum: number
}

/** The bytes of one chunk: the length of the data, the type, the data, and the CRC-32 of the type and the data. */
function pngChunk(type: string, data: Buffer): Buffer {
  const body = Buffer.concat([
    Buffer.from(type, 'latin1'),
    data,
  ])
  const length = Buffer.alloc(4)
  length.writeUInt32BE(data.length)
  const checksum = Buffer.alloc(4)
  checksum.writeUInt32BE(crc32(body))
  return Buffer.concat([length, body, checksum])
}

/** The bytes of a PNG file that holds `image`. */
export function encodePng({ width, height, colorType, scanlines }: PngImage): Buffer {
  if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0)
    throw new Error('A PNG needs a width and a height that are positive integers.')
  const expected = (1 + width * SAMPLES_PER_PIXEL[colorType]) * height
  if (scanlines.length !== expected)
    throw new Error(`A PNG of ${width} by ${height} pixels of color type ${colorType} needs ${expected} bytes of scanlines, not ${scanlines.length}.`)
  const header = Buffer.alloc(13)
  header.writeUInt32BE(width, 0)
  header.writeUInt32BE(height, 4)
  // The sample depth and the color type. The bytes of the compression method, the filter method, and the interlace
  // method stay 0.
  header[8] = 8
  header[9] = colorType
  return Buffer.concat([
    PNG_SIGNATURE,
    pngChunk('IHDR', header),
    pngChunk('IDAT', deflateSync(scanlines)),
    pngChunk('IEND', Buffer.alloc(0)),
  ])
}

/**
 * The chunks of the PNG file `bytes`, in order. Throws when `bytes` do not start with the PNG signature, or when a
 * chunk runs past the end of `bytes`. The reader does not check the checksums: a test compares each one.
 */
export function readPngChunks(bytes: Buffer): PngChunk[] {
  if (!bytes.subarray(0, PNG_SIGNATURE.length).equals(PNG_SIGNATURE))
    throw new Error('The bytes do not start with the PNG signature.')
  const chunks: PngChunk[] = []
  for (let offset = PNG_SIGNATURE.length; offset < bytes.length;) {
    // The length, the type, and the checksum take 12 bytes around the data.
    const end = bytes.length - offset < 12
      ? undefined
      : offset + 12 + bytes.readUInt32BE(offset)
    if (end === undefined || end > bytes.length)
      throw new Error(`The PNG chunk at byte ${offset} runs past the end of the bytes.`)
    chunks.push({
      type: bytes.toString('latin1', offset + 4, offset + 8),
      data: bytes.subarray(offset + 8, end - 4),
      checksum: bytes.readUInt32BE(end - 4),
    })
    offset = end
  }
  return chunks
}
