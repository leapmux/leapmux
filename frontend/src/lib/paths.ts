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

// Remove every trailing separator. One spelling for a strip that four callers
// need, so a base directory reduces the same way wherever it enters.
//
// Both separators, whatever the flavor: a win32 path reaches this file spelled
// with `/` (a user types `C:/Users`), and a base that keeps one trailing
// separator is a base that matches none of its own descendants.
function stripTrailingSep(p: string): string {
  return p.replace(TRAILING_SEP_RE, '')
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

// The win32 volume of `p` (`C:` or `\\server\share`), WITHOUT a trailing
// separator, or '' when the path has none. Private on purpose: a volume alone
// is not a directory, and `filesystemRoot` is the answer callers want.
function extractVolume(p: string): string {
  if (DRIVE_LETTER_RE.test(p))
    return p.slice(0, 2)
  return parseUncHead(p)?.volume ?? ''
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

/**
 * The filesystem root `p` lives under, or undefined when `p` is not absolute.
 *
 *   filesystemRoot('/etc/hosts', 'posix')        -> '/'
 *   filesystemRoot('C:/Users/a', 'win32')        -> 'C:\\'
 *   filesystemRoot('\\\\srv\\share\\x', 'win32') -> '\\\\srv\\share\\'
 *   filesystemRoot('proj/src', 'posix')          -> undefined
 *   filesystemRoot('\\rooted', 'win32')          -> undefined
 *
 * The answer always ENDS IN the flavor's separator, because that is what a
 * worker's ListDirectory needs. `C:` alone identifies the current directory
 * on drive C, not the drive's root, which is also why `isAbsolute('C:')` is
 * correctly false.
 *
 * A win32 path that is rooted but volume-less (`\foo`, `/foo`) answers
 * undefined: which drive it lands on is the worker's current directory, and
 * the browser cannot know it. Callers fall back.
 */
export function filesystemRoot(p: string, flavor?: PathFlavor): string | undefined {
  if (!p)
    return undefined
  const f = flavorOf(p, flavor)
  if (f === 'posix')
    return p.startsWith('/') ? '/' : undefined
  const volume = extractVolume(p)
  return volume ? `${volume}${sep(f)}` : undefined
}

/**
 * Whether `p` IS a filesystem root, whatever spelling it arrives in.
 *
 *   isFilesystemRoot('/', 'posix')                 -> true
 *   isFilesystemRoot('//', 'posix')                -> true
 *   isFilesystemRoot('C:/', 'win32')               -> true
 *   isFilesystemRoot('\\\\srv\\share', 'win32')      -> true
 *   isFilesystemRoot('C:', 'win32')                -> false
 *   isFilesystemRoot('/home', 'posix')             -> false
 *
 * A root arrives spelled several ways -- `/` and `//`, `C:\` and `C:/`,
 * `\\srv\share` with and without its trailing separator -- and a caller that
 * compares against `filesystemRoot`'s own output recognizes only one of them.
 * A caller that compares against `filesystemRoot`'s own output recognizes
 * only one of them. Counting COMPONENTS recognizes all of them, because
 * `split` drops the empty ones: `//` and `C:/` hold nothing beyond their own
 * root, and `/home` holds one thing more. That matters at both call sites:
 * the tree labels its root row with itself, and "Copy relative path" refuses
 * a root as a base.
 *
 * `C:` stays false. It is drive-relative -- it identifies the current
 * directory on drive C, not the drive's root -- and `filesystemRoot` already
 * answers undefined for it, which is also why `isAbsolute('C:')` is correctly
 * false.
 */
export function isFilesystemRoot(p: string, flavor?: PathFlavor): boolean {
  if (!p)
    return false
  const f = flavorOf(p, flavor)
  const root = filesystemRoot(p, f)
  if (root === undefined)
    return false
  return split(p, f).length === split(root, f).length
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

export function join(parts: string[], flavor?: PathFlavor): string {
  const filtered = parts.filter(p => p !== undefined && p !== null && p !== '')
  if (filtered.length === 0)
    return ''
  const f = flavorOf(filtered[0], flavor)
  const s = sep(f)
  // A first element that is NOTHING BUT separators is a filesystem root: `/`,
  // and win32's volume-less `\`. It becomes a prefix rather than an element,
  // because the loop below strips the trailing separator from every element
  // but the last -- which empties a root and drops it, so `join(['/', 'home'])`
  // answered `'home'`. Every path built from a POSIX root then came out
  // RELATIVE, which is how a tree rooted at `/` lost its git decorations.
  // Win32 hides the defect: `C:\` strips to `C:`, which still identifies the
  // drive.
  const rooted = stripTrailingSep(filtered[0]) === ''
  const out: string[] = []
  for (let i = rooted ? 1 : 0; i < filtered.length; i++) {
    let piece = filtered[i]
    if (i > 0)
      piece = piece.replace(LEADING_SEP_RE, '')
    if (i < filtered.length - 1)
      piece = stripTrailingSep(piece)
    if (piece !== '')
      out.push(piece)
  }
  let joined = out.join(s)
  if (f === 'win32')
    joined = joined.replace(FWD_SLASH_G, '\\')
  // One separator, never two: the root supplies it, so the elements must not.
  return rooted ? `${s}${joined}` : joined
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

/**
 * Lowercase file extension (without the leading dot), or empty string
 * if the last path component has none. Accepts both POSIX and Windows
 * separators without needing a flavor — extensions are universally
 * defined by the last dot inside the final path segment.
 *
 * Examples:
 *   extname('photo.PNG')       → 'png'
 *   extname('archive.tar.gz')  → 'gz'
 *   extname('/etc/hosts')      → ''
 *   extname('.gitignore')      → 'gitignore'
 *   extname('dir.zip/file')    → ''   (dot before last separator)
 */
export function extname(p: string): string {
  const dot = p.lastIndexOf('.')
  if (dot < 0)
    return ''
  const sep = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
  if (dot <= sep)
    return ''
  return p.substring(dot + 1).toLowerCase()
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

/**
 * Rewrite `p` with the flavor's own separator, where the flavor HAS a second
 * one.
 *
 * Win32 accepts both `/` and `\`, so a path a user typed as `C:/Users` and the
 * same path a worker reported as `C:\Users` must reduce to one spelling before
 * any comparison.
 *
 * POSIX returns `p` unchanged, and that is the RULE, not a shortcut. `\` is a
 * legal character in a POSIX file name, so `a\b` is ONE component, and a
 * rewrite to `a/b` makes it compare equal to the directory `a/b`. Use
 * `toPosixSeparators` where the target really is `/`: git reports its paths
 * that way whatever the host OS.
 */
export function normalizeSeparators(p: string, flavor: PathFlavor): string {
  return flavor === 'win32' ? p.replace(FWD_SLASH_G, '\\') : p
}

// Convert any flavor's separators to posix `/`. Useful for comparing against
// git-reported paths, which always use `/` regardless of host OS.
export function toPosixSeparators(p: string): string {
  return p.replace(BACK_SLASH_G, '/')
}

// Compare two path TEXTS under the flavor's case rules: case-insensitive on
// win32, byte-exact on posix.
//
// Text, not paths: it neither cleans nor normalizes separators, and callers
// apply it to a whole path, to one segment, and to a prefix slice. A caller
// that wants "the same directory" must normalize first. Deliberately NOT
// named `samePath`, because Go's `pathutil.SamePath` cleans both inputs and
// the shared name would mean two different things across the two languages.
export function pathEq(a: string, b: string, flavor: PathFlavor): boolean {
  return flavor === 'win32'
    ? a.toLowerCase() === b.toLowerCase()
    : a === b
}

// Returns '' when abs === base, the slice after `base + sep` when abs is
// strictly under base, or null otherwise. Both inputs must already use the
// flavor's separator.
//
// A TRAILING SEPARATOR on `base` is tolerated and stripped, because a
// filesystem root is nothing but one (`/`, `C:\`, `\\srv\share\`). Without
// that, every path under a root answered null -- so a tree rooted at `/` found
// none of its own descendants.
export function relativeUnder(abs: string, base: string, flavor: PathFlavor): string | null {
  if (!abs)
    return null
  // A relative path is under no absolute base. The strip below is what makes
  // this necessary: it turns `C:\` into `C:`, and the equality test would then
  // report the drive-RELATIVE `C:` -- the current directory on drive C -- as
  // the drive's root. Every other spelling survives the strip unchanged.
  if (isAbsolute(base, flavor) && !isAbsolute(abs, flavor))
    return null
  const s = sep(flavor)
  const trimmed = stripTrailingSep(base)
  if (pathEq(abs, base, flavor) || pathEq(abs, trimmed, flavor))
    return ''
  // A root trims to '' (or to the bare volume), so the prefix IS the root.
  // `abs` may spell that root with more separators than `base` did.
  const prefix = `${trimmed}${s}`
  if (pathEq(abs, prefix, flavor))
    return ''
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
  const homeTrimmed = stripTrailingSep(homeDir)
  const homeNorm = normalizeSeparators(homeTrimmed, f)
  const absNorm = normalizeSeparators(absPath, f)
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

  const wdTrimmed = stripTrailingSep(workingDir)
  const absNorm = normalizeSeparators(absPath, f)
  const wdNorm = normalizeSeparators(wdTrimmed, f)

  // A FILESYSTEM ROOT is a base in name only. Every absolute path is under it,
  // so the direct-relative answer below is just the path with its root sliced
  // off -- one character shorter and no more readable. The directory picker's
  // tree is rooted at `/` (or `C:\`), so without this "Copy relative path" on
  // `~/proj/a.ts` answers `home/alice/proj/a.ts`.
  //
  // Only the TILDE candidate competes after this. The `..` candidate cannot:
  // `wdTrimmed` reduces a root to '' (or to the bare volume `C:`), so
  // `rootsMatch` compares an undefined root and answers false. That is the
  // correct outcome -- for a root base the `..` chain is empty, so it would
  // rebuild the same one-character-shorter answer this guard just refused --
  // and a caller outside the home directory therefore gets the ABSOLUTE path.
  // `FileActionsMenu` hides "Copy relative path" for a root base rather than
  // offer a second item that copies what "Copy path" already copies.
  const baseIsRoot = isFilesystemRoot(workingDir, f)

  const rem = relativeUnder(absNorm, wdNorm, f)
  if (rem === '')
    return '.'
  if (rem !== null && !baseIsRoot)
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

// Whether two paths hang off the SAME root: the same drive or UNC share on
// win32, and both-absolute-or-both-relative on posix. Built on
// `filesystemRoot` so "what root does this path have" is stated once.
function rootsMatch(a: string, b: string, flavor: PathFlavor): boolean {
  const rootA = filesystemRoot(a, flavor)
  const rootB = filesystemRoot(b, flavor)
  if (rootA === undefined || rootB === undefined)
    return rootA === rootB
  return pathEq(rootA, rootB, flavor)
}
