import { extname, lastSepIndex } from '~/lib/paths'

/** The value the file listing is ordered by. */
export type FileSortKey = 'name' | 'modified' | 'size' | 'type'

export type FileSortDirection = 'asc' | 'desc'

export interface FileSortOrder {
  key: FileSortKey
  direction: FileSortDirection
}

export const DEFAULT_FILE_SORT_ORDER: FileSortOrder = { key: 'name', direction: 'asc' }

export const FILE_SORT_KEYS: readonly FileSortKey[] = ['name', 'modified', 'size', 'type']
export const FILE_SORT_DIRECTIONS: readonly FileSortDirection[] = ['asc', 'desc']

const SORT_KEY_LABELS: Record<FileSortKey, string> = {
  name: 'Name',
  modified: 'Last modified',
  size: 'Size',
  type: 'Type',
}

/**
 * Direction labels follow the criterion, because "ascending" says nothing
 * useful about a timestamp or a byte count.
 */
const DIRECTION_LABELS: Record<FileSortKey, Record<FileSortDirection, string>> = {
  name: { asc: 'A → Z', desc: 'Z → A' },
  type: { asc: 'A → Z', desc: 'Z → A' },
  modified: { asc: 'Oldest first', desc: 'Newest first' },
  size: { asc: 'Smallest first', desc: 'Largest first' },
}

export function sortKeyLabel(key: FileSortKey): string {
  return SORT_KEY_LABELS[key]
}

export function sortDirectionLabel(key: FileSortKey, direction: FileSortDirection): string {
  return DIRECTION_LABELS[key][direction]
}

/**
 * Validates a value read back from browser storage. The stored shape is
 * arbitrary JSON that a previous build (or a hand edit) may have written, and
 * an unrecognized key would otherwise reach the comparator's lookup tables.
 */
export function parseFileSortOrder(raw: unknown): FileSortOrder {
  if (!raw || typeof raw !== 'object')
    return DEFAULT_FILE_SORT_ORDER
  const { key, direction } = raw as { key?: unknown, direction?: unknown }
  if (!FILE_SORT_KEYS.includes(key as FileSortKey) || !FILE_SORT_DIRECTIONS.includes(direction as FileSortDirection))
    return DEFAULT_FILE_SORT_ORDER
  return { key: key as FileSortKey, direction: direction as FileSortDirection }
}

// Case-insensitive but accent-sensitive. `sensitivity: 'base'` would
// additionally fold accents together, so `resume` and `résumé` would tie and
// fall to the code-unit tiebreak below.
//
// The locale is PINNED, never the host default: `Intl.Collator(undefined, …)`
// resolves against whatever locale the browser runs in, and the locales
// disagree -- `sv-SE` sorts `ä` after `z` where `en` sorts it with `a`. Two
// people looking at the same directory would see two different orders, and a
// test asserting one of them would pass on a developer's machine and fail in
// CI.
//
// This order is the DISPLAYED one, and it is not the worker's. The worker sorts
// by lowercased UTF-8 bytes (`cmp.Compare` over `strings.ToLower` in
// `listDirectory`), which disagrees with ICU on more than accents -- `a_1.txt`
// versus `a1.txt` already differs on pure ASCII. That costs nothing for a
// directory the worker returns whole, because the frontend re-sorts every entry
// it receives. It matters only for a directory the worker TRUNCATES, where the
// byte order picks which entries survive the cut; `truncationNotice` in
// DirectoryTree is what tells the user they are looking at a window.
const nameCollator = new Intl.Collator('en', { sensitivity: 'accent' })

/**
 * Compares two names case-insensitively, then breaks a remaining tie by code
 * unit. Without the tiebreak, `README` and `readme` compare equal and their
 * relative order is whatever the sort algorithm happens to produce -- which can
 * differ between two renders of the same directory and make rows jump.
 */
export function compareNames(a: string, b: string): number {
  const byCollator = nameCollator.compare(a, b)
  if (byCollator !== 0)
    return byCollator
  if (a === b)
    return 0
  return a < b ? -1 : 1
}

/**
 * The extension a `type` sort groups by, lowercased and without the dot.
 *
 * A LEADING dot starts a dotfile; it does not begin an extension. So
 * `.gitignore` has none and groups with `Makefile` and `LICENSE`, while
 * `.eslintrc.json` has `json` and groups with the other JSON files. The rule is
 * "a dot at index > 0 of the basename", which is the one `extname` in
 * `~/lib/paths` applies to a full path -- it only mistakes the leading dot
 * because a bare basename gives it no separator to measure against.
 *
 * `extname` itself must NOT be changed to match: `languageMap` maps the
 * extension `env` to `dotenv`, which depends on `extname('.env')` returning
 * `'env'`.
 */
