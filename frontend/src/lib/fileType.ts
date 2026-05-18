import { extname } from './paths'

// Single source of truth for image extension → MIME-type mapping.
// `IMAGE_EXTENSIONS` is derived from these keys so adding a new image
// format here automatically updates `isImageExtension` too.
const IMAGE_MIME_BY_EXT: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  bmp: 'image/bmp',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  ico: 'image/x-icon',
  avif: 'image/avif',
}

const IMAGE_EXTENSIONS = new Set(Object.keys(IMAGE_MIME_BY_EXT))

const MARKDOWN_EXTENSIONS = new Set([
  'md',
  'markdown',
  'mdx',
])

/**
 * Extensions that we treat as binary up front, without ever fetching a
 * byte. Used by `FileViewer` to skip the 256 KiB probe read for files
 * we already know we can't render inline (archives, executables, office
 * docs, PDFs, fonts, media, databases, disk images).
 *
 * Membership is a deny-list signal only — the cost of a false positive
 * is a single user click on "Show anyway" to render the bytes anyway.
 * Files with no extension or with an extension absent from this set
 * still fall through to the fetch+probe path where `isBinaryContent`
 * decides.
 */
const LIKELY_BINARY_EXTENSIONS = new Set([
  // Archives
  'zip',
  'tar',
  'gz',
  'tgz',
  '7z',
  'rar',
  'bz2',
  'xz',
  'zst',
  // Executables / objects
  'exe',
  'dll',
  'so',
  'dylib',
  'a',
  'o',
  'lib',
  'obj',
  'bin',
  'dat',
  'pak',
  // Office / PDF
  'pdf',
  'doc',
  'docx',
  'xls',
  'xlsx',
  'ppt',
  'pptx',
  'odt',
  'ods',
  'odp',
  // JVM / .NET
  'jar',
  'war',
  'class',
  'dex',
  // Media (non-image)
  'mp3',
  'mp4',
  'mkv',
  'avi',
  'mov',
  'wav',
  'flac',
  'ogg',
  'webm',
  'm4a',
  'aac',
  // Fonts
  'ttf',
  'otf',
  'woff',
  'woff2',
  'eot',
  // Databases
  'db',
  'sqlite',
  'sqlite3',
  // Disk images / firmware
  'iso',
  'img',
  'dmg',
])

// Ext-aware predicates: callers that have already extracted the
// extension (e.g. via a shared `createMemo(() => extname(path))`)
// avoid re-running `extname` once per helper. The path-based versions
// below are thin wrappers for callers that don't have a pre-extracted
// extension handy.

/** Image extension check against a pre-extracted extension. */
export function isImageExt(ext: string): boolean {
  return IMAGE_EXTENSIONS.has(ext)
}

/** Markdown extension check against a pre-extracted extension. */
export function isMarkdownExt(ext: string): boolean {
  return MARKDOWN_EXTENSIONS.has(ext)
}

/** SVG extension check against a pre-extracted extension. */
export function isSvgExt(ext: string): boolean {
  return ext === 'svg'
}

/** Binary-deny-list check against a pre-extracted extension. */
export function isLikelyBinaryExt(ext: string): boolean {
  return LIKELY_BINARY_EXTENSIONS.has(ext)
}

/**
 * MIME type for a pre-extracted extension that is one of
 * `IMAGE_EXTENSIONS`. Returns `application/octet-stream` for unknown
 * extensions so callers can hand the result straight to
 * `new Blob({ type })`.
 */
export function imageMimeFromExt(ext: string): string {
  return IMAGE_MIME_BY_EXT[ext] ?? 'application/octet-stream'
}

/** Check if a file path has an image extension. */
export function isImageExtension(path: string): boolean {
  return isImageExt(extname(path))
}

/** MIME type for an image path; see `imageMimeFromExt`. */
export function getImageMimeType(path: string): string {
  return imageMimeFromExt(extname(path))
}

/** Check if a file path has a markdown extension. */
export function isMarkdownExtension(path: string): boolean {
  return isMarkdownExt(extname(path))
}

/** Check if a file path has an SVG extension. */
export function isSvgExtension(path: string): boolean {
  return isSvgExt(extname(path))
}

/**
 * Whether the path's extension is one we treat as binary up front, so
 * the FileViewer can skip the 256 KiB probe read. Returns false for
 * paths with no extension and for any extension not in the deny-list.
 */
export function isLikelyBinaryExtension(path: string): boolean {
  return isLikelyBinaryExt(extname(path))
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

/** Detect the appropriate view mode from a pre-extracted extension. */
export function detectFileViewModeFromExt(ext: string, content: Uint8Array): FileViewMode {
  if (isImageExt(ext))
    return 'image'
  if (isMarkdownExt(ext))
    return 'markdown'
  if (isBinaryContent(content))
    return 'binary'
  return 'text'
}

/** Detect the appropriate view mode for a file path. */
export function detectFileViewMode(path: string, content: Uint8Array): FileViewMode {
  return detectFileViewModeFromExt(extname(path), content)
}
