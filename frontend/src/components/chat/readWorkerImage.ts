import type { ImageResultSource } from '~/lib/imageBlocks'
import { uint8ArrayToBase64 } from '~/lib/base64'
import { getImageMimeType } from '~/lib/fileType'
import { MAX_FILE_IMAGE_BYTES, RENDERABLE_IMAGE_MIME_TYPES } from '~/lib/imageBlocks'
import { sniffImageDimensionsFromBase64 } from '~/lib/imageDimensions'
import { fileUriToPath, isAbsolute, join } from '~/lib/paths'
import { readWorkerFile } from '~/lib/readWorkerFile'

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

/** Read a local image through the worker's existing encrypted file API. */
export async function readWorkerImage(workerId: string, path: string, workingDir: string | undefined, signal: AbortSignal): Promise<ImageResultSource> {
  if (!workerId || !path)
    throw new Error('The image file is unavailable')
  let filePath = path.startsWith('file:') ? fileUriToPath(path) : path
  if (!filePath || (/^[a-z][a-z\d+.-]*:\/\//i.test(filePath) && !/^[a-z]:/i.test(filePath)))
    throw new Error('The image does not have a local file path')
  if (!isAbsolute(filePath)) {
    if (!workingDir)
      throw new Error('The image working directory is unavailable')
    filePath = join([workingDir, filePath])
  }
  const response = await readWorkerFile({ workerId, path: filePath, maxBytes: MAX_FILE_IMAGE_BYTES, signal })
  if (response.totalSize > BigInt(MAX_FILE_IMAGE_BYTES) || response.content.length > MAX_FILE_IMAGE_BYTES)
    throw new Error('The image file is too large to preview')
  if (response.totalSize === 0n)
    throw new Error('The image file is empty')
  const content = response.content
  const mimeType = fileImageMime(filePath, content)
  if (!RENDERABLE_IMAGE_MIME_TYPES.has(mimeType))
    throw new Error('The image file format is not supported')
  const data = uint8ArrayToBase64(content)
  return { filePath, data, mimeType, dimensions: sniffImageDimensionsFromBase64(data) ?? undefined }
}
