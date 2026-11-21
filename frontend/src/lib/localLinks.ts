import type { PathFlavor } from './paths'
import {
  cleanPath,
  filesystemRoot,
  isAbsolute,
  isFilesystemRoot,
  resolvePath,
  untildify,
} from './paths'

/** Attribute marking a local file/directory hyperlink span emitted by the Markdown worker. */
export const LOCAL_LINK_ATTRIBUTE = 'data-local-link'
/** Attribute placed on MarkdownText's root when local links and images are interactive. */
export const LOCAL_LINKS_ACTIVE_ATTRIBUTE = 'data-local-links-active'
/** Attribute carrying the raw local image source path on a worker-emitted placeholder span. */
export const LOCAL_IMAGE_ATTRIBUTE = 'data-local-image'
/** Attribute carrying the image alt text on a worker-emitted local image span. */
export const LOCAL_IMAGE_ALT_ATTRIBUTE = 'data-local-image-alt'
/** Attribute marking a standalone hydrated local image that acts as a clickable File Viewer link. */
export const LOCAL_IMAGE_CLICKABLE_ATTRIBUTE = 'data-local-image-clickable'
/** Marker class on local image wrapper spans emitted by the Markdown worker. */
export const LOCAL_IMAGE_CLASS = 'local-markdown-image'
/** Marker class on the text fallback inside a local image wrapper span. */
export const LOCAL_IMAGE_FALLBACK_CLASS = 'local-markdown-image-fallback'
/** Marker class on the error reason appended when a local image fails to load. */
export const LOCAL_IMAGE_ERROR_CLASS = 'local-markdown-image-error'

export interface ParsedLocalLink {
  raw: string
  path: string
  pathWithoutLineSuffix?: string
  fromFileUri: boolean
  isTilde: boolean
}

export interface LocalLinkResolveOptions {
  baseDir: string
  workingDir: string
  homeDir: string
  flavor: PathFlavor
}

const DOUBLE_SLASH_START_RE = /^[\\/]{2}/
const FILE_SCHEME_RE = /^file:/i
const LOCAL_FILE_URI_RE = /^file:\/\/(?:localhost)?(\/.*)$/i
const LEADING_SLASH_WIN_DRIVE_RE = /^\/[A-Z]:[\\/]/i
const FILE_URI_DRIVE_RELATIVE_RE = /^\/?[A-Z]:(?![\\/])/i
const WIN_DRIVE_PATH_RE = /^[A-Z]:[\\/]/i
// `remark-rehype` (`mdast-util-to-hast`) percent-encodes `\` as `%5C` in link
// hrefs and image srcs, so a Markdown Windows path like `C:\repo\x.png` arrives
// here as `C:%5Crepo%5Cx.png`. Accept raw slash or percent-encoded slash here;
// post-decode validation still rejects every drive-relative `C:foo` path.
const WIN_DRIVE_RAW_PATH_RE = /^[A-Z]:(?:[\\/]|%5C|%2F)/i
const WIN_DRIVE_RELATIVE_RE = /^[A-Z]:(?![\\/])/i
const LINE_SUFFIX_RE = /:\d+(?::\d+|-\d+)?$/
const FIRST_SEGMENT_LINE_RE = /^\d+(?::\d+|-\d+)?$/
const LEADING_SLASHES_RE = /^\/+/
const LEADING_ANY_SEPS_RE = /^[\\/]+/

/** Reject NUL and ASCII control characters (0x00-0x1F, 0x7F). */
function hasControlChar(value: string): boolean {
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i)
    if (code <= 0x1F || code === 0x7F)
      return true
  }
  return false
}

/** Percent-decode `value`, falling back to the raw text on malformed escape sequences. */
function safeDecodeURIComponent(value: string): string {
  try {
    return decodeURIComponent(value)
  }
  catch {
    return value
  }
}

/** Strip `?query` and `#fragment` from a non-URI local path string. */
function stripQueryAndFragment(value: string): string {
  const queryIndex = value.indexOf('?')
  const hashIndex = value.indexOf('#')
  let end = value.length
  if (queryIndex >= 0 && queryIndex < end)
    end = queryIndex
  if (hashIndex >= 0 && hashIndex < end)
    end = hashIndex
  return value.slice(0, end)
}

