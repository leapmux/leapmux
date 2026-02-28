const IMAGE_EXTENSIONS = new Set([
  'png',
  'jpg',
  'jpeg',
  'gif',
  'bmp',
  'webp',
  'svg',
  'ico',
  'avif',
])

const MARKDOWN_EXTENSIONS = new Set([
  'md',
  'markdown',
  'mdx',
])

/** Check if a file path has an image extension. */
export function isImageExtension(path: string): boolean {
  const ext = path.split('.').pop()?.toLowerCase() ?? ''
  return IMAGE_EXTENSIONS.has(ext)
}

/** Check if a file path has a markdown extension. */
export function isMarkdownExtension(path: string): boolean {
  const ext = path.split('.').pop()?.toLowerCase() ?? ''
  return MARKDOWN_EXTENSIONS.has(ext)
}

/** Check if a file path has an SVG extension. */
export function isSvgExtension(path: string): boolean {
  return (path.split('.').pop()?.toLowerCase() ?? '') === 'svg'
}

/** Check if content appears to be binary (contains null bytes or high ratio of non-printable chars). */
export function isBinaryContent(bytes: Uint8Array): boolean {
  const checkLen = Math.min(bytes.length, 512)
  let nonPrintable = 0
  for (let i = 0; i < checkLen; i++) {
    const b = bytes[i]
    if (b === 0)
      return true
    if (b < 7 || (b > 14 && b < 32 && b !== 27))
      nonPrintable++
  }
  return checkLen > 0 && nonPrintable / checkLen > 0.3
}

export type FileViewMode = 'text' | 'image' | 'binary' | 'markdown'

/** Detect the appropriate view mode for a file. */
export function detectFileViewMode(path: string, content: Uint8Array): FileViewMode {
  if (isImageExtension(path))
    return 'image'
  if (isMarkdownExtension(path))
    return 'markdown'
  if (isBinaryContent(content))
    return 'binary'
  return 'text'
}
