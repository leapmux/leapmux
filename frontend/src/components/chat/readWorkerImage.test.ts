import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFile } from '~/api/workerRpc'
import { base64ToUint8Array } from '~/lib/base64'
import { MAX_FILE_IMAGE_BYTES } from '~/lib/imageBlocks'
import { pngBase64 } from '~/test-support/pngFixture'
import { readWorkerImage } from './readWorkerImage'

vi.mock('~/api/workerRpc', () => ({ readFile: vi.fn() }))
const read = vi.mocked(readFile)
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
    expect(image).toMatchObject({ filePath: '/image', mimeType: 'image/png', data, dimensions: { width: 12, height: 8 } })
    expect(read).toHaveBeenCalledOnce()
    expect(read).toHaveBeenCalledWith('worker', expect.objectContaining({ path: '/image', limit: BigInt(MAX_FILE_IMAGE_BYTES), metaOnlyIfTruncated: true }), { signal })
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
    expect(read.mock.calls.slice(1).map(call => Number(call[1].offset))).toEqual(Array.from({ length: Math.ceil(content.length / 8) }, (_, index) => index * 8))
  })

  it.each([
    [response(new Uint8Array(), BigInt(MAX_FILE_IMAGE_BYTES + 1)), 'too large'],
    [response(new Uint8Array(), 0n), 'empty'],
    [response(content, -1n), 'invalid size'],
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

  it.each(['https://example.com/image.png', 'relative.png'])('rejects a path without a usable local target (%s)', async (path) => {
    await expect(readWorkerImage('worker', path, undefined, signal)).rejects.toThrow()
    expect(read).not.toHaveBeenCalled()
  })

  it('preserves cancellation', async () => {
    read.mockRejectedValue(new DOMException('Stopped', 'AbortError'))
    await expect(readWorkerImage('worker', '/image.png', undefined, signal)).rejects.toMatchObject({ name: 'AbortError' })
  })
})
