import type { ImageResultSource } from '~/lib/imageBlocks'
import type { ImageDimensions } from '~/lib/imageDimensions'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { getImageMimeType } from '~/lib/fileType'
import { MAX_FILE_IMAGE_BYTES, RENDERABLE_IMAGE_MIME_TYPES } from '~/lib/imageBlocks'
import { imageHeaderBytesNeeded, sniffImageDimensionsFromBytes } from '~/lib/imageDimensions'
import { fileUriToPath, isAbsolute, join } from '~/lib/paths'
import { readWorkerFile, readWorkerFileHead } from '~/lib/readWorkerFile'
import { FileImageRefusedError } from './fileImageResolver'

/**
 * The bytes of the first read for the size of a file image. The size of a PNG,
 * a GIF, and a WebP lies at a fixed place in the first bytes, and most JPEG and
 * AVIF headers end inside this read.
 */
export const FILE_IMAGE_HEADER_BYTES = 4096

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
 * Why the chat shows no image for a file of `totalSize` bytes and of `mimeType`, or
 * undefined for a file that it shows. A new read of the same file gives the same
 * answer, so `readWorkerImage` throws it as a FileImageRefusedError.
 */
function fileImageRefusal(totalSize: bigint, mimeType: string): string | undefined {
  if (totalSize > BigInt(MAX_FILE_IMAGE_BYTES))
    return 'The image file is too large to preview'
  if (totalSize === 0n)
    return 'The image file is empty'
  if (!RENDERABLE_IMAGE_MIME_TYPES.has(mimeType))
    return 'The image file format is not supported'
  return undefined
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
 * `undefined` when the header gives no size, and for a file that
 * `readWorkerImage` refuses: no image ever takes the box that a size reserves
 * for such a file. A failed read rejects.
 *
 * At most two requests: FILE_IMAGE_HEADER_BYTES, and one longer read for a
 * format whose size lies behind a longer header (see imageHeaderBytesNeeded).
 */
export async function readWorkerImageSize(workerId: string, path: string, workingDir: string | undefined, signal: AbortSignal): Promise<ImageDimensions | undefined> {
  const filePath = workerImagePath(workerId, path, workingDir)
  const head = await readWorkerFileHead({
    workerId,
    path: filePath,
    bytes: FILE_IMAGE_HEADER_BYTES,
    signal,
  })
  // With no size, the row reads the image, and draws the refusal of that read.
  if (fileImageRefusal(head.totalSize, fileImageMime(filePath, head.content)) !== undefined)
    return undefined
  const size = sniffImageDimensionsFromBytes(head.content)
  if (size)
    return size
  // The sniffer says how many bytes it scans of this format. More bytes help only when
  // it scans more than this read gave, and the file holds more.
  const needed = imageHeaderBytesNeeded(head.content)
  if (needed === null || needed <= head.content.length || head.totalSize <= BigInt(head.content.length))
    return undefined
  const longer = await readWorkerFileHead({
    workerId,
    path: filePath,
    bytes: needed,
    signal,
  })
  return sniffImageDimensionsFromBytes(longer.content) ?? undefined
}

/**
 * Read a local image through the worker's existing encrypted file API. Rejects with a
 * FileImageRefusedError for a file that the chat shows no image of, whatever a new read
 * of it gives: a file that is too large, an empty file, and a format that it does not show.
 */
export async function readWorkerImage(workerId: string, path: string, workingDir: string | undefined, signal: AbortSignal): Promise<ImageResultSource> {
  const filePath = workerImagePath(workerId, path, workingDir)
  const response = await readWorkerFile({
    workerId,
    path: filePath,
    maxBytes: MAX_FILE_IMAGE_BYTES,
    signal,
  })
  // readWorkerFile gives no content for a file above the limit, and never more content
  // than the size of the file. So the size of the file alone states "too large".
  const content = response.content
  const mimeType = fileImageMime(filePath, content)
  const refusal = fileImageRefusal(response.totalSize, mimeType)
  if (refusal !== undefined)
    throw new FileImageRefusedError(refusal)
  const dimensions = sniffImageDimensionsFromBytes(content) ?? undefined
  return {
    filePath,
    data: uint8ArrayToBase64(content),
    mimeType,
    ...(dimensions !== undefined
      ? { dimensions }
      : {}),
  }
}
