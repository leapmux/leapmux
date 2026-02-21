import { decompress as fzstdDecompress } from 'fzstd'
import { ContentCompression } from '~/generated/leapmux/v1/agent_pb'

const textDecoder = new TextDecoder()

/**
 * Decompress message content bytes based on the compression algorithm.
 * Returns the decompressed bytes, or null for unknown/unsupported compression.
 */
export function decompressContent(
  content: Uint8Array,
  compression: ContentCompression,
): Uint8Array | null {
  switch (compression) {
    case ContentCompression.ZSTD:
      return fzstdDecompress(content)
    case ContentCompression.NONE:
      return content
    default:
      return null
  }
}

/**
 * Decompress message content and decode as a UTF-8 string.
 * Returns null for unknown/unsupported compression.
 */
export function decompressContentToString(
  content: Uint8Array,
  compression: ContentCompression,
): string | null {
  const decompressed = decompressContent(content, compression)
  if (decompressed === null)
    return null
  return textDecoder.decode(decompressed)
}
