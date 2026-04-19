// OS-aware filesystem path helpers. Callers without an explicit flavor get a
// best-effort sniff from the path (drive letter / UNC prefix → win32, else
// posix); callers that know the worker OS should pass `flavor` to override.

export type PathFlavor = 'win32' | 'posix'

const DRIVE_LETTER_RE = /^[A-Z]:[\\/]/i
const SEP_SPLIT_RE = /[\\/]+/
const LEADING_SEP_RE = /^[\\/]+/
const TRAILING_SEP_RE = /[\\/]+$/
const FWD_SLASH_G = /\//g
const BACK_SLASH_G = /\\/g

function isSep(ch: string): boolean {
  return ch === '\\' || ch === '/'
}

// Parse a \\server\share prefix. Returns the normalized `\\server\share`
// volume and the remainder, or null if the input isn't a well-formed UNC.
function parseUncHead(p: string): { volume: string, rest: string } | null {
  if (!p.startsWith('\\\\'))
    return null
  const n = p.length
  let i = 2
  while (i < n && !isSep(p[i])) i++
  if (i === 2 || i === n)
    return null
  const server = p.slice(2, i)
  i++
  const shareStart = i
  while (i < n && !isSep(p[i])) i++
  if (i === shareStart)
    return null
  return { volume: `\\\\${server}\\${p.slice(shareStart, i)}`, rest: p.slice(i) }
}

export function detectFlavor(p: string): PathFlavor {
  if (!p)
    return 'posix'
  if (p.startsWith('\\') || DRIVE_LETTER_RE.test(p))
    return 'win32'
  return 'posix'
}

/** Map a worker's reported OS (e.g. "windows", "linux") to a flavor. */
export function flavorFromOs(os?: string): PathFlavor {
  return os?.toLowerCase() === 'windows' ? 'win32' : 'posix'
}

function flavorOf(p: string, flavor?: PathFlavor): PathFlavor {
  return flavor ?? detectFlavor(p)
}

/** Native separator for the given flavor. */
export function sep(flavor: PathFlavor): string {
  return flavor === 'win32' ? '\\' : '/'
}

// Index of the last separator in `p`. On posix only `/` counts; on win32
// either `/` or `\` is accepted. Returns -1 when there's no separator.
export function lastSepIndex(p: string, flavor: PathFlavor): number {
  return flavor === 'win32'
    ? Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
    : p.lastIndexOf('/')
}

// Strip everything from the last occurrence of `separator` onward. Returns
// '' at or above the root. Distinct from `parentDirectory`: this strips a
// trailing separator in place (`foo/` → `foo`), where `parentDirectory`
// treats the trailing sep as part of the segment and goes up a level
// (`foo/` → root).
export function trimLastSegment(p: string, separator: string): string {
  const i = p.lastIndexOf(separator)
  return i <= 0 ? '' : p.substring(0, i)
}

// POSIX: starts with /
// Win32: drive-letter (C:\, C:/), UNC (\\server\share), rooted (\foo or /foo)
export function isAbsolute(p: string, flavor?: PathFlavor): boolean {
  const f = flavorOf(p, flavor)
  if (f === 'win32')
    return p.startsWith('\\') || p.startsWith('/') || DRIVE_LETTER_RE.test(p)
  return p.startsWith('/')
}

// Split a path into non-empty components. On Win32 the volume (`C:` or
// `\\srv\share`) is emitted as a single leading segment.
export function split(p: string, flavor?: PathFlavor): string[] {
  if (!p)
    return []
  const f = flavorOf(p, flavor)
  if (f === 'win32') {
    let volume = ''
    let rest = p
    if (DRIVE_LETTER_RE.test(p)) {
      volume = p.slice(0, 2)
      rest = p.slice(2)
    }
    else {
      const unc = parseUncHead(p)
      if (unc) {
        volume = unc.volume
        rest = unc.rest
      }
    }
    const parts = rest.split(SEP_SPLIT_RE).filter(Boolean)
    return volume ? [volume, ...parts] : parts
  }
  return p.split('/').filter(Boolean)
}

