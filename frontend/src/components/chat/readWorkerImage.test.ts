import { beforeEach, describe, expect, it, vi } from 'vitest'
import { workerFile } from '~/api/workerRpc'
import { base64ToUint8Array } from '~/lib/base64'
import { MAX_FILE_IMAGE_BYTES } from '~/lib/imageBlocks'
import { box, chars, fullBox, ispeBox, jpegBytes, u16be, u32be } from '~/test-support/imageBytes'
import { pngBase64 } from '~/test-support/pngFixture'
import { FileImageRefusedError } from './fileImageResolver'
import { FILE_IMAGE_HEADER_BYTES, readWorkerImage, readWorkerImageSize } from './readWorkerImage'

vi.mock('~/api/workerRpc', () => ({
  workerFile: {
    readFile: vi.fn(),
  },
}))
const read = vi.mocked(workerFile.readFile)
const data = pngBase64(12, 8)
const content = base64ToUint8Array(data)
const signal = new AbortController().signal
function response(bytes = content, totalSize = BigInt(content.length), modTime = '2026-09-11T00:00:00Z') {
  return {
    $typeName: 'leapmux.v1.ReadFileResponse' as const,
    path: '/image.png',
    content: bytes,
    totalSize,
    modTime,
  }
}
beforeEach(() => {
  read.mockReset()
})

