import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFile } from '~/api/workerRpc'
import { readWorkerFile } from './readWorkerFile'

vi.mock('~/api/workerRpc', () => ({ readFile: vi.fn() }))
const read = vi.mocked(readFile)

/** The page size the reader asks for, and the window it keeps in flight. */
const PAGE = 48 * 1024
const CONCURRENCY = 4
const MOD_TIME = '2026-09-11T00:00:00Z'

function response(content: Uint8Array, totalSize: bigint, modTime = MOD_TIME) {
  return {
    $typeName: 'leapmux.v1.ReadFileResponse' as const,
    path: '/file.bin',
    content,
    totalSize,
    modTime,
  }
}

/** Bytes that state their own offset, so a page written to the wrong place shows. */
function fileBytes(length: number): Uint8Array {
  const bytes = new Uint8Array(length)
  for (let index = 0; index < length; index++)
    bytes[index] = index % 251
  return bytes
}

function readAll(bytes: Uint8Array, signal?: AbortSignal) {
  return readWorkerFile({
    workerId: 'worker',
    path: '/file.bin',
    maxBytes: bytes.length,
    // The option rejects an explicit undefined; omit when no signal was given.
    ...(signal === undefined ? {} : { signal }),
  })
}

beforeEach(() => {
  read.mockReset()
})

describe('readWorkerFile', () => {
  it('rejects a preview limit that is not a positive safe integer', async () => {
    for (const maxBytes of [0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY]) {
      await expect(readWorkerFile({ workerId: 'worker', path: '/file.bin', maxBytes })).rejects.toThrow('preview limit is invalid')
    }
    expect(read).not.toHaveBeenCalled()
  })

  it('returns metadata only when the file exceeds the preview limit', async () => {
    read.mockResolvedValue(response(new Uint8Array(), 4096n))
    const result = await readWorkerFile({ workerId: 'worker', path: '/file.bin', maxBytes: 16 })
    expect(result.content).toEqual(new Uint8Array())
    expect(read).toHaveBeenCalledOnce()
  })

  it('returns the first response when it already holds the whole file', async () => {
    const bytes = fileBytes(64)
    read.mockResolvedValue(response(bytes, BigInt(bytes.length)))
    expect((await readAll(bytes)).content).toEqual(bytes)
    expect(read).toHaveBeenCalledOnce()
  })

  it('reads the remaining pages in a bounded window and assembles them in order', async () => {
    const bytes = fileBytes(PAGE * 10)
    let inFlight = 0
    let peak = 0
    read.mockResolvedValueOnce(response(new Uint8Array(), BigInt(bytes.length)))
    read.mockImplementation(async (_worker, request) => {
      inFlight++
      peak = Math.max(peak, inFlight)
      await Promise.resolve()
      inFlight--
      const offset = Number(request.offset ?? 0n)
      return response(bytes.slice(offset, offset + Number(request.limit ?? 0n)), BigInt(bytes.length))
    })
    expect((await readAll(bytes)).content).toEqual(bytes)
    expect(peak).toBe(CONCURRENCY)
    expect(read).toHaveBeenCalledTimes(11)
    expect(read.mock.calls.slice(1).map(call => Number(call[1].offset)).sort((a, b) => a - b))
      .toEqual(Array.from({ length: 10 }, (_, index) => index * PAGE))
  })

  it('fills a range that a short page leaves incomplete', async () => {
    const bytes = fileBytes(PAGE + 16)
    read.mockResolvedValueOnce(response(new Uint8Array(), BigInt(bytes.length)))
    read.mockImplementation(async (_worker, request) => {
      const offset = Number(request.offset ?? 0n)
      const length = Math.min(Number(request.limit ?? 0n), 1024)
      return response(bytes.slice(offset, offset + length), BigInt(bytes.length))
    })
    expect((await readAll(bytes)).content).toEqual(bytes)
  })

  it('stops when a page reports a different size or modification time', async () => {
    for (const changed of [response(fileBytes(8), 999n), response(fileBytes(8), 128n, '2026-09-11T00:00:01Z')]) {
      read.mockReset()
      read.mockResolvedValueOnce(response(new Uint8Array(), 128n)).mockResolvedValue(changed)
      await expect(readAll(fileBytes(128))).rejects.toThrow('changed while it was read')
    }
  })

  it('stops when a page returns no bytes', async () => {
    read.mockResolvedValue(response(new Uint8Array(), 128n))
    await expect(readAll(fileBytes(128))).rejects.toThrow('could not be read completely')
  })

  it('stops when a page overruns its own range', async () => {
    const bytes = fileBytes(PAGE * 2)
    read.mockResolvedValueOnce(response(new Uint8Array(), BigInt(bytes.length)))
    read.mockImplementation(async () => response(bytes, BigInt(bytes.length)))
    await expect(readAll(bytes)).rejects.toThrow('could not be read completely')
  })

  it('stops the pages that still run when one of them fails', async () => {
    const bytes = fileBytes(PAGE * 10)
    const signals: Array<AbortSignal | undefined> = []
    read.mockResolvedValueOnce(response(new Uint8Array(), BigInt(bytes.length)))
    read.mockImplementation(async (_worker, request, opts) => {
      signals.push(opts?.signal)
      await Promise.resolve()
      const offset = Number(request.offset ?? 0n)
      if (offset === PAGE * 2)
        throw new Error('The channel closed')
      return response(bytes.slice(offset, offset + Number(request.limit ?? 0n)), BigInt(bytes.length))
    })
    await expect(readAll(bytes)).rejects.toThrow('The channel closed')
    // The window stops rather than reading every remaining page.
    expect(read.mock.calls.length).toBeLessThan(11)
    expect(signals.every(signal => signal?.aborted)).toBe(true)
  })

  it('throws before it reads when the caller already aborted', async () => {
    const controller = new AbortController()
    controller.abort()
    await expect(readAll(fileBytes(64), controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(read).not.toHaveBeenCalled()
  })

  it('stops the pages when the caller aborts during the read', async () => {
    const bytes = fileBytes(PAGE * 10)
    const controller = new AbortController()
    read.mockResolvedValueOnce(response(new Uint8Array(), BigInt(bytes.length)))
    read.mockImplementation(async (_worker, request) => {
      controller.abort()
      await Promise.resolve()
      const offset = Number(request.offset ?? 0n)
      return response(bytes.slice(offset, offset + Number(request.limit ?? 0n)), BigInt(bytes.length))
    })
    await expect(readAll(bytes, controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(read.mock.calls.length).toBeLessThan(11)
  })
})