// Build cumulative breadcrumb segments. Each entry carries its displayable
// `name` and the absolute `path` that navigating to it would refer to.
export function pathSegments(
  p: string,
  flavor?: PathFlavor,
): Array<{ name: string, path: string }> {
  if (!p)
    return []
  const f = flavorOf(p, flavor)
  const parts = split(p, f)
  if (parts.length === 0)
    return []
  const s = sep(f)
  const result: Array<{ name: string, path: string }> = []
  if (f === 'win32') {
    const [volume, ...rest] = parts
    result.push({ name: `${volume}${s}`, path: `${volume}${s}` })
    let acc = volume
    for (const part of rest) {
      acc = acc.endsWith(s) ? `${acc}${part}` : `${acc}${s}${part}`
      result.push({ name: part, path: acc })
    }
    return result
  }
  let acc = ''
  for (const part of parts) {
    acc = `${acc}/${part}`
    result.push({ name: part, path: acc })
  }
  return result
}

export function join(parts: string[], flavor?: PathFlavor): string {
  const filtered = parts.filter(p => p !== undefined && p !== null && p !== '')
  if (filtered.length === 0)
    return ''
  const f = flavorOf(filtered[0], flavor)
  const s = sep(f)
  const out: string[] = []
  for (let i = 0; i < filtered.length; i++) {
    let piece = filtered[i]
    if (i > 0)
      piece = piece.replace(LEADING_SEP_RE, '')
    if (i < filtered.length - 1)
      piece = piece.replace(TRAILING_SEP_RE, '')
    if (piece !== '')
      out.push(piece)
  }
  if (out.length === 0)
    return ''
  let joined = out.join(s)
  if (f === 'win32')
    joined = joined.replace(FWD_SLASH_G, '\\')
  return joined
}

// Parent directory of `p`. For a root, returns the root itself.
export function parentDirectory(p: string, flavor?: PathFlavor): string {
  if (!p)
    return ''
  const f = flavorOf(p, flavor)
  if (f === 'posix') {
    const i = lastSepIndex(p, f)
    return i <= 0 ? '/' : p.substring(0, i)
  }
  // Fall back to segment-aware logic for Win32 volume handling.
  const parts = split(p, f)
  const s = sep(f)
  if (parts.length <= 1)
    return parts.length === 1 ? `${parts[0]}${s}` : p
  const [volume, ...rest] = parts
  if (rest.length === 1)
    return `${volume}${s}`
  return `${volume}${s}${rest.slice(0, -1).join(s)}`
}

// Last component of the path, or empty string if none.
export function basename(p: string, flavor?: PathFlavor): string {
  if (!p)
    return ''
  const f = flavorOf(p, flavor)
  const i = lastSepIndex(p, f)
  // Strip any leading volume to avoid returning 'C:' as a basename.
  const tail = i < 0 ? p : p.substring(i + 1)
  if (tail)
    return tail
  // No tail after the last separator: fall back to segment-aware split so
  // `basename('C:\\')` returns the volume, `basename('/')` returns ''.
  const parts = split(p, f)
  return parts.length === 0 ? '' : parts[parts.length - 1]
}

function normalizeSeparators(p: string, flavor: PathFlavor): string {
  return flavor === 'win32'
    ? p.replace(FWD_SLASH_G, '\\')
    : p.replace(BACK_SLASH_G, '/')
}

// Convert any flavor's separators to posix `/`. Useful for comparing against
// git-reported paths, which always use `/` regardless of host OS.
export function toPosixSeparators(p: string): string {
  return p.replace(BACK_SLASH_G, '/')
}

// Case-insensitive on win32, byte-exact on posix.
function pathEq(a: string, b: string, flavor: PathFlavor): boolean {
  return flavor === 'win32'
    ? a.toLowerCase() === b.toLowerCase()
    : a === b
}