describe('worker image reads', () => {
  it('reads a complete image once and supplies its MIME type and dimensions', async () => {
    read.mockResolvedValue(response())
    const image = await readWorkerImage('worker', '/image', undefined, signal)
    expect(image).toMatchObject({
      filePath: '/image',
      mimeType: 'image/png',
      data,
      dimensions: { width: 12, height: 8 },
    })
    expect(read).toHaveBeenCalledOnce()
    expect(read).toHaveBeenCalledWith('worker', expect.objectContaining({
      path: '/image',
      limit: BigInt(MAX_FILE_IMAGE_BYTES),
      metaOnlyIfTruncated: true,
    }), { signal })
  })

  it.each([
    ['file:///repo/a%20b.png', undefined, '/repo/a b.png'],
    ['file:///C:/Users/alice/a%20b.png', undefined, 'C:/Users/alice/a b.png'],
    ['file://server/share/image.png', undefined, '//server/share/image.png'],
    ['image.png', '/project', '/project/image.png'],
  ])('resolves the worker path %s', async (path, cwd, expected) => {
    read.mockResolvedValue(response())
    await readWorkerImage('worker', path!, cwd, signal)
    expect(read).toHaveBeenCalledWith('worker', expect.objectContaining({ path: expected }), { signal })
  })

  it('reads pages when a smaller channel returns metadata only', async () => {
    read.mockResolvedValueOnce(response(new Uint8Array()))
    read.mockImplementation(async (_worker, request) => {
      const offset = Number(request.offset ?? 0n)
      return response(content.slice(offset, offset + 8))
    })
    expect((await readWorkerImage('worker', '/image.png', undefined, signal)).data).toBe(data)
    expect(read.mock.calls.slice(1).map(call => Number(call[1].offset))).toEqual(Array.from({
      length: Math.ceil(content.length / 8),
    }, (_, index) => index * 8))
  })

  it.each([
    [
      response(new Uint8Array(), BigInt(MAX_FILE_IMAGE_BYTES + 1)),
      'too large',
    ],
    [
      response(new Uint8Array(), 0n),
      'empty',
    ],
    [
      response(content, -1n),
      'invalid size',
    ],
  ])('rejects unusable file metadata', async (value, reason) => {
    read.mockResolvedValue(value)
    await expect(readWorkerImage('worker', '/image.png', undefined, signal)).rejects.toThrow(reason)
    expect(read).toHaveBeenCalledOnce()
  })

  it.each([
    [
      'a file that is larger than the limit',
      response(new Uint8Array(), BigInt(MAX_FILE_IMAGE_BYTES + 1)),
      '/image.png',
    ],
    [
      'an empty file',
      response(new Uint8Array(), 0n),
      '/image.png',
    ],
    [
      'a file of a format that the chat does not show',
      response(Uint8Array.from([0x42, 0x4D, 0, 0]), 4n),
      '/image.bmp',
    ],
  ])('refuses %s with the error that a new read cannot change', async (_file, value, path) => {
    read.mockResolvedValue(value)

    await expect(readWorkerImage('worker', path, undefined, signal)).rejects.toBeInstanceOf(FileImageRefusedError)
  })

  it('fails for a response of an invalid size with an error that is no refusal', async () => {
    read.mockResolvedValue(response(content, -1n))

    // The Worker answered wrongly, and it can answer correctly at the next read.
    const failure = await readWorkerImage('worker', '/image.png', undefined, signal).catch((error: unknown) => error)

    expect(failure).toBeInstanceOf(Error)
    expect(failure).not.toBeInstanceOf(FileImageRefusedError)
  })

  it('stops if the file changes between pages', async () => {
    read.mockResolvedValueOnce(response(new Uint8Array())).mockResolvedValueOnce(response(content, BigInt(content.length), '2026-09-11T00:00:01Z'))
    await expect(readWorkerImage('worker', '/image.png', undefined, signal)).rejects.toThrow('changed')
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('stops if a page makes no progress', async () => {
    read.mockResolvedValue(response(new Uint8Array()))
    await expect(readWorkerImage('worker', '/image.png', undefined, signal)).rejects.toThrow('read completely')
    expect(read).toHaveBeenCalledTimes(2)
  })

  it.each([
    ['https://example.com/image.png', 'The image does not have a local file path'],
    ['relative.png', 'The image working directory is unavailable'],
  ])('rejects a path without a usable local target (%s)', async (path, reason) => {
    await expect(readWorkerImage('worker', path, undefined, signal)).rejects.toThrow(reason)
    expect(read).not.toHaveBeenCalled()
  })

  it('preserves cancellation', async () => {
    read.mockRejectedValue(new DOMException('Stopped', 'AbortError'))
    await expect(readWorkerImage('worker', '/image.png', undefined, signal)).rejects.toMatchObject({ name: 'AbortError' })
  })
})

describe('readWorkerImageSize', () => {
  /**
   * A JPEG whose frame header lies behind a metadata segment of `metadataBytes`: the
   * shape of a photograph with a large EXIF or ICC block.
   */
  function jpegWithLongHeader(width: number, height: number, metadataBytes: number): Uint8Array {
    return Uint8Array.from(jpegBytes(width, height, [
      0xFF,
      0xE2, // an application segment that is no EXIF block
      ...u16be(metadataBytes + 2),
      ...Array.from<number>({ length: metadataBytes }).fill(0),
    ]))
  }

  /**
   * An image file of the HEIF structure with the brand `brand`: `avif` for an AVIF, and
   * `heic` for a HEIC. A box of `paddingBytes` lies before the box that holds its size, as
   * the metadata of a photograph does.
   */
  function heifImage(brand: string, width: number, height: number, paddingBytes: number): Uint8Array {
    return Uint8Array.from([
      ...box('ftyp', chars(brand), u32be(0), chars(brand)),
      ...box('free', Array.from<number>({ length: paddingBytes }).fill(0)),
      ...fullBox('meta', 0, 0, box('iprp', box('ipco', ispeBox(width, height)))),
    ])
  }

  it('reads the size of a PNG from the first bytes of its file, in one request', async () => {
    read.mockResolvedValue(response(content, 500_000n))

    await expect(readWorkerImageSize('worker', '/image.png', undefined, signal)).resolves.toEqual({ width: 12, height: 8 })

    expect(read).toHaveBeenCalledOnce()
    // No `metaOnlyIfTruncated`: the size lies in the head of a file of any length.
    expect(read).toHaveBeenCalledWith('worker', {
      path: '/image.png',
      limit: BigInt(FILE_IMAGE_HEADER_BYTES),
    }, { signal })
  })

  it('reads a longer head once for a JPEG whose header is longer than the first read', async () => {
    const jpeg = jpegWithLongHeader(640, 480, 6000)
    read
      .mockResolvedValueOnce(response(jpeg.slice(0, FILE_IMAGE_HEADER_BYTES), BigInt(jpeg.length)))
      .mockResolvedValueOnce(response(jpeg, BigInt(jpeg.length)))

    await expect(readWorkerImageSize('worker', '/photo', undefined, signal)).resolves.toEqual({ width: 640, height: 480 })

    expect(read).toHaveBeenCalledTimes(2)
    // The second read asks for what the sniffer scans of a JPEG: 256 KiB.
    expect(read.mock.calls[1]![1]).toEqual({
      path: '/photo',
      limit: 262144n,
    })
  })

  it('reads a longer head once for an AVIF whose header is longer than the first read', async () => {
    const avif = heifImage('avif', 640, 480, 5000)
    read
      .mockResolvedValueOnce(response(avif.slice(0, FILE_IMAGE_HEADER_BYTES), BigInt(avif.length)))
      .mockResolvedValueOnce(response(avif, BigInt(avif.length)))

    await expect(readWorkerImageSize('worker', '/photo.avif', undefined, signal)).resolves.toEqual({ width: 640, height: 480 })

    expect(read).toHaveBeenCalledTimes(2)
    // The second read asks for what the sniffer scans of an AVIF: 64 KiB.
    expect(read.mock.calls[1]![1]).toEqual({
      path: '/photo.avif',
      limit: 65536n,
    })
  })

  it('reads no longer head for a file that only its name calls a JPEG', async () => {
    const head = new Uint8Array(FILE_IMAGE_HEADER_BYTES)
    read.mockResolvedValue(response(head, 500_000n))

    await expect(readWorkerImageSize('worker', '/photo.jpg', undefined, signal)).resolves.toBeUndefined()

    // The sniffer reads a JPEG by its signature, so more bytes of this file give it nothing.
    expect(read).toHaveBeenCalledOnce()
  })

  it('gives no size for a file that is larger than the image read accepts', async () => {
    read.mockResolvedValue(response(content, BigInt(MAX_FILE_IMAGE_BYTES + 1)))

    // No image ever takes the box that a size reserves, so the row must reserve none.
    await expect(readWorkerImageSize('worker', '/image.png', undefined, signal)).resolves.toBeUndefined()
  })

  it('gives no size for a file of a format that the chat does not show', async () => {
    const heic = heifImage('heic', 640, 480, 0)
    read.mockResolvedValue(response(heic, BigInt(heic.length)))

    // The header states a size, and the read of the image refuses the format.
    await expect(readWorkerImageSize('worker', '/photo.heic', undefined, signal)).resolves.toBeUndefined()
    expect(read).toHaveBeenCalledOnce()
  })

  it('gives the size of a file of exactly the largest size that the image read accepts', async () => {
    read.mockResolvedValue(response(content, BigInt(MAX_FILE_IMAGE_BYTES)))

    await expect(readWorkerImageSize('worker', '/image.png', undefined, signal)).resolves.toEqual({ width: 12, height: 8 })
  })

  it('gives no size for a JPEG whose longer head holds no frame header either', async () => {
    const jpeg = jpegWithLongHeader(640, 480, 6000)
    // Both reads end inside the metadata segment.
    read.mockResolvedValue(response(jpeg.slice(0, FILE_IMAGE_HEADER_BYTES), BigInt(jpeg.length)))

    await expect(readWorkerImageSize('worker', '/photo.jpg', undefined, signal)).resolves.toBeUndefined()

    expect(read).toHaveBeenCalledTimes(2)
  })

  it('reads no longer head for a JPEG whose first read held the whole file', async () => {
    const truncated = jpegWithLongHeader(640, 480, 100).slice(0, 50)
    read.mockResolvedValue(response(truncated, BigInt(truncated.length)))

    await expect(readWorkerImageSize('worker', '/photo.jpg', undefined, signal)).resolves.toBeUndefined()

    expect(read).toHaveBeenCalledOnce()
  })

  it('gives no size, in one request, for a PNG with no readable header', async () => {
    const head = new Uint8Array(FILE_IMAGE_HEADER_BYTES)
    // The signature of a PNG, and no IHDR chunk after it.
    head.set(content.slice(0, 8))
    read.mockResolvedValue(response(head, 500_000n))

    await expect(readWorkerImageSize('worker', '/image.png', undefined, signal)).resolves.toBeUndefined()

    // Only a JPEG and an AVIF keep their size behind a header of no fixed length.
    expect(read).toHaveBeenCalledOnce()
  })

  it('gives no size for an empty file', async () => {
    read.mockResolvedValue(response(new Uint8Array(), 0n))

    await expect(readWorkerImageSize('worker', '/image.png', undefined, signal)).resolves.toBeUndefined()

    expect(read).toHaveBeenCalledOnce()
  })

  it.each([
    ['file:///repo/a%20b.png', undefined, '/repo/a b.png'],
    ['a.png', '/w', '/w/a.png'],
  ])('resolves the worker path %s as the image read does', async (path, cwd, expected) => {
    read.mockResolvedValue(response(content, 500_000n))

    await readWorkerImageSize('worker', path, cwd, signal)

    expect(read).toHaveBeenCalledWith('worker', expect.objectContaining({ path: expected }), { signal })
  })

  it.each([
    ['https://example.com/image.png', 'The image does not have a local file path'],
    ['relative.png', 'The image working directory is unavailable'],
    ['', 'The image file is unavailable'],
  ])('rejects a path without a usable local target (%s)', async (path, reason) => {
    await expect(readWorkerImageSize('worker', path, undefined, signal)).rejects.toThrow(reason)
    expect(read).not.toHaveBeenCalled()
  })

  it('rejects when the read fails', async () => {
    read.mockRejectedValue(new Error('the worker is unavailable'))

    await expect(readWorkerImageSize('worker', '/image.png', undefined, signal)).rejects.toThrow('the worker is unavailable')
  })
})