/** Extract the first path segment before any separator, query, or fragment delimiter. */
function firstSegmentOf(value: string): string {
  let end = value.length
  for (let i = 0; i < value.length; i++) {
    const ch = value[i]
    if (ch === '/' || ch === '\\' || ch === '?' || ch === '#') {
      end = i
      break
    }
  }
  return value.slice(0, end)
}

/** Build the parsed link record, extracting an optional `:line` / `:line:col` / `:line-end` suffix. */
function buildParsedLink(raw: string, path: string, fromFileUri: boolean): ParsedLocalLink {
  const isTilde = !fromFileUri && (path === '~' || path.startsWith('~/') || path.startsWith('~\\'))
  const lineMatch = LINE_SUFFIX_RE.exec(path)
  const pathWithoutLineSuffix = lineMatch && lineMatch.index > 0
    ? path.slice(0, lineMatch.index)
    : undefined
  return {
    raw,
    path,
    ...(pathWithoutLineSuffix !== undefined
      ? { pathWithoutLineSuffix }
      : {}),
    fromFileUri,
    isTilde,
  }
}

/**
 * Classify and parse a raw Markdown link href or image src as a local filesystem path.
 *
 * Returns `null` for remote URLs, non-local `file://host/...` URIs, UNC or mixed-slash
 * double-slash prefixes (raw or percent-encoded), Windows drive-relative paths, fragment/query-only
 * links, control characters, and unknown URI schemes.
 */
export function parseLocalLinkHref(rawHref: string): ParsedLocalLink | null {
  const trimmed = rawHref.trim()
  if (!trimmed || hasControlChar(trimmed))
    return null
  if (trimmed.startsWith('#') || trimmed.startsWith('?'))
    return null
  if (DOUBLE_SLASH_START_RE.test(trimmed))
    return null

  if (FILE_SCHEME_RE.test(trimmed)) {
    const localFileMatch = LOCAL_FILE_URI_RE.exec(trimmed)
    if (!localFileMatch)
      return null
    // Reject UNC-form `file:////host/share`, `file://localhost//host/share`, and
    // mixed-slash / percent-encoded equivalents BEFORE `new URL()`: Chromium's URL
    // parser collapses extra leading slashes in `file:////host/share` to a single
    // leading slash (`pathname === '/host/share'`), unlike Node and Bun.
    const rawUriPath = localFileMatch[1]!
    if (DOUBLE_SLASH_START_RE.test(rawUriPath))
      return null
    const rawUriPathDecoded = safeDecodeURIComponent(stripQueryAndFragment(rawUriPath))
    if (DOUBLE_SLASH_START_RE.test(rawUriPathDecoded))
      return null
    let url: URL
    try {
      url = new URL(trimmed)
    }
    catch {
      return null
    }
    if (url.protocol !== 'file:')
      return null
    const host = url.hostname.toLowerCase()
    if (host !== '' && host !== 'localhost')
      return null
    const rawPathname = url.pathname
    if (!rawPathname || DOUBLE_SLASH_START_RE.test(rawPathname))
      return null
    let decoded = safeDecodeURIComponent(rawPathname)
    if (!decoded || hasControlChar(decoded) || DOUBLE_SLASH_START_RE.test(decoded))
      return null
    if (FILE_URI_DRIVE_RELATIVE_RE.test(decoded))
      return null
    if (LEADING_SLASH_WIN_DRIVE_RE.test(decoded))
      decoded = decoded.slice(1)
    return buildParsedLink(rawHref, decoded, true)
  }

  if (!WIN_DRIVE_RAW_PATH_RE.test(trimmed)) {
    if (WIN_DRIVE_RELATIVE_RE.test(trimmed))
      return null
    const firstSegment = firstSegmentOf(trimmed)
    const colonIndex = firstSegment.indexOf(':')
    if (colonIndex >= 0) {
      const prefix = firstSegment.slice(0, colonIndex)
      const remainder = firstSegment.slice(colonIndex + 1)
      if (!prefix.includes('.') || !FIRST_SEGMENT_LINE_RE.test(remainder))
        return null
    }
  }

  const stripped = stripQueryAndFragment(trimmed)
  if (!stripped)
    return null
  const decoded = safeDecodeURIComponent(stripped)
  if (!decoded || hasControlChar(decoded) || DOUBLE_SLASH_START_RE.test(decoded))
    return null
  if (WIN_DRIVE_RELATIVE_RE.test(decoded))
    return null

  return buildParsedLink(rawHref, decoded, false)
}

