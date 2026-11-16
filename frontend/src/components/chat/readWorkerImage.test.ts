import { beforeEach, describe, expect, it, vi } from 'vitest'
import { workerFile } from '~/api/workerRpc'
import { base64ToUint8Array } from '~/lib/base64'
import { MAX_FILE_IMAGE_BYTES } from '~/lib/imageBlocks'
import { pngBase64 } from '~/test-support/pngFixture'
import { FILE_IMAGE_HEADER_BYTES, FILE_IMAGE_HEADER_MAX_BYTES, readWorkerImage, readWorkerImageSize } from './readWorkerImage'

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
    const segmentLength = metadataBytes + 2
    return Uint8Array.from([
      0xFF,
      0xD8, // start of image
      0xFF,
      0xE2, // an application segment that is no EXIF block
      (segmentLength >>> 8) & 0xFF,
      segmentLength & 0xFF,
      ...Array.from<number>({ length: metadataBytes }).fill(0),
      0xFF,
      0xC0, // the frame header
      0x00,
      0x11,
      0x08,
      (height >>> 8) & 0xFF,
      height & 0xFF,
      (width >>> 8) & 0xFF,
      width & 0xFF,
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
    expect(read.mock.calls[1]![1]).toEqual({
      path: '/photo',
      limit: BigInt(FILE_IMAGE_HEADER_MAX_BYTES),
    })
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
