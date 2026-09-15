import type { ReadFileResponse } from '~/generated/proto/leapmux/v1/file_pb'
import { readFile } from '~/api/workerRpc'

/**
 * The most bytes one page request asks for.
 * The limit leaves room for the response metadata on the smallest supported channel.
 */
const PAGE_BYTES = 48 * 1024

/**
 * How many page requests run at the same time.
 * A worker whose operator configured a small message size splits one preview into
 * hundreds of pages, and a serial loop pays a full round trip for each of them.
 * The window caps how much of the channel one preview takes from the rest of the
 * application.
 */
const PAGE_CONCURRENCY = 4

/** Read a file within the preview limit. Return metadata only when the file exceeds that limit. */
export async function readWorkerFile(options: {
  workerId: string
  path: string
  maxBytes: number
  signal?: AbortSignal
}): Promise<ReadFileResponse> {
  const { workerId, path, maxBytes, signal } = options
  if (!Number.isSafeInteger(maxBytes) || maxBytes <= 0)
    throw new Error('The file preview limit is invalid')
  signal?.throwIfAborted()
  const response = await readFile(workerId, { workerId, path, limit: BigInt(maxBytes), metaOnlyIfTruncated: true }, signal ? { signal } : undefined)
  signal?.throwIfAborted()
  if (response.totalSize < 0n || BigInt(response.content.length) > response.totalSize)
    throw new Error('The file has an invalid size')
  if (response.totalSize > BigInt(maxBytes))
    return { ...response, content: new Uint8Array() }
  if (BigInt(response.content.length) === response.totalSize)
    return response

  const complete = new Uint8Array(Number(response.totalSize))
  complete.set(response.content)
  const ranges: Array<{ start: number, end: number }> = []
  for (let start = response.content.length; start < complete.length; start += PAGE_BYTES)
    ranges.push({ start, end: Math.min(start + PAGE_BYTES, complete.length) })

  // The pages share one controller, so the first failure stops the requests that
  // still run. Without it they read on against a file that already changed, and
  // their responses arrive for a result that nobody waits for.
  const pages = new AbortController()
  const forwardAbort = () => {
    if (!pages.signal.aborted)
      pages.abort(signal?.reason)
  }
  signal?.addEventListener('abort', forwardAbort, { once: true })

  /**
   * Fill one range. The loop stays inside the range because a page can answer
   * fewer bytes than the request asked for. It must never answer more: the extra
   * bytes belong to the next range, which another worker fills at the same time.
   */
  const readRange = async (range: { start: number, end: number }): Promise<void> => {
    let offset = range.start
    while (offset < range.end) {
      signal?.throwIfAborted()
      const page = await readFile(workerId, { workerId, path, offset: BigInt(offset), limit: BigInt(range.end - offset) }, { signal: pages.signal })
      signal?.throwIfAborted()
      if (page.totalSize !== response.totalSize || (page.modTime && response.modTime && page.modTime !== response.modTime))
        throw new Error('The file changed while it was read')
      if (page.content.length === 0 || offset + page.content.length > range.end)
        throw new Error('The file could not be read completely')
      complete.set(page.content, offset)
      offset += page.content.length
    }
  }

  let next = 0
  const readRemainingRanges = async (): Promise<void> => {
    while (next < ranges.length && !pages.signal.aborted)
      await readRange(ranges[next++])
  }

  try {
    await Promise.all(Array.from({ length: Math.min(PAGE_CONCURRENCY, ranges.length) }, () => readRemainingRanges()))
  }
  catch (error) {
    if (!pages.signal.aborted)
      pages.abort(error)
    throw error
  }
  finally {
    signal?.removeEventListener('abort', forwardAbort)
  }
  return { ...response, content: complete }
}