/** True when `rawHref` classifies as a safe local filesystem path or local `file:` URI. */
export function isLocalLinkHref(rawHref: string): boolean {
  return parseLocalLinkHref(rawHref) !== null
}

/** Resolve one parsed path variant into ordered candidate paths before filtering/deduping. */
function resolvePathVariant(
  item: string,
  parsed: ParsedLocalLink,
  opts: LocalLinkResolveOptions,
): string[] {
  const results: string[] = []
  const pushValid = (candidate: string) => {
    if (!candidate)
      return
    if (DOUBLE_SLASH_START_RE.test(candidate))
      return
    if (filesystemRoot(candidate, opts.flavor) === undefined)
      return
    results.push(candidate)
  }

  if (parsed.isTilde) {
    if (!opts.homeDir)
      return results
    pushValid(cleanPath(untildify(item, opts.homeDir, opts.flavor), opts.flavor))
    return results
  }

  if (!parsed.fromFileUri && item.startsWith('/') && !WIN_DRIVE_PATH_RE.test(item)) {
    if (opts.flavor === 'posix') {
      pushValid(cleanPath(item, 'posix'))
      if (opts.workingDir && !isFilesystemRoot(opts.workingDir, 'posix')) {
        const stripped = item.replace(LEADING_SLASHES_RE, '')
        if (stripped)
          pushValid(resolvePath(opts.workingDir, stripped, 'posix'))
      }
      return results
    }

    const stripped = item.replace(LEADING_ANY_SEPS_RE, '')
    if (opts.workingDir) {
      pushValid(stripped
        ? resolvePath(opts.workingDir, stripped, 'win32')
        : cleanPath(opts.workingDir, 'win32'))
    }
    const base = opts.baseDir || opts.workingDir
    if (base)
      pushValid(resolvePath(base, item, 'win32'))
    return results
  }

  if (opts.flavor === 'posix') {
    if (isAbsolute(item, 'posix')) {
      pushValid(cleanPath(item, 'posix'))
    }
    else {
      const base = opts.baseDir || opts.workingDir
      if (base)
        pushValid(resolvePath(base, item, 'posix'))
    }
    return results
  }

  if (filesystemRoot(item, 'win32') !== undefined) {
    pushValid(cleanPath(item, 'win32'))
  }
  else {
    const base = opts.baseDir || opts.workingDir
    if (base)
      pushValid(resolvePath(base, item, 'win32'))
  }
  return results
}

/**
 * Resolve a raw local link href or image src into deduplicated absolute candidate paths
 * in probe order.
 */
export function resolveLocalLinkCandidates(rawHref: string, opts: LocalLinkResolveOptions): string[] {
  const parsed = parseLocalLinkHref(rawHref)
  if (!parsed)
    return []

  const variants = parsed.pathWithoutLineSuffix !== undefined
    ? (opts.flavor === 'win32'
        ? [parsed.pathWithoutLineSuffix, parsed.path]
        : [parsed.path, parsed.pathWithoutLineSuffix])
    : [parsed.path]

  const seen = new Set<string>()
  const out: string[] = []
  for (const variant of variants) {
    for (const candidate of resolvePathVariant(variant, parsed, opts)) {
      if (seen.has(candidate))
        continue
      seen.add(candidate)
      out.push(candidate)
    }
  }
  return out
}

/**
 * Primary display path for hovering a local link or standalone local image.
 * Prefers the line-stripped path for `file:line` references when resolvable.
 */
export function tooltipPathForLocalLink(rawHref: string, opts: LocalLinkResolveOptions): string | undefined {
  const parsed = parseLocalLinkHref(rawHref)
  if (!parsed)
    return undefined
  const candidates = resolveLocalLinkCandidates(rawHref, opts)
  if (candidates.length === 0)
    return undefined
  if (parsed.pathWithoutLineSuffix !== undefined) {
    for (const candidate of resolvePathVariant(parsed.pathWithoutLineSuffix, parsed, opts)) {
      if (candidates.includes(candidate))
        return candidate
    }
  }
  return candidates[0]
}