export function fileExtension(name: string): string {
  const base = name.slice(lastSepIndex(name, 'win32') + 1)
  return base.lastIndexOf('.') > 0 ? extname(base) : ''
}

/**
 * How to read the sortable values off one entry type. Accessors rather than a
 * shared entry interface, because the two call sites hold different shapes
 * (a tree node and a git status entry) and neither may be copied into a wrapper
 * object: building one per comparison allocates O(n log n) objects, and
 * pre-decorating the array hands `<For>` fresh objects, which disposes and
 * rebuilds every row instead of moving it.
 */
export interface FileSortFields<T> {
  /** The name a `name` sort orders by, and every other sort's tiebreak. */
  name: (entry: T) => string
  isDir: (entry: T) => boolean
  /** Bytes, or undefined when the entry could not be stat-ed. */
  size: (entry: T) => number | undefined
  /** RFC3339, or undefined when the entry could not be stat-ed. */
  modTime: (entry: T) => string | undefined
}

/**
 * Compare two optional values, or `undefined` when the caller must break the
 * tie itself.
 *
 * The rule both this module and `~/lib/workspaceSort` need, stated once: an
 * ABSENT value sorts LAST under both directions, because the pin happens before
 * the direction flip -- so "unknown" never migrates to the top. Two present and
 * equal values, and two absent ones, are a tie.
 *
 * It answers `undefined` for a tie rather than `0`, so a caller can write
 * `compareOptionalValue(...) ?? byName(a, b)` without `??` swallowing a real
 * comparison result of `0`.
 *
 * A caller whose "absent" is an empty string rather than `undefined` passes
 * `value || undefined`, which keeps that choice at the call site.
 */
export function compareOptionalValue<V extends string | number>(
  av: V | undefined,
  bv: V | undefined,
  flip: number,
): number | undefined {
  if (av === undefined || bv === undefined) {
    if (av === bv)
      return undefined
    return av === undefined ? 1 : -1
  }
  if (av === bv)
    return undefined
  return (av < bv ? -1 : 1) * flip
}

/**
 * Builds the comparator for one sort order.
 *
 * The rules, in order:
 *
 * 1. Directories come before files, under every criterion.
 * 2. Two directories compare by name ASCENDING, ignoring the direction, unless
 *    the criterion is `name`. A directory has no size and no extension, and its
 *    modification time moves only when its direct children are added or removed
 *    -- so ordering folders by any of those three reads as arbitrary.
 * 3. Two files compare by the criterion, then the direction is applied.
 * 4. Every criterion falls back to a name comparison on a tie, so the order is
 *    stable across refreshes.
 * 5. An entry with no size / modTime sorts LAST under both directions. The pin
 *    happens before the direction flip, so "unknown" never migrates to the top.
 */
export function makeFileComparator<T>(order: FileSortOrder, fields: FileSortFields<T>): (a: T, b: T) => number {
  const flip = order.direction === 'desc' ? -1 : 1

  const byName = (a: T, b: T) => compareNames(fields.name(a), fields.name(b))

  return (a, b) => {
    const aDir = fields.isDir(a)
    if (aDir !== fields.isDir(b))
      return aDir ? -1 : 1

    if (aDir)
      return order.key === 'name' ? byName(a, b) * flip : byName(a, b)

    switch (order.key) {
      case 'name':
        return byName(a, b) * flip
      case 'size':
        // A size of 0 is a real size, so only `undefined` counts as absent.
        return compareOptionalValue(fields.size(a), fields.size(b), flip) ?? byName(a, b)
      case 'modified':
        // The worker writes every modification time in one fixed-width UTC
        // layout (`modTimeLayout` in the worker's file.go: RFC3339 with all
        // nine nanosecond digits), so a lexicographic compare is a
        // chronological one and needs no parsing. Anything that varies the
        // width -- a stock RFC3339Nano, which trims trailing zeros -- silently
        // breaks that.
        //
        // An EMPTY string means "not reported", so it maps to `undefined`.
        return compareOptionalValue(
          fields.modTime(a) || undefined,
          fields.modTime(b) || undefined,
          flip,
        ) ?? byName(a, b)
      case 'type': {
        const aExt = fileExtension(fields.name(a))
        const bExt = fileExtension(fields.name(b))
        return aExt === bExt ? byName(a, b) : compareNames(aExt, bExt) * flip
      }
    }
  }
}