// Returns '' when abs === base, the slice after `base + sep` when abs is
// strictly under base, or null otherwise. `base` must have no trailing sep;
// both inputs must already use the flavor's separator.
export function relativeUnder(abs: string, base: string, flavor: PathFlavor): string | null {
  if (pathEq(abs, base, flavor))
    return ''
  const prefix = `${base}${sep(flavor)}`
  if (pathEq(abs.slice(0, prefix.length), prefix, flavor))
    return abs.slice(prefix.length)
  return null
}

// Replace a leading home-directory prefix with `~`, using the flavor's
// separator. Win32 matches the home prefix case-insensitively.
export function tildify(absPath: string, homeDir?: string, flavor?: PathFlavor): string {
  if (!homeDir)
    return absPath
  const f = flavorOf(absPath, flavor)
  const homeTrimmed = homeDir.replace(TRAILING_SEP_RE, '')
  const homeNorm = f === 'win32' ? normalizeSeparators(homeTrimmed, f) : homeTrimmed
  const absNorm = f === 'win32' ? normalizeSeparators(absPath, f) : absPath
  const rem = relativeUnder(absNorm, homeNorm, f)
  if (rem === '')
    return '~'
  if (rem !== null)
    return `~${sep(f)}${rem}`
  return absPath
}

// Expand a leading `~` or `~/…` (`~\…` on Win32) against homeDir. Anything
// not starting with `~` is returned unchanged, as is the input if homeDir
// is missing.
export function untildify(value: string, homeDir?: string, flavor?: PathFlavor): string {
  if (!homeDir || !value)
    return value
  if (value === '~')
    return homeDir
  if (!(value.startsWith('~/') || value.startsWith('~\\')))
    return value
  const rest = value.slice(2).replace(LEADING_SEP_RE, '')
  return rest ? join([homeDir, rest], flavor) : homeDir
}

// Return the shortest reasonable rendering of `absPath` against a working
// directory: a direct relative path, an `../` chain, or a tilde path. When
// the paths don't share a root (different drive letters, POSIX vs. UNC) the
// `../` candidate is suppressed.
export function relativizePath(
  absPath: string,
  workingDir?: string,
  homeDir?: string,
  flavor?: PathFlavor,
): string {
  if (!workingDir)
    return absPath
  const f = flavorOf(absPath, flavor)
  const s = sep(f)

  const wdTrimmed = workingDir.replace(TRAILING_SEP_RE, '')
  const absNorm = f === 'win32' ? normalizeSeparators(absPath, f) : absPath
  const wdNorm = f === 'win32' ? normalizeSeparators(wdTrimmed, f) : wdTrimmed

  const rem = relativeUnder(absNorm, wdNorm, f)
  if (rem === '')
    return '.'
  if (rem !== null)
    return rem

  let best = absNorm
  if (rootsMatch(absNorm, wdNorm, f)) {
    const baseParts = split(wdNorm, f)
    const absParts = split(absNorm, f)
    let common = 0
    while (common < baseParts.length && common < absParts.length && pathEq(baseParts[common], absParts[common], f))
      common++
    const ups = baseParts.length - common
    const dotRel = `${'..'.concat(s).repeat(ups)}${absParts.slice(common).join(s)}`
    if (dotRel.length < best.length)
      best = dotRel
  }
  const tildePath = tildify(absNorm, homeDir, f)
  if (tildePath !== absNorm && tildePath.length < best.length)
    best = tildePath
  return best
}

function rootsMatch(a: string, b: string, flavor: PathFlavor): boolean {
  if (flavor === 'posix')
    return a.startsWith('/') === b.startsWith('/')
  const volA = extractVolume(a)
  const volB = extractVolume(b)
  return volA.toLowerCase() === volB.toLowerCase()
}

function extractVolume(p: string): string {
  if (DRIVE_LETTER_RE.test(p))
    return p.slice(0, 2)
  return parseUncHead(p)?.volume ?? ''
}
