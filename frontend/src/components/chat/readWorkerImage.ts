import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { getImageMimeType } from '~/lib/fileType'
import { MAX_FILE_IMAGE_BYTES, RENDERABLE_IMAGE_MIME_TYPES } from '~/lib/imageBlocks'
import { sniffImageDimensionsFromBase64, sniffImageDimensionsFromBytes } from '~/lib/imageDimensions'
import { fileUriToPath, isAbsolute, join } from '~/lib/paths'
import { readWorkerFile, readWorkerFileHead } from '~/lib/readWorkerFile'

/**
 * The bytes of the first read for the size of a file image. The size of a PNG,
 * a GIF, and a WebP lies at a fixed place in the first bytes, and most JPEG and
 * AVIF headers end inside this read.
 */
export const FILE_IMAGE_HEADER_BYTES = 4096
/**
 * The bytes of the second read, for a JPEG or an AVIF whose header is longer
 * than the first read. It equals the scan budget of the sniffer for a JPEG
 * (see `~/lib/imageDimensions`): a larger read finds nothing more.
 */
export const FILE_IMAGE_HEADER_MAX_BYTES = 256 * 1024

/** Recognize common image signatures when the file has no useful extension. */
function fileImageMime(path: string, bytes: Uint8Array): string {
  const head = new TextDecoder('latin1').decode(bytes.subarray(0, 64))
  if (bytes[0] === 0x89 && head.slice(1, 4) === 'PNG')
    return 'image/png'
  if (bytes[0] === 0xFF && bytes[1] === 0xD8 && bytes[2] === 0xFF)
    return 'image/jpeg'
  if (head.startsWith('GIF87a') || head.startsWith('GIF89a'))
    return 'image/gif'
  if (head.startsWith('RIFF') && head.slice(8, 12) === 'WEBP')
    return 'image/webp'
  if (head.slice(4, 8) === 'ftyp' && /avif|avis/.test(head.slice(8)))
    return 'image/avif'
  return getImageMimeType(path)
}

/**
 * The absolute path on the worker of the image that a chat row gives as `path`:
 * a path, or a `file:` URI. A relative path resolves against `workingDir`.
 */
function workerImagePath(workerId: string, path: string, workingDir: string | undefined): string {
  if (!workerId || !path)
    throw new Error('The image file is unavailable')
  const filePath = path.startsWith('file:')
    ? fileUriToPath(path)
    : path
  if (!filePath || (/^[a-z][a-z\d+.-]*:\/\//i.test(filePath) && !/^[a-z]:/i.test(filePath)))
    throw new Error('The image does not have a local file path')
  if (isAbsolute(filePath))
    return filePath
  if (!workingDir)
    throw new Error('The image working directory is unavailable')
  return join([workingDir, filePath])
}

/**
 * Read the size of a local image from the first bytes of its file, so that a
 * chat row can reserve the image box before it reads the image. Resolves
 * `undefined` when the header gives no size. A failed read rejects.
 *
 * At most two requests: FILE_IMAGE_HEADER_BYTES, and for a JPEG or an AVIF with
 * a longer header, FILE_IMAGE_HEADER_MAX_BYTES once.
 */
export async function readWorkerImageSize(workerId: string, path: string, workingDir: string | undefined, signal: AbortSignal): Promise<ImageDimensions | undefined> {
  const filePath = workerImagePath(workerId, path, workingDir)
  const head = await readWorkerFileHead({
    workerId,
    path: filePath,
    bytes: FILE_IMAGE_HEADER_BYTES,
    signal,
  })
  const size = sniffImageDimensionsFromBytes(head.content)
  if (size)
    return size
  // Only these two formats keep their size behind a header of no fixed length.
  const mimeType = fileImageMime(filePath, head.content)
  const headerCanBeLonger = mimeType === 'image/jpeg' || mimeType === 'image/avif'
  if (!headerCanBeLonger || head.totalSize <= BigInt(head.content.length))
    return undefined
  const longer = await readWorkerFileHead({
    workerId,
    path: filePath,
    bytes: FILE_IMAGE_HEADER_MAX_BYTES,
    signal,
  })
  return sniffImageDimensionsFromBytes(longer.content) ?? undefined
}

/** Read a local image through the worker's existing encrypted file API. */
export async function readWorkerImage(workerId: string, path: string, workingDir: string | undefined, signal: AbortSignal): Promise<ImageResultSource> {
  const filePath = workerImagePath(workerId, path, workingDir)
  const response = await readWorkerFile({
    workerId,
    path: filePath,
    maxBytes: MAX_FILE_IMAGE_BYTES,
    signal,
  })
  if (response.totalSize > BigInt(MAX_FILE_IMAGE_BYTES) || response.content.length > MAX_FILE_IMAGE_BYTES)
    throw new Error('The image file is too large to preview')
  if (response.totalSize === 0n)
    throw new Error('The image file is empty')
  const content = response.content
  const mimeType = fileImageMime(filePath, content)
  if (!RENDERABLE_IMAGE_MIME_TYPES.has(mimeType))
    throw new Error('The image file format is not supported')
  const data = uint8ArrayToBase64(content)
  const dimensions = sniffImageDimensionsFromBase64(data) ?? undefined
  return {
    filePath,
    data,
    mimeType,
    ...(dimensions !== undefined
      ? { dimensions }
      : {}),
  }
}
