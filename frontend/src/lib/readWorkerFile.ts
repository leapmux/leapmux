import type { ReadFileResponse } from '~/generated/proto/leapmux/v1/file_pb'
import { readFile } from '~/api/workerRpc'

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
  let offset = response.content.length
  while (offset < complete.length) {
    signal?.throwIfAborted()
    // Leave room for metadata even on the smallest supported channel.
    const page = await readFile(workerId, { workerId, path, offset: BigInt(offset), limit: BigInt(Math.min(48 * 1024, complete.length - offset)) }, signal ? { signal } : undefined)
    signal?.throwIfAborted()
    if (page.totalSize !== response.totalSize || (page.modTime && response.modTime && page.modTime !== response.modTime))
      throw new Error('The file changed while it was read')
    if (page.content.length === 0 || offset + page.content.length > complete.length)
      throw new Error('The file could not be read completely')
    complete.set(page.content, offset)
    offset += page.content.length
  }
  return { ...response, content: complete }
}
